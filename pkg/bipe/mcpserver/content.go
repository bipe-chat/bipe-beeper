package mcpserver

import (
	"strings"

	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

// MessageInput is what draft_message and send_message get from the client.
type MessageInput struct {
	RoomID     string `json:"room_id"`
	Body       string `json:"body"`
	ReplyTo    string `json:"reply_to,omitempty"`
	ThreadRoot string `json:"thread_root,omitempty"`
}

// inputError is a tool error with a user-facing pt-BR message.
type inputError struct{ msg string }

func (e *inputError) Error() string { return e.msg }

func userError(msg string) error { return &inputError{msg: msg} }

// validRoomID tells if s is a room ID in the form the drafts rule takes.
func validRoomID(s string) bool { return strings.HasPrefix(s, "!") && len(s) > 1 }

// validEventID tells if s is an event ID in the form the drafts rule takes.
func validEventID(s string) bool { return strings.HasPrefix(s, "$") && len(s) > 1 }

// Validate checks the input against the rules that the apps apply to a draft
// (draftFromEvent in bipe-shared tools/lib/registries.mjs). joined is the
// list of rooms the user is in; nil skips that check.
func (in MessageInput) Validate(joined []id.RoomID) error {
	d := bipe.DraftsRegistry()
	switch {
	case !validRoomID(in.RoomID):
		return userError(bipe.MCPBadRoomID)
	case in.ReplyTo != "" && !validEventID(in.ReplyTo), in.ThreadRoot != "" && !validEventID(in.ThreadRoot):
		return userError(bipe.MCPBadEventID)
	case strings.TrimSpace(in.Body) == "":
		return userError(bipe.MCPBodyBlank)
	case bipe.UTF16Len(in.Body) > d.BodyMaxLength:
		return userError(sprintf(bipe.MCPBodyTooLong, d.BodyMaxLength))
	}
	if joined != nil && !containsRoom(joined, id.RoomID(in.RoomID)) {
		return userError(bipe.MCPNotJoined)
	}
	return nil
}

func containsRoom(list []id.RoomID, r id.RoomID) bool {
	for _, x := range list {
		if x == r {
			return true
		}
	}
	return false
}

// DraftContent is the m.room.message of a draft in the drafts room
// (bipe-shared docs/drafts-room.md). It carries no reply or thread relation:
// the app adds them when the user sends the draft. client is a name from
// bipe.ClientName.
func DraftContent(in MessageInput, client string) map[string]any {
	field := map[string]any{
		"room_id": in.RoomID,
		"client":  client,
	}
	if in.ReplyTo != "" {
		field["reply_to"] = in.ReplyTo
	}
	if in.ThreadRoot != "" {
		field["thread_root"] = in.ThreadRoot
	}
	return map[string]any{
		"msgtype":                   "m.text",
		"body":                      in.Body,
		bipe.DraftsRegistry().Field: field,
	}
}

// SendContent is the m.room.message that send_message sends: the text, the
// reply and thread relation, and the assistant marker
// {"chat.bipe.assistant": {"client": ...}} that the apps label.
func SendContent(in MessageInput, client string) map[string]any {
	c := map[string]any{
		"msgtype":                                "m.text",
		"body":                                   in.Body,
		bipe.MCPTools().PersonalSend.MarkerField: map[string]any{"client": client},
	}
	var rel map[string]any
	switch {
	case in.ThreadRoot != "":
		rel = map[string]any{"rel_type": "m.thread", "event_id": in.ThreadRoot}
		if in.ReplyTo != "" {
			rel["is_falling_back"] = false
			rel["m.in_reply_to"] = map[string]any{"event_id": in.ReplyTo}
		} else {
			rel["is_falling_back"] = true
			rel["m.in_reply_to"] = map[string]any{"event_id": in.ThreadRoot}
		}
	case in.ReplyTo != "":
		rel = map[string]any{"m.in_reply_to": map[string]any{"event_id": in.ReplyTo}}
	}
	if rel != nil {
		c["m.relates_to"] = rel
	}
	return c
}

// FullContentState is the content of the chat.bipe.assistant state event.
// Off is an empty object, which Matrix clients read as a removed event.
func FullContentState(on bool) map[string]any {
	if on {
		return map[string]any{"full_content": true}
	}
	return map[string]any{}
}
