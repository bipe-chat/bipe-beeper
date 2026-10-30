package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

// Matrix is what the tools need from the user's Matrix session. The bipe-mcp
// command implements it with mautrix-go and the crypto helper, so every
// message is decrypted and encrypted on the user's machine.
type Matrix interface {
	// UserID is the user's own Matrix ID.
	UserID() id.UserID
	// JoinedRooms lists the rooms the user is in.
	JoinedRooms(ctx context.Context) ([]id.RoomID, error)
	// Chats lists the joined rooms with metadata. No message text.
	Chats(ctx context.Context) ([]Chat, error)
	// Messages returns up to limit messages of a room, oldest first. When
	// sender is set, it returns only the messages of that sender.
	Messages(ctx context.Context, roomID id.RoomID, sender id.UserID, limit int) ([]Message, error)
	// AccountData returns global account data, or nil when the account has
	// none of that type.
	AccountData(ctx context.Context, eventType string) (json.RawMessage, error)
	// SetAccountData writes global account data.
	SetAccountData(ctx context.Context, eventType string, content any) error
	// RoomState returns the content of a state event, or nil when the room
	// has none.
	RoomState(ctx context.Context, roomID id.RoomID, eventType, stateKey string) (json.RawMessage, error)
	// SetRoomState sends a state event.
	SetRoomState(ctx context.Context, roomID id.RoomID, eventType, stateKey string, content any) error
	// CreateDraftsRoom creates the encrypted, invite-only drafts room with
	// no invites.
	CreateDraftsRoom(ctx context.Context, name, roomType string) (id.RoomID, error)
	// SendMessage sends an m.room.message, encrypted when the room is.
	SendMessage(ctx context.Context, roomID id.RoomID, content map[string]any) (id.EventID, error)
}

// Chat is the metadata of one chat. It holds no message text.
type Chat struct {
	RoomID       id.RoomID `json:"room_id"`
	Name         string    `json:"name"`
	Direct       bool      `json:"direct"`
	Unread       int       `json:"unread"`
	Highlights   int       `json:"highlights"`
	LastActivity string    `json:"last_activity,omitempty"`
}

// Message is one message of a chat.
type Message struct {
	EventID       id.EventID `json:"event_id"`
	Sender        id.UserID  `json:"sender"`
	Time          string     `json:"time"`
	MsgType       string     `json:"msgtype,omitempty"`
	Body          string     `json:"body,omitempty"`
	ReplyTo       id.EventID `json:"reply_to,omitempty"`
	ThreadRoot    id.EventID `json:"thread_root,omitempty"`
	Replaces      id.EventID `json:"replaces,omitempty"`
	Undecryptable bool       `json:"undecryptable,omitempty"`
}

const (
	defaultLimit = 50
	maxLimit     = 200
)

// Service runs the personal tools.
type Service struct {
	mx     Matrix
	scopes Scopes

	draftsLock sync.Mutex
}

// NewService makes a service for the granted scopes.
func NewService(mx Matrix, scopes Scopes) *Service {
	return &Service{mx: mx, scopes: scopes}
}

// Scopes returns the granted scopes.
func (s *Service) Scopes() Scopes { return s.scopes }

