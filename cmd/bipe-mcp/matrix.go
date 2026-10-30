package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
	"github.com/bipe-chat/bipe-beeper/pkg/bipe/mcpserver"
	"github.com/bipe-chat/bipe-beeper/pkg/e2ee"
)

// matrixClient is the Matrix session of the bipe-mcp device. It syncs in the
// background with the mautrix-go crypto helper, so room keys arrive and
// messages are decrypted and encrypted on this machine. It implements
// mcpserver.Matrix.
type matrixClient struct {
	cli    *mautrix.Client
	helper *cryptohelper.CryptoHelper
	tokens *tokenKeeper
	log    zerolog.Logger

	ready     chan struct{}
	readyOnce sync.Once
}

var _ mcpserver.Matrix = (*matrixClient)(nil)

const (
	readyTimeout = 60 * time.Second
	pageSize     = 100
	maxPages     = 10
)

// openMatrix makes the client and opens the crypto store. It does not sync.
func openMatrix(ctx context.Context, dir string, sess *storedSession, tokens *tokenKeeper, log zerolog.Logger) (*matrixClient, error) {
	cli, err := mautrix.NewClient(sess.HomeserverURL, id.UserID(sess.UserID), "")
	if err != nil {
		return nil, err
	}
	cli.DeviceID = id.DeviceID(sess.DeviceID)
	// Info level: mautrix-go logs request bodies at debug level.
	cli.Log = log.With().Str("component", "matrix").Logger().Level(zerolog.InfoLevel)
	tokens.attach(cli)
	if tokens.expiresWithin(refreshMargin) {
		if err = tokens.Refresh(ctx); errors.Is(err, errSessionEnded) {
			return nil, err
		} else if err != nil {
			log.Warn().Str("reason", errorKind(err)).Msg("Failed to refresh the token before connecting")
		}
	}
	cli.Syncer = mautrix.NewDefaultSyncer()
	helper, err := cryptohelper.NewCryptoHelper(cli, []byte(sess.PickleKey), cryptoPath(dir))
	if err != nil {
		return nil, fmt.Errorf("failed to make the crypto helper: %w", err)
	}
	err = helper.Init(ctx)
	if errors.Is(err, mautrix.MUnknownToken) {
		if rerr := tokens.Refresh(ctx); rerr != nil {
			return nil, rerr
		}
		err = helper.Init(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to start end-to-end encryption: %w", err)
	}
	cli.Crypto = helper
	m := &matrixClient{cli: cli, helper: helper, tokens: tokens, log: log, ready: make(chan struct{})}
	return m, nil
}

// loadSecrets imports the cross-signing keys that the sign-in stored.
func (m *matrixClient) loadSecrets(ctx context.Context) {
	if _, err := e2ee.LoadStoredSecrets(ctx, m.helper.Machine()); err != nil {
		m.log.Warn().Err(err).Msg("Failed to load the stored E2EE secrets")
	}
}

func (m *matrixClient) close() {
	m.cli.StopSync()
	if err := m.helper.Close(); err != nil {
		m.log.Err(err).Msg("Failed to close the crypto store")
	}
}

// startSync syncs until ctx ends or the session ends. The first sync after
// start has no timeout, so ready closes quickly.
func (m *matrixClient) startSync(ctx context.Context) {
	syncer := m.cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnSync(func(context.Context, *mautrix.RespSync, string) bool {
		m.readyOnce.Do(func() { close(m.ready) })
		return true
	})
	go func() {
		unknownToken := 0
		for {
			err := m.cli.SyncWithContext(ctx)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, mautrix.MUnknownToken) {
				// A fresh token that is still unknown means the device is
				// gone.
				unknownToken++
				if unknownToken > 2 {
					m.tokens.markEnded()
				} else if rerr := m.tokens.Refresh(ctx); rerr == nil || !errors.Is(rerr, errSessionEnded) {
					continue
				}
				m.log.Warn().Msg("The session ended")
				return
			}
			unknownToken = 0
			m.log.Warn().Str("reason", errorKind(err)).Msg("Sync stopped, retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
		}
	}()
}

// waitReady waits for the first sync, so the state store knows which rooms
// are encrypted before anything is sent.
func (m *matrixClient) waitReady(ctx context.Context) error {
	if m.tokens.Ended() {
		return mcpserver.UserError(bipe.MCPSessionEnded)
	}
	select {
	case <-m.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(readyTimeout):
		return mcpserver.UserError(bipe.MCPNotReady)
	}
}

func (m *matrixClient) UserID() id.UserID { return m.cli.UserID }

func (m *matrixClient) JoinedRooms(ctx context.Context) ([]id.RoomID, error) {
	if err := m.waitReady(ctx); err != nil {
		return nil, err
	}
	resp, err := m.cli.JoinedRooms(ctx)
	if err != nil {
		return nil, err
	}
	return resp.JoinedRooms, nil
}

// Chats asks the server for a small sync with only names, members, the
// last event and unread counts. The answer is not given to the syncer, so
// the sync token and the to-device queue do not change.
func (m *matrixClient) Chats(ctx context.Context) ([]mcpserver.Chat, error) {
	if err := m.waitReady(ctx); err != nil {
		return nil, err
	}
	none := &mautrix.FilterPart{NotTypes: []event.Type{{Type: "*"}}}
	filter := mautrix.Filter{
		AccountData: &mautrix.FilterPart{Types: []event.Type{event.AccountDataDirectChats}},
		Presence:    none,
		Room: &mautrix.RoomFilter{
			State: &mautrix.FilterPart{
				Types:           []event.Type{event.StateRoomName, event.StateCanonicalAlias, event.StateMember},
				LazyLoadMembers: true,
			},
			Timeline:    &mautrix.FilterPart{Limit: 1, LazyLoadMembers: true},
			Ephemeral:   none,
			AccountData: none,
		},
	}
	filterJSON, err := json.Marshal(filter)
	if err != nil {
		return nil, err
	}
	resp, err := m.cli.FullSyncRequest(ctx, mautrix.ReqSync{FilterID: string(filterJSON)})
	if err != nil {
		return nil, err
	}
	direct := map[id.RoomID]bool{}
	for _, evt := range resp.AccountData.Events {
		if evt.Type != event.AccountDataDirectChats {
			continue
		}
		var dm map[id.UserID][]id.RoomID
		if json.Unmarshal(evt.Content.VeryRaw, &dm) == nil {
			for _, rooms := range dm {
				for _, r := range rooms {
					direct[r] = true
				}
			}
		}
	}
	chats := make([]mcpserver.Chat, 0, len(resp.Rooms.Join))
	var lastTS = map[id.RoomID]int64{}
	for roomID, room := range resp.Rooms.Join {
		c := mcpserver.Chat{RoomID: roomID, Direct: direct[roomID], Name: m.roomName(room)}
		if room.UnreadNotifications != nil {
			c.Unread = room.UnreadNotifications.NotificationCount
			c.Highlights = room.UnreadNotifications.HighlightCount
		}
		if n := len(room.Timeline.Events); n > 0 {
			ts := room.Timeline.Events[n-1].Timestamp
			lastTS[roomID] = ts
			c.LastActivity = time.UnixMilli(ts).UTC().Format(time.RFC3339)
		}
		chats = append(chats, c)
	}
	slices.SortFunc(chats, func(a, b mcpserver.Chat) int {
		if d := lastTS[b.RoomID] - lastTS[a.RoomID]; d != 0 {
			return int(min(max(d, -1), 1))
		}
		return strings.Compare(string(a.RoomID), string(b.RoomID))
	})
	return chats, nil
}

// roomName follows the Matrix room name rules: m.room.name, the canonical
// alias, then the heroes.
func (m *matrixClient) roomName(room *mautrix.SyncJoinedRoom) string {
	members := map[id.UserID]string{}
	var name, alias string
	for _, evt := range append(room.State.Events, room.Timeline.Events...) {
		_ = evt.Content.ParseRaw(evt.Type)
		switch evt.Type {
		case event.StateRoomName:
			if c := evt.Content.AsRoomName(); c != nil {
				name = c.Name
			}
		case event.StateCanonicalAlias:
			if c := evt.Content.AsCanonicalAlias(); c != nil {
				alias = string(c.Alias)
			}
		case event.StateMember:
			if c := evt.Content.AsMember(); c != nil && evt.StateKey != nil {
				members[id.UserID(*evt.StateKey)] = c.Displayname
			}
		}
	}
	if name != "" {
		return name
	}
	if alias != "" {
		return alias
	}
	var heroes []string
	for _, h := range room.Summary.Heroes {
		if h == m.cli.UserID {
			continue
		}
		if dn := members[h]; dn != "" {
			heroes = append(heroes, dn)
		} else {
			heroes = append(heroes, h.Localpart())
		}
	}
	return strings.Join(heroes, ", ")
}

// Messages pages back through the room. With a sender, the server filter
// asks only for that sender's events, and the loop drops any other sender
// before decryption.
func (m *matrixClient) Messages(ctx context.Context, roomID id.RoomID, sender id.UserID, limit int) ([]mcpserver.Message, error) {
	if err := m.waitReady(ctx); err != nil {
		return nil, err
	}
	filter := &mautrix.FilterPart{
		Types:           []event.Type{event.EventMessage, event.EventEncrypted},
		LazyLoadMembers: true,
	}
	if sender != "" {
		filter.Senders = []id.UserID{sender}
	}
	var out []mcpserver.Message
	from := ""
	for page := 0; page < maxPages && len(out) < limit; page++ {
		resp, err := m.cli.Messages(ctx, roomID, from, "", mautrix.DirectionBackward, filter, pageSize)
		if err != nil {
			return nil, err
		}
		for _, evt := range resp.Chunk {
			if sender != "" && evt.Sender != sender {
				continue
			}
			evt.RoomID = roomID
			if msg, ok := m.convert(ctx, evt); ok {
				out = append(out, msg)
				if len(out) == limit {
					break
				}
			}
		}
		if resp.End == "" || len(resp.Chunk) == 0 {
			break
		}
		from = resp.End
	}
	slices.Reverse(out)
	return out, nil
}

func (m *matrixClient) convert(ctx context.Context, evt *event.Event) (mcpserver.Message, bool) {
	msg := mcpserver.Message{
		EventID: evt.ID,
		Sender:  evt.Sender,
		Time:    time.UnixMilli(evt.Timestamp).UTC().Format(time.RFC3339),
	}
	if evt.Unsigned.RedactedBecause != nil {
		return msg, false
	}
	_ = evt.Content.ParseRaw(evt.Type)
	if evt.Type == event.EventEncrypted {
		dec, err := m.helper.Decrypt(ctx, evt)
		if err != nil {
			msg.Undecryptable = true
			return msg, true
		}
		evt = dec
		if evt.Content.Parsed == nil {
			_ = evt.Content.ParseRaw(evt.Type)
		}
	}
	if evt.Type != event.EventMessage {
		return msg, false
	}
	c := evt.Content.AsMessage()
	if c == nil {
		return msg, false
	}
	msg.MsgType = string(c.MsgType)
	msg.Body = c.Body
	if rel := c.RelatesTo; rel != nil {
		msg.ReplyTo = rel.GetReplyTo()
		msg.ThreadRoot = rel.GetThreadParent()
		if r := rel.GetReplaceID(); r != "" {
			msg.Replaces = r
			if c.NewContent != nil {
				msg.Body = c.NewContent.Body
			}
		}
	}
	return msg, true
}

func (m *matrixClient) AccountData(ctx context.Context, eventType string) (json.RawMessage, error) {
	if m.tokens.Ended() {
		return nil, mcpserver.UserError(bipe.MCPSessionEnded)
	}
	var raw json.RawMessage
	err := m.cli.GetAccountData(ctx, eventType, &raw)
	if errors.Is(err, mautrix.MNotFound) {
		return nil, nil
	}
	return raw, err
}

func (m *matrixClient) SetAccountData(ctx context.Context, eventType string, content any) error {
	return m.cli.SetAccountData(ctx, eventType, content)
}

func stateType(t string) event.Type { return event.Type{Type: t, Class: event.StateEventType} }

func (m *matrixClient) RoomState(ctx context.Context, roomID id.RoomID, eventType, stateKey string) (json.RawMessage, error) {
	if m.tokens.Ended() {
		return nil, mcpserver.UserError(bipe.MCPSessionEnded)
	}
	var raw json.RawMessage
	err := m.cli.StateEvent(ctx, roomID, stateType(eventType), stateKey, &raw)
	if errors.Is(err, mautrix.MNotFound) {
		return nil, nil
	}
	return raw, err
}

func (m *matrixClient) SetRoomState(ctx context.Context, roomID id.RoomID, eventType, stateKey string, content any) error {
	if err := m.waitReady(ctx); err != nil {
		return err
	}
	_, err := m.cli.SendStateEvent(ctx, roomID, stateType(eventType), stateKey, content)
	return err
}

func (m *matrixClient) CreateDraftsRoom(ctx context.Context, name, roomType string) (id.RoomID, error) {
	if err := m.waitReady(ctx); err != nil {
		return "", err
	}
	resp, err := m.cli.CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Visibility:      "private",
		Preset:          "private_chat",
		Name:            name,
		CreationContent: map[string]any{"type": roomType},
		InitialState: []*event.Event{
			{Type: event.StateEncryption, Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}}},
			{Type: event.StateJoinRules, Content: event.Content{Parsed: &event.JoinRulesEventContent{JoinRule: event.JoinRuleInvite}}},
			{Type: event.StateHistoryVisibility, Content: event.Content{Parsed: &event.HistoryVisibilityEventContent{HistoryVisibility: event.HistoryVisibilityJoined}}},
		},
	})
	if err != nil {
		return "", err
	}
	return resp.RoomID, nil
}

// SendMessage sends an encrypted message when the room is encrypted. Before
// the send it asks the server for the encryption state and the members, so
// an old state store never makes it send plain text into an encrypted room.
func (m *matrixClient) SendMessage(ctx context.Context, roomID id.RoomID, content map[string]any) (id.EventID, error) {
	if err := m.waitReady(ctx); err != nil {
		return "", err
	}
	var enc event.EncryptionEventContent
	err := m.cli.StateEvent(ctx, roomID, event.StateEncryption, "", &enc)
	encrypted := err == nil && enc.Algorithm != ""
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		return "", fmt.Errorf("failed to check the room encryption: %w", err)
	}
	if encrypted {
		known, err := m.cli.StateStore.IsEncrypted(ctx, roomID)
		if err != nil || !known {
			return "", errors.New("the state store does not know that the room is encrypted")
		}
		if _, err = m.cli.Members(ctx, roomID); err != nil {
			return "", fmt.Errorf("failed to get the room members: %w", err)
		}
	}
	resp, err := m.cli.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}
