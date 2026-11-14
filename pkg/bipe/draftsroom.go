package bipe

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// The drafts room (bipe-shared docs/drafts-room.md, "The room"). bipe-mcp
// writes drafts and its device entry there; the Beeper bridge writes its
// device entry there. Both use this code to find or create the room.

// DraftsRoomRequest is the request that creates the drafts room by the rules
// of bipe-shared docs/drafts-room.md, "The room": encrypted, invite only, no
// invites, history visibility joined. DraftsRoom passes DraftsRoomName and
// the registry roomType.
func DraftsRoomRequest(name, roomType string) *mautrix.ReqCreateRoom {
	return &mautrix.ReqCreateRoom{
		Visibility:      "private",
		Preset:          "private_chat",
		Name:            name,
		CreationContent: map[string]any{"type": roomType},
		InitialState: []*event.Event{
			{Type: event.StateEncryption, Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}}},
			{Type: event.StateJoinRules, Content: event.Content{Parsed: &event.JoinRulesEventContent{JoinRule: event.JoinRuleInvite}}},
			{Type: event.StateHistoryVisibility, Content: event.Content{Parsed: &event.HistoryVisibilityEventContent{HistoryVisibility: event.HistoryVisibilityJoined}}},
		},
	}
}

// DraftsRoomStore is what DraftsRoom needs from a Matrix session.
type DraftsRoomStore interface {
	// AccountData returns global account data, or nil when the account has
	// none of that type.
	AccountData(ctx context.Context, eventType string) (json.RawMessage, error)
	// SetAccountData writes global account data.
	SetAccountData(ctx context.Context, eventType string, content any) error
	// CreateDraftsRoom creates the room from DraftsRoomRequest.
	CreateDraftsRoom(ctx context.Context, name, roomType string) (id.RoomID, error)
}

// DraftsRoomID returns the drafts room ID from the account data, or "" when
// the account data names no valid room.
func DraftsRoomID(ctx context.Context, s DraftsRoomStore) (id.RoomID, error) {
	raw, err := s.AccountData(ctx, drafts.AccountDataType)
	if err != nil || raw == nil {
		return "", err
	}
	var ad struct {
		RoomID string `json:"room_id"`
	}
	if json.Unmarshal(raw, &ad) != nil || !ValidRoomID(ad.RoomID) {
		return "", nil
	}
	return id.RoomID(ad.RoomID), nil
}

// DraftsRoom returns the drafts room, and creates it when the account data
// names none or names a room the user is not in. joined is the list of
// joined rooms. An error reading the account data stops the write: a device
// never makes a second room. The caller serializes calls.
func DraftsRoom(ctx context.Context, s DraftsRoomStore, joined []id.RoomID) (id.RoomID, error) {
	roomID, err := DraftsRoomID(ctx, s)
	if err != nil {
		return "", err
	}
	if roomID != "" && slices.Contains(joined, roomID) {
		return roomID, nil
	}
	if roomID, err = s.CreateDraftsRoom(ctx, DraftsRoomName, drafts.RoomType); err != nil {
		return "", err
	}
	if err = s.SetAccountData(ctx, drafts.AccountDataType, map[string]any{"room_id": roomID}); err != nil {
		return "", err
	}
	return roomID, nil
}

// ValidRoomID tells if s is a room ID in the form the drafts rule takes.
func ValidRoomID(s string) bool { return strings.HasPrefix(s, "!") && len(s) > 1 }
