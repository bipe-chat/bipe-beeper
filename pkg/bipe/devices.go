package bipe

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Device entries (bipe ADR 0291, bipe-chat/bipe#1479). device-kinds.json is a
// copy of the bipe-shared registry registries/device-kinds.json. The
// protocol is bipe-shared docs/drafts-room.md, "Device entries".

//go:embed device-kinds.json
var deviceKindsJSON []byte

// DeviceKinds is registries/device-kinds.json, the part that a writer needs.
type DeviceKinds struct {
	Version        int    `json:"version"`
	Field          string `json:"field"`
	Msgtype        string `json:"msgtype"`
	FieldMaxLength int    `json:"fieldMaxLength"`
	Kinds          []struct {
		Key string `json:"key"`
	} `json:"kinds"`
}

var deviceKinds DeviceKinds

func init() {
	if err := json.Unmarshal(deviceKindsJSON, &deviceKinds); err != nil {
		panic(fmt.Errorf("bipe: invalid device-kinds.json: %w", err))
	}
}

// DeviceKindsRegistry returns the device kinds registry.
func DeviceKindsRegistry() DeviceKinds { return deviceKinds }

// Device kinds that bipe-beeper writes.
const (
	DeviceKindBridge    = "bridge"
	DeviceKindAssistant = "assistant"
)

// DeviceInfo is the field chat.bipe.device_info of a device entry with
// source "self". Empty text fields and a zero LinkedAt are left out.
type DeviceInfo struct {
	DeviceID id.DeviceID
	Kind     string
	Model    string
	OS       string
	App      string
	// LinkedAt is the link time in milliseconds.
	LinkedAt int64
	Attested bool
}

// Fields returns the field content. Text fields are trimmed and cut to
// fieldMaxLength UTF-16 code units, so the reference rule
// deviceInfoFromEvent accepts the entry.
func (d DeviceInfo) Fields() map[string]any {
	f := map[string]any{
		"device_id": string(d.DeviceID),
		"source":    "self",
		"kind":      d.Kind,
		"attested":  d.Attested,
	}
	for k, v := range map[string]string{"model": d.Model, "os": d.OS, "app": d.App} {
		if v = CutUTF16(v, deviceKinds.FieldMaxLength); v != "" {
			f[k] = v
		}
	}
	if d.LinkedAt > 0 {
		f["linked_at"] = d.LinkedAt
	}
	return f
}

// Content returns the m.room.message content of the entry. Other Matrix
// apps show the body as a notice.
func (d DeviceInfo) Content() map[string]any {
	f := d.Fields()
	var parts []string
	for _, k := range []string{"app", "model", "os"} {
		if v, ok := f[k].(string); ok {
			parts = append(parts, v)
		}
	}
	body := strings.Join(parts, " · ")
	if body == "" {
		body = d.Kind
	}
	return map[string]any{
		"msgtype":         deviceKinds.Msgtype,
		"body":            body,
		deviceKinds.Field: f,
	}
}

// Fingerprint identifies the fields of the entry except linked_at. A writer
// keeps it and writes again on start only when it changed.
func (d DeviceInfo) Fingerprint() string {
	f := d.Fields()
	delete(f, "linked_at")
	raw, _ := json.Marshal(f) // map keys marshal in sorted order
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CutUTF16 trims s and cuts it to at most limit UTF-16 code units.
func CutUTF16(s string, limit int) string {
	s = strings.TrimSpace(s)
	n := 0
	for i, r := range s {
		if n+utf16.RuneLen(r) > limit {
			return strings.TrimSpace(s[:i])
		}
		n += utf16.RuneLen(r)
	}
	return s
}

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
