package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Client is the Matrix client of one login on the remote homeserver.
// It syncs with the remote homeserver, decrypts the events and gives them to the bridge.
type Client struct {
	main   *Connector
	login  *bridgev2.UserLogin
	userID id.UserID
	cli    *mautrix.Client
	crypto *cryptohelper.CryptoHelper

	stopSync  context.CancelFunc
	loggedIn  atomic.Bool
	connected atomic.Bool
	// initialSync is true while the client handles the first sync of its device.
	initialSync atomic.Bool

	dmLock  sync.RWMutex
	dmRooms map[id.RoomID]id.UserID

	typingLock sync.Mutex
	typing     map[id.RoomID]map[id.UserID]struct{}
}

var (
	_ bridgev2.NetworkAPI                        = (*Client)(nil)
	_ bridgev2.NetworkAPIWithUserID              = (*Client)(nil)
	_ bridgev2.BackfillingNetworkAPI             = (*Client)(nil)
	_ bridgev2.EditHandlingNetworkAPI            = (*Client)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI        = (*Client)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI       = (*Client)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI     = (*Client)(nil)
	_ bridgev2.TypingHandlingNetworkAPI          = (*Client)(nil)
	_ bridgev2.RoomNameHandlingNetworkAPI        = (*Client)(nil)
	_ bridgev2.RoomTopicHandlingNetworkAPI       = (*Client)(nil)
	_ bridgev2.RoomAvatarHandlingNetworkAPI      = (*Client)(nil)
	_ bridgev2.PollHandlingNetworkAPI            = (*Client)(nil)
	_ bridgev2.MessageRequestAcceptingNetworkAPI = (*Client)(nil)
	_ bridgev2.DeleteChatHandlingNetworkAPI      = (*Client)(nil)
)

// NewClient makes the client of a login from its metadata. It does not connect.
func NewClient(main *Connector, login *bridgev2.UserLogin) *Client {
	c := &Client{
		main:    main,
		login:   login,
		userID:  id.UserID(login.ID),
		dmRooms: make(map[id.RoomID]id.UserID),
		typing:  make(map[id.RoomID]map[id.UserID]struct{}),
	}
	meta, _ := login.Metadata.(*UserLoginMetadata)
	if meta == nil || meta.AccessToken == "" || meta.HomeserverURL == "" {
		return c
	}
	cli, err := mautrix.NewClient(meta.HomeserverURL, c.userID, meta.AccessToken)
	if err != nil {
		login.Log.Err(err).Msg("Failed to make the remote Matrix client")
		return c
	}
	cli.DeviceID = id.DeviceID(meta.DeviceID)
	cli.Log = login.Log.With().Str("component", "remote matrix").Logger()
	c.cli = cli
	c.loggedIn.Store(true)
	return c
}

// cryptoStorePath is the SQLite file that keeps the Olm account and the Megolm sessions of a login.
func cryptoStorePath(loginID networkid.UserLoginID) string {
	sum := sha256.Sum256([]byte(loginID))
	return "remote-" + hex.EncodeToString(sum[:])[:12] + ".db"
}

func (c *Client) Connect(ctx context.Context) {
	if c.cli == nil {
		c.login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Error:      "remote-not-logged-in",
		})
		return
	}
	log := c.login.Log.With().Str("action", "connect").Logger()
	meta := c.login.Metadata.(*UserLoginMetadata)
	c.cli.Syncer = mautrix.NewDefaultSyncer()
	helper, err := cryptohelper.NewCryptoHelper(c.cli, []byte(meta.PickleKey), cryptoStorePath(c.login.ID))
	if err != nil {
		log.Err(err).Msg("Failed to make the crypto helper")
		c.sendUnknownError("remote-crypto-init-failed", err)
		return
	}
	helper.DecryptErrorCallback = c.handleDecryptError
	err = helper.Init(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to start end-to-end encryption")
		if errors.Is(err, mautrix.MUnknownToken) {
			c.setBadCredentials(err)
		} else {
			c.sendUnknownError("remote-crypto-init-failed", err)
		}
		return
	}
	c.crypto = helper
	c.cli.Crypto = helper

	// Register after the crypto helper, so that the state store is current when these run.
	syncer := c.cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnSync(c.handleSync)
	syncer.OnEvent(c.handleEvent)

	c.loadDirectRooms(ctx)
	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})

	syncCtx, cancel := context.WithCancel(log.WithContext(context.Background()))
	c.stopSync = cancel
	go c.syncLoop(syncCtx)
}