// Call runs a tool. client is the MCP clientInfo.name. A tool whose scope is
// not granted returns an error and does nothing. An error with a pt-BR
// message for the user is an *inputError; other errors are internal.
func (s *Service) Call(ctx context.Context, name, client string, args json.RawMessage) (any, error) {
	var tool *bipe.MCPTool
	for _, t := range bipe.PersonalTools() {
		if t.Name == name {
			tool = &t
			break
		}
	}
	if tool == nil {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	if !s.scopes[tool.Scope] {
		return nil, userError(fmt.Sprintf(bipe.MCPScopeMissing, tool.Scope, tool.Scope))
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	client = bipe.ClientName(client)
	switch name {
	case "list_chats":
		return s.listChats(ctx)
	case "read_my_messages":
		return s.readMyMessages(ctx, args)
	case "read_chat":
		return s.readChat(ctx, args)
	case "set_full_content":
		return s.setFullContent(ctx, args)
	case "draft_message":
		return s.draftMessage(ctx, args, client)
	case "send_message":
		return s.sendMessage(ctx, args, client)
	}
	return nil, fmt.Errorf("tool %q has no handler", name)
}

// UserMessage returns the pt-BR message of a tool error, and false for an
// unknown tool or another internal error.
func UserMessage(err error) (string, bool) {
	var ie *inputError
	var tf *toolFailure
	switch {
	case errors.As(err, &ie):
		return ie.msg, true
	case errors.As(err, &tf):
		return tf.msg, true
	}
	return "", false
}

func decodeArgs(args json.RawMessage, out any) error {
	if err := json.Unmarshal(args, out); err != nil {
		return userError(fmt.Sprintf(bipe.MCPBadArguments, err.Error()))
	}
	return nil
}

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// wrap keeps a user error and replaces any other error with msg, keeping
// the cause for the log.
func wrap(err error, msg string) error {
	if _, ok := UserMessage(err); ok {
		return err
	}
	return &toolFailure{msg: msg, cause: err}
}

// toolFailure is a failed Matrix call. The user sees msg; the cause stays in
// the log on the user's machine.
type toolFailure struct {
	msg   string
	cause error
}

func (e *toolFailure) Error() string { return e.msg + ": " + e.cause.Error() }
func (e *toolFailure) Unwrap() error { return e.cause }

// draftsRoomID returns the drafts room ID from the account data, or "".
func (s *Service) draftsRoomID(ctx context.Context) (id.RoomID, error) {
	raw, err := s.mx.AccountData(ctx, bipe.DraftsRegistry().AccountDataType)
	if err != nil || raw == nil {
		return "", err
	}
	var ad struct {
		RoomID string `json:"room_id"`
	}
	if json.Unmarshal(raw, &ad) != nil || !validRoomID(ad.RoomID) {
		return "", nil
	}
	return id.RoomID(ad.RoomID), nil
}

func (s *Service) listChats(ctx context.Context) (any, error) {
	chats, err := s.mx.Chats(ctx)
	if err != nil {
		return nil, wrap(err, bipe.MCPListFailed)
	}
	drafts, err := s.draftsRoomID(ctx)
	if err != nil {
		return nil, wrap(err, bipe.MCPListFailed)
	}
	out := make([]Chat, 0, len(chats))
	for _, c := range chats {
		if c.RoomID != drafts {
			out = append(out, c)
		}
	}
	return map[string]any{"chats": out}, nil
}

type readArgs struct {
	RoomID string `json:"room_id"`
	Limit  int    `json:"limit,omitempty"`
}

func (s *Service) readArgs(ctx context.Context, args json.RawMessage) (id.RoomID, int, error) {
	var in readArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", 0, err
	}
	if !validRoomID(in.RoomID) {
		return "", 0, userError(bipe.MCPBadRoomID)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	joined, err := s.mx.JoinedRooms(ctx)
	if err != nil {
		return "", 0, wrap(err, bipe.MCPReadFailed)
	}
	if !containsRoom(joined, id.RoomID(in.RoomID)) {
		return "", 0, userError(bipe.MCPNotJoined)
	}
	return id.RoomID(in.RoomID), limit, nil
}

// readMyMessages returns only the user's own messages. The Matrix layer
// filters by sender, and this filter runs again here: a message of another
// member never leaves.
func (s *Service) readMyMessages(ctx context.Context, args json.RawMessage) (any, error) {
	roomID, limit, err := s.readArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	own := s.mx.UserID()
	msgs, err := s.mx.Messages(ctx, roomID, own, limit)
	if err != nil {
		return nil, wrap(err, bipe.MCPReadFailed)
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Sender == own {
			out = append(out, m)
		}
	}
	return map[string]any{"room_id": roomID, "messages": out}, nil
}

// fullContentOn reads the chat.bipe.assistant state event of the user. Any
// error means off.
func (s *Service) fullContentOn(ctx context.Context, roomID id.RoomID) bool {
	raw, err := s.mx.RoomState(ctx, roomID, bipe.FullContentEventType, string(s.mx.UserID()))
	return err == nil && bipe.FullContentOn(raw)
}

// readChat returns every message of a chat, only while the user's
// chat.bipe.assistant state event says full_content true. Every member's
// app shows the notice while it does.
func (s *Service) readChat(ctx context.Context, args json.RawMessage) (any, error) {
	roomID, limit, err := s.readArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	if !s.fullContentOn(ctx, roomID) {
		return nil, userError(bipe.MCPFullContentOff)
	}
	msgs, err := s.mx.Messages(ctx, roomID, "", limit)
	if err != nil {
		return nil, wrap(err, bipe.MCPReadFailed)
	}
	if msgs == nil {
		msgs = []Message{}
	}
	return map[string]any{"room_id": roomID, "messages": msgs}, nil
}

// setFullContent writes the state event before it returns. If the write
// fails, read_chat stays refused.
func (s *Service) setFullContent(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		RoomID  string `json:"room_id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	if !validRoomID(in.RoomID) {
		return nil, userError(bipe.MCPBadRoomID)
	}
	if in.Enabled == nil {
		return nil, userError(fmt.Sprintf(bipe.MCPBadArguments, "enabled"))
	}
	on := *in.Enabled
	failMsg := bipe.MCPFullContentOnErr
	if !on {
		failMsg = bipe.MCPFullContentOffErr
	}
	joined, err := s.mx.JoinedRooms(ctx)
	if err != nil {
		return nil, wrap(err, failMsg)
	}
	roomID := id.RoomID(in.RoomID)
	if !containsRoom(joined, roomID) {
		return nil, userError(bipe.MCPNotJoined)
	}
	err = s.mx.SetRoomState(ctx, roomID, bipe.FullContentEventType, string(s.mx.UserID()), FullContentState(on))
	if err != nil {
		return nil, wrap(err, failMsg)
	}
	return map[string]any{"room_id": roomID, "full_content": on}, nil
}

// draftsRoom returns the drafts room, and creates it when the account data
// names none or names a room the user is not in. An error reading the
// account data stops the draft: a device never makes a second room.
func (s *Service) draftsRoom(ctx context.Context, joined []id.RoomID) (id.RoomID, error) {
	s.draftsLock.Lock()
	defer s.draftsLock.Unlock()
	d := bipe.DraftsRegistry()
	roomID, err := s.draftsRoomID(ctx)
	if err != nil {
		return "", err
	}
	if roomID != "" && containsRoom(joined, roomID) {
		return roomID, nil
	}
	roomID, err = s.mx.CreateDraftsRoom(ctx, bipe.DraftsRoomName, d.RoomType)
	if err != nil {
		return "", err
	}
	if err = s.mx.SetAccountData(ctx, d.AccountDataType, map[string]any{"room_id": roomID}); err != nil {
		return "", err
	}
	return roomID, nil
}

func (s *Service) messageInput(ctx context.Context, args json.RawMessage, failMsg string) (MessageInput, []id.RoomID, error) {
	var in MessageInput
	if err := decodeArgs(args, &in); err != nil {
		return in, nil, err
	}
	if err := in.Validate(nil); err != nil {
		return in, nil, err
	}
	joined, err := s.mx.JoinedRooms(ctx)
	if err != nil {
		return in, nil, wrap(err, failMsg)
	}
	if err = in.Validate(joined); err != nil {
		return in, nil, err
	}
	drafts, err := s.draftsRoomID(ctx)
	if err != nil {
		return in, nil, wrap(err, failMsg)
	}
	if drafts != "" && id.RoomID(in.RoomID) == drafts {
		return in, nil, userError(bipe.MCPDraftsRoomTarget)
	}
	return in, joined, nil
}

func (s *Service) draftMessage(ctx context.Context, args json.RawMessage, client string) (any, error) {
	in, joined, err := s.messageInput(ctx, args, bipe.MCPDraftFailed)
	if err != nil {
		return nil, err
	}
	roomID, err := s.draftsRoom(ctx, joined)
	if err != nil {
		return nil, wrap(err, bipe.MCPDraftFailed)
	}
	eventID, err := s.mx.SendMessage(ctx, roomID, DraftContent(in, client))
	if err != nil {
		return nil, wrap(err, bipe.MCPDraftFailed)
	}
	return map[string]any{"draft_event_id": eventID, "room_id": in.RoomID, "sent": false}, nil
}

// sendMessage reads the switch before every send. Only {"enabled": true}
// allows it; a read error is off.
func (s *Service) sendMessage(ctx context.Context, args json.RawMessage, client string) (any, error) {
	in, _, err := s.messageInput(ctx, args, bipe.MCPSendFailed)
	if err != nil {
		return nil, err
	}
	sw, err := s.mx.AccountData(ctx, bipe.MCPTools().PersonalSend.AccountDataType)
	if err != nil || !bipe.AssistantSendAllowed(sw) {
		return nil, userError(bipe.MCPSendSwitchOff)
	}
	eventID, err := s.mx.SendMessage(ctx, id.RoomID(in.RoomID), SendContent(in, client))
	if err != nil {
		return nil, wrap(err, bipe.MCPSendFailed)
	}
	return map[string]any{"event_id": eventID, "room_id": in.RoomID, "sent": true}, nil
}
