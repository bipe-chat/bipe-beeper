package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

// writeDeviceEntry writes the device entry of the bridge into the drafts
// room of the account (bipe ADR 0291, bipe-chat/bipe#1485; bipe-shared
// docs/drafts-room.md, "Device entries"). It runs after the first sync of
// each connection. It writes on link, and on start only when a field
// changed since the last write. A failure does not stop the bridge; the
// next start tries again.
func (c *Client) writeDeviceEntry(ctx context.Context) {
	log := zerolog.Ctx(ctx).With().Str("action", "write device entry").Logger()
	meta, ok := c.login.Metadata.(*UserLoginMetadata)
	if !ok || c.cli == nil {
		return
	}
	info := bipe.BridgeDeviceInfo(c.cli.DeviceID, c.main.Version, meta.LinkedAt)
	fingerprint := info.Fingerprint()
	if meta.DeviceInfoFingerprint == fingerprint {
		return
	}
	joined, err := c.cli.JoinedRooms(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to list the joined rooms")
		return
	}
	roomID, err := bipe.DraftsRoom(ctx, draftsStore{c.cli}, joined.JoinedRooms)
	if err != nil {
		log.Err(err).Msg("Failed to find or create the drafts room")
		return
	}
	if err = c.sendEncrypted(ctx, roomID, info.Content()); err != nil {
		log.Err(err).Msg("Failed to send the device entry")
		return
	}
	meta.DeviceInfoFingerprint = fingerprint
	if err = c.login.Save(ctx); err != nil {
		log.Err(err).Msg("Failed to save the device entry fingerprint")
	}
	log.Info().Msg("Wrote the device entry")
}

// sendEncrypted sends an m.room.message into an encrypted room. It reads the
// encryption state from the server, so a new room or an old state store
// never makes it send plain text. A room without encryption gets nothing.
func (c *Client) sendEncrypted(ctx context.Context, roomID id.RoomID, content map[string]any) error {
	var enc event.EncryptionEventContent
	if err := c.cli.StateEvent(ctx, roomID, event.StateEncryption, "", &enc); err != nil {
		return fmt.Errorf("failed to read the room encryption: %w", err)
	}
	if enc.Algorithm == "" {
		return errors.New("the room is not encrypted")
	}
	if err := c.cli.StateStore.SetEncryptionEvent(ctx, roomID, &enc); err != nil {
		return fmt.Errorf("failed to save the room encryption: %w", err)
	}
	if _, err := c.cli.Members(ctx, roomID); err != nil {
		return fmt.Errorf("failed to get the room members: %w", err)
	}
	_, err := c.cli.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	return err
}

// draftsStore gives bipe.DraftsRoom the account data and room creation of
// the remote Matrix client.
type draftsStore struct{ cli *mautrix.Client }

func (s draftsStore) AccountData(ctx context.Context, eventType string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.cli.GetAccountData(ctx, eventType, &raw)
	if errors.Is(err, mautrix.MNotFound) {
		return nil, nil
	}
	return raw, err
}

func (s draftsStore) SetAccountData(ctx context.Context, eventType string, content any) error {
	return s.cli.SetAccountData(ctx, eventType, content)
}

func (s draftsStore) CreateDraftsRoom(ctx context.Context, name, roomType string) (id.RoomID, error) {
	resp, err := s.cli.CreateRoom(ctx, bipe.DraftsRoomRequest(name, roomType))
	if err != nil {
		return "", err
	}
	return resp.RoomID, nil
}