func (c *Client) syncLoop(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	for {
		err := c.cli.SyncWithContext(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, mautrix.MUnknownToken) {
			c.setBadCredentials(err)
			return
		}
		log.Err(err).Msg("Sync with the remote homeserver stopped, retrying")
		c.connected.Store(false)
		c.login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Error:      "remote-sync-failed",
			Message:    err.Error(),
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (c *Client) setBadCredentials(err error) {
	c.loggedIn.Store(false)
	c.login.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      "remote-token-invalid",
		Message:    err.Error(),
	})
}

func (c *Client) sendUnknownError(code status.BridgeStateErrorCode, err error) {
	c.login.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateUnknownError,
		Error:      code,
		Message:    err.Error(),
	})
}

func (c *Client) Disconnect() {
	if c.stopSync != nil {
		c.stopSync()
		c.stopSync = nil
	}
	if c.cli != nil {
		c.cli.StopSync()
	}
	if c.crypto != nil {
		if err := c.crypto.Close(); err != nil {
			c.login.Log.Err(err).Msg("Failed to close the crypto store")
		}
		c.crypto = nil
	}
}

func (c *Client) IsLoggedIn() bool {
	return c.cli != nil && c.loggedIn.Load()
}

// LogoutRemote signs out the bridge device, which deletes its access token and device keys on the homeserver.
func (c *Client) LogoutRemote(ctx context.Context) {
	if c.cli == nil {
		return
	}
	_, err := c.cli.Logout(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to sign out of the remote homeserver")
	}
	c.Disconnect()
	c.loggedIn.Store(false)
}

func (c *Client) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return networkid.UserID(c.login.ID) == userID
}

func (c *Client) GetUserID() networkid.UserID {
	return networkid.UserID(c.userID)
}

func (c *Client) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	return roomFeatures
}

// roomFeatures are the same in every room: both sides are Matrix.
var roomFeatures = &event.RoomFeatures{
	ID: "chat.bipe.beeper.capabilities.2026_10_18",
	Formatting: event.FormattingFeatureMap{
		event.FmtBold:          event.CapLevelFullySupported,
		event.FmtItalic:        event.CapLevelFullySupported,
		event.FmtUnderline:     event.CapLevelFullySupported,
		event.FmtStrikethrough: event.CapLevelFullySupported,
		event.FmtInlineCode:    event.CapLevelFullySupported,
		event.FmtCodeBlock:     event.CapLevelFullySupported,
		event.FmtBlockquote:    event.CapLevelFullySupported,
		event.FmtInlineLink:    event.CapLevelFullySupported,
		event.FmtUserLink:      event.CapLevelFullySupported,
		event.FmtUnorderedList: event.CapLevelFullySupported,
		event.FmtOrderedList:   event.CapLevelFullySupported,
		event.FmtSpoiler:       event.CapLevelFullySupported,
		event.FmtHeaders:       event.CapLevelFullySupported,
	},
	File: event.FileFeatureMap{
		event.MsgImage:      anyFile,
		event.MsgVideo:      anyFile,
		event.MsgAudio:      anyFile,
		event.MsgFile:       anyFile,
		event.CapMsgVoice:   anyFile,
		event.CapMsgGIF:     anyFile,
		event.CapMsgSticker: anyFile,
	},
	Poll:                event.CapLevelFullySupported,
	Thread:              event.CapLevelFullySupported,
	Reply:               event.CapLevelFullySupported,
	Edit:                event.CapLevelFullySupported,
	Delete:              event.CapLevelFullySupported,
	Reaction:            event.CapLevelFullySupported,
	ReadReceipts:        true,
	TypingNotifications: true,
	DeleteChat:          true,
	// An invite is accepted with the button. A message cannot go to a room before the user joins it.
	MessageRequest: &event.MessageRequestFeatures{
		AcceptWithButton: event.CapLevelFullySupported,
	},
}

var anyFile = &event.FileFeatures{
	MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
	Caption:   event.CapLevelFullySupported,
}

func (c *Client) portalKey(roomID id.RoomID) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(roomID), Receiver: c.login.ID}
}

func (c *Client) sender(userID id.UserID) bridgev2.EventSender {
	if userID == c.userID {
		return bridgev2.EventSender{IsFromMe: true, SenderLogin: c.login.ID, Sender: networkid.UserID(userID)}
	}
	return bridgev2.EventSender{Sender: networkid.UserID(userID)}
}

func (c *Client) requireLogin() error {
	if !c.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	return nil
}

func roomIDOf(portal *bridgev2.Portal) id.RoomID {
	return id.RoomID(portal.ID)
}
