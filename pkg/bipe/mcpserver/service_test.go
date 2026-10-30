package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

const own = id.UserID("@ana:bipe.net.br")

type sentMessage struct {
	room    id.RoomID
	content map[string]any
}

type fakeMatrix struct {
	joined      []id.RoomID
	chats       []Chat
	messages    map[id.RoomID][]Message
	accountData map[string]json.RawMessage
	accountErr  error
	state       map[string]json.RawMessage
	stateErr    error
	created     []id.RoomID
	sent        []sentMessage
	sendErr     error
	setStateErr error
}

func newFake() *fakeMatrix {
	return &fakeMatrix{
		joined: []id.RoomID{"!chat:bipe.net.br", "!group:bipe.net.br"},
		messages: map[id.RoomID][]Message{
			"!chat:bipe.net.br": {
				{EventID: "$1", Sender: own, Body: "oi"},
				{EventID: "$2", Sender: "@bia:bipe.net.br", Body: "segredo da Bia"},
				{EventID: "$3", Sender: own, Body: "tudo bem?"},
			},
		},
		accountData: map[string]json.RawMessage{},
		state:       map[string]json.RawMessage{},
	}
}

func (f *fakeMatrix) UserID() id.UserID { return own }
func (f *fakeMatrix) JoinedRooms(context.Context) ([]id.RoomID, error) {
	return f.joined, nil
}
func (f *fakeMatrix) Chats(context.Context) ([]Chat, error) { return f.chats, nil }

// Messages ignores the sender filter on purpose: the service must filter
// again.
func (f *fakeMatrix) Messages(_ context.Context, roomID id.RoomID, _ id.UserID, _ int) ([]Message, error) {
	return f.messages[roomID], nil
}
func (f *fakeMatrix) AccountData(_ context.Context, t string) (json.RawMessage, error) {
	if f.accountErr != nil {
		return nil, f.accountErr
	}
	return f.accountData[t], nil
}
func (f *fakeMatrix) SetAccountData(_ context.Context, t string, content any) error {
	data, _ := json.Marshal(content)
	f.accountData[t] = data
	return nil
}
func (f *fakeMatrix) RoomState(_ context.Context, roomID id.RoomID, t, key string) (json.RawMessage, error) {
	if f.stateErr != nil {
		return nil, f.stateErr
	}
	return f.state[string(roomID)+"|"+t+"|"+key], nil
}
func (f *fakeMatrix) SetRoomState(_ context.Context, roomID id.RoomID, t, key string, content any) error {
	if f.setStateErr != nil {
		return f.setStateErr
	}
	data, _ := json.Marshal(content)
	f.state[string(roomID)+"|"+t+"|"+key] = data
	return nil
}
func (f *fakeMatrix) CreateDraftsRoom(_ context.Context, name, roomType string) (id.RoomID, error) {
	if name != "Rascunhos" || roomType != "chat.bipe.drafts" {
		return "", errors.New("bad drafts room")
	}
	r := id.RoomID("!drafts" + string(rune('0'+len(f.created))) + ":bipe.net.br")
	f.created = append(f.created, r)
	f.joined = append(f.joined, r)
	return r, nil
}
func (f *fakeMatrix) SendMessage(_ context.Context, roomID id.RoomID, content map[string]any) (id.EventID, error) {
	if f.sendErr != nil {
		return "", f.sendErr
	}
	f.sent = append(f.sent, sentMessage{roomID, content})
	return "$sent", nil
}

func allScopes(t *testing.T) Scopes {
	s, err := ParseScopes("chats:read,content-own,content-full,content-send")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(t *testing.T, svc *Service, name, args string) (map[string]any, string) {
	t.Helper()
	res, err := svc.Call(context.Background(), name, "Claude Desktop", json.RawMessage(args))
	if err != nil {
		msg, ok := UserMessage(err)
		if !ok {
			t.Fatalf("%s: internal error %v", name, err)
		}
		return nil, msg
	}
	data, _ := json.Marshal(res)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out, ""
}

func TestParseScopes(t *testing.T) {
	s, err := ParseScopes("")
	if err != nil || s.String() != "chats:read,content-own" {
		t.Fatalf("default: %v %q", err, s.String())
	}
	if _, err = ParseScopes("chats:read,content-everything"); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if _, err = ParseScopes(" , "); err == nil {
		t.Fatal("empty list accepted")
	}
	s, _ = ParseScopes("chats:read")
	if s.GrantsContent() || ContentNotice(s) != "" {
		t.Fatal("chats:read is metadata only")
	}
	s, _ = ParseScopes("chats:read,content-own")
	if !s.GrantsContent() || !strings.Contains(ContentNotice(s), "fora do Brasil") {
		t.Fatal("content-own must print the notice")
	}
}

func TestScopeFiltering(t *testing.T) {
	cases := map[string][]string{
		"chats:read":                          {"list_chats", "set_full_content"},
		"chats:read,content-own":              {"list_chats", "read_my_messages", "draft_message", "set_full_content"},
		"chats:read,content-own,content-full": {"list_chats", "read_my_messages", "read_chat", "draft_message", "set_full_content"},
		"content-send":                        {"send_message"},
		"chats:read,content-own,content-send": {"list_chats", "read_my_messages", "draft_message", "send_message", "set_full_content"},
	}
	for list, want := range cases {
		s, err := ParseScopes(list)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tool := range s.Tools() {
			got = append(got, tool.Name)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v, want %v", list, got, want)
		}
	}

	// A tool outside the scopes refuses and does nothing.
	f := newFake()
	f.accountData["chat.bipe.assistant_send"] = json.RawMessage(`{"enabled":true}`)
	s, _ := ParseScopes("chats:read,content-own")
	svc := NewService(f, s)
	for _, name := range []string{"read_chat", "send_message"} {
		_, msg := call(t, svc, name, `{"room_id":"!chat:bipe.net.br","body":"oi"}`)
		if !strings.Contains(msg, "permissão") {
			t.Errorf("%s: got %q", name, msg)
		}
	}
	if len(f.sent) != 0 {
		t.Fatal("a refused tool sent a message")
	}
}

// The MCP server lists only granted tools, and a call to another tool fails.
func TestServerListsGrantedTools(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s, _ := ParseScopes("")
	server := NewServer(NewService(f, s), "test", zerolog.Nop())
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "  Claude Desktop ", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "draft_message,list_chats,read_my_messages,set_full_content" &&
		strings.Join(names, ",") != "list_chats,read_my_messages,draft_message,set_full_content" {
		t.Fatalf("tools: %v", names)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_chat", Arguments: map[string]any{"room_id": "!chat:bipe.net.br"}})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatal("read_chat without content-full did not fail")
	}

	// The client name of the draft comes from initialize, trimmed.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "draft_message", Arguments: map[string]any{"room_id": "!chat:bipe.net.br", "body": "oi"}})
	if err != nil || res.IsError {
		t.Fatalf("draft_message: %v %+v", err, res)
	}
	field := f.sent[0].content["chat.bipe.draft"].(map[string]any)
	if field["client"] != "Claude Desktop" {
		t.Fatalf("client %q", field["client"])
	}
}

func TestReadMyMessagesOnlyOwn(t *testing.T) {
	svc := NewService(newFake(), allScopes(t))
	out, msg := call(t, svc, "read_my_messages", `{"room_id":"!chat:bipe.net.br"}`)
	if msg != "" {
		t.Fatal(msg)
	}
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "segredo") || strings.Contains(string(data), "@bia") {
		t.Fatalf("another member's message leaked: %s", data)
	}
	if n := len(out["messages"].([]any)); n != 2 {
		t.Fatalf("got %d messages, want 2", n)
	}
	if _, msg = call(t, svc, "read_my_messages", `{"room_id":"!other:bipe.net.br"}`); msg != bipe.MCPNotJoined {
		t.Fatalf("not joined: %q", msg)
	}
}

func TestReadChatNeedsFullContent(t *testing.T) {
	f := newFake()
	svc := NewService(f, allScopes(t))
	key := "!chat:bipe.net.br|chat.bipe.assistant|" + string(own)

	// No state event.
	if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != bipe.MCPFullContentOff {
		t.Fatalf("no state: %q", msg)
	}
	// A state event that is not exactly full_content true, or another
	// member's event.
	for _, content := range []string{`{}`, `{"full_content":false}`, `{"full_content":"true"}`} {
		f.state[key] = json.RawMessage(content)
		if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != bipe.MCPFullContentOff {
			t.Fatalf("%s: %q", content, msg)
		}
	}
	delete(f.state, key)
	f.state["!chat:bipe.net.br|chat.bipe.assistant|@bia:bipe.net.br"] = json.RawMessage(`{"full_content":true}`)
	if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != bipe.MCPFullContentOff {
		t.Fatalf("other member's event: %q", msg)
	}
	// A read error is off.
	f.state[key] = json.RawMessage(`{"full_content":true}`)
	f.stateErr = errors.New("network")
	if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != bipe.MCPFullContentOff {
		t.Fatalf("state error: %q", msg)
	}
	f.stateErr = nil
	out, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`)
	if msg != "" || len(out["messages"].([]any)) != 3 {
		t.Fatalf("full read: %q %v", msg, out)
	}
}

func TestSetFullContent(t *testing.T) {
	f := newFake()
	svc := NewService(f, allScopes(t))
	key := "!chat:bipe.net.br|chat.bipe.assistant|" + string(own)

	// A failed send keeps full content off.
	f.setStateErr = errors.New("M_FORBIDDEN")
	if _, msg := call(t, svc, "set_full_content", `{"room_id":"!chat:bipe.net.br","enabled":true}`); msg != bipe.MCPFullContentOnErr {
		t.Fatalf("failed send: %q", msg)
	}
	if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != bipe.MCPFullContentOff {
		t.Fatalf("read after failed send: %q", msg)
	}
	// A room where members cannot set state (bipe-chat/bipe#879) names the cause, and full content stays off.
	f.setStateErr = mautrix.MForbidden
	if _, msg := call(t, svc, "set_full_content", `{"room_id":"!chat:bipe.net.br","enabled":true}`); msg != bipe.MCPFullContentForbidden {
		t.Fatalf("forbidden: %q", msg)
	}
	if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != bipe.MCPFullContentOff {
		t.Fatalf("read after forbidden: %q", msg)
	}
	f.setStateErr = nil

	if _, msg := call(t, svc, "set_full_content", `{"room_id":"!chat:bipe.net.br"}`); msg == "" {
		t.Fatal("missing enabled accepted")
	}
	if _, msg := call(t, svc, "set_full_content", `{"room_id":"!chat:bipe.net.br","enabled":true}`); msg != "" {
		t.Fatal(msg)
	}
	if string(f.state[key]) != `{"full_content":true}` {
		t.Fatalf("state %s", f.state[key])
	}
	if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != "" {
		t.Fatal(msg)
	}
	if _, msg := call(t, svc, "set_full_content", `{"room_id":"!chat:bipe.net.br","enabled":false}`); msg != "" {
		t.Fatal(msg)
	}
	if string(f.state[key]) != `{}` {
		t.Fatalf("state %s", f.state[key])
	}
	if _, msg := call(t, svc, "read_chat", `{"room_id":"!chat:bipe.net.br"}`); msg != bipe.MCPFullContentOff {
		t.Fatalf("read after off: %q", msg)
	}
}

func TestDraftMessage(t *testing.T) {
	f := newFake()
	svc := NewService(f, allScopes(t))
	out, msg := call(t, svc, "draft_message", `{"room_id":"!chat:bipe.net.br","body":"Posso passar aí às 15h?","reply_to":"$r"}`)
	if msg != "" || out["sent"] != false {
		t.Fatalf("%q %v", msg, out)
	}
	if len(f.created) != 1 || len(f.sent) != 1 || f.sent[0].room != f.created[0] {
		t.Fatalf("created %v sent %v", f.created, f.sent)
	}
	if string(f.accountData["chat.bipe.drafts_room"]) != `{"room_id":"`+string(f.created[0])+`"}` {
		t.Fatalf("account data %s", f.accountData["chat.bipe.drafts_room"])
	}
	if _, has := f.sent[0].content["m.relates_to"]; has {
		t.Fatal("a draft must not carry a relation")
	}
	// The second draft uses the same room.
	if _, msg = call(t, svc, "draft_message", `{"room_id":"!group:bipe.net.br","body":"oi"}`); msg != "" {
		t.Fatal(msg)
	}
	if len(f.created) != 1 || f.sent[1].room != f.created[0] {
		t.Fatal("a second drafts room was made")
	}
	// A draft cannot target the drafts room.
	if _, msg = call(t, svc, "draft_message", `{"room_id":"`+string(f.created[0])+`","body":"oi"}`); msg != bipe.MCPDraftsRoomTarget {
		t.Fatalf("drafts room target: %q", msg)
	}
	// A drafts room the user left is made again.
	f.joined = f.joined[:2]
	if _, msg = call(t, svc, "draft_message", `{"room_id":"!chat:bipe.net.br","body":"oi"}`); msg != "" || len(f.created) != 2 {
		t.Fatalf("left room: %q %v", msg, f.created)
	}
	// An account data read error never makes a second room.
	f.accountErr = errors.New("network")
	if _, msg = call(t, svc, "draft_message", `{"room_id":"!chat:bipe.net.br","body":"oi"}`); msg != bipe.MCPDraftFailed || len(f.created) != 2 {
		t.Fatalf("account data error: %q %v", msg, f.created)
	}
}

func TestDraftInputValidation(t *testing.T) {
	svc := NewService(newFake(), allScopes(t))
	long := strings.Repeat("a", 8001)
	for args, want := range map[string]string{
		`{"room_id":"chat:bipe.net.br","body":"oi"}`:                    bipe.MCPBadRoomID,
		`{"room_id":"!other:bipe.net.br","body":"oi"}`:                  bipe.MCPNotJoined,
		`{"room_id":"!chat:bipe.net.br","body":"   "}`:                  bipe.MCPBodyBlank,
		`{"room_id":"!chat:bipe.net.br","body":"` + long + `"}`:         "A mensagem passa de 8000 caracteres.",
		`{"room_id":"!chat:bipe.net.br","body":"oi","reply_to":"r"}`:    bipe.MCPBadEventID,
		`{"room_id":"!chat:bipe.net.br","body":"oi","thread_root":"x"}`: bipe.MCPBadEventID,
		`{"room_id":1}`: "",
	} {
		_, msg := call(t, svc, "draft_message", args)
		if want == "" && msg == "" || want != "" && msg != want {
			t.Errorf("%.60s: got %q, want %q", args, msg, want)
		}
	}
	// 8000 characters is fine.
	if _, msg := call(t, svc, "draft_message", `{"room_id":"!chat:bipe.net.br","body":"`+strings.Repeat("a", 8000)+`"}`); msg != "" {
		t.Fatal(msg)
	}
}

type draftVectors struct {
	Context struct {
		OwnUserID     id.UserID   `json:"ownUserId"`
		JoinedRoomIDs []id.RoomID `json:"joinedRoomIds"`
	} `json:"context"`
	Cases []struct {
		Name  string `json:"name"`
		Event struct {
			Type     string         `json:"type"`
			Sender   id.UserID      `json:"sender"`
			Redacted bool           `json:"redacted"`
			Content  map[string]any `json:"content"`
		} `json:"event"`
		Expected struct {
			Show   bool   `json:"show"`
			Reason string `json:"reason"`
			Draft  *struct {
				RoomID     string  `json:"roomId"`
				Body       string  `json:"body"`
				ReplyTo    *string `json:"replyTo"`
				ThreadRoot *string `json:"threadRoot"`
				Client     string  `json:"client"`
			} `json:"draft"`
		} `json:"expected"`
	} `json:"cases"`
}

// The writer must accept exactly the drafts that the apps show
// (draftFromEvent, test-vectors/drafts.json). Cases about the event envelope
// (type, sender, redaction, msgtype, missing field) are reader-side only.
func TestDraftContentMatchesReaderVectors(t *testing.T) {
	data, err := os.ReadFile("../testdata/drafts-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v draftVectors
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, c := range v.Cases {
		switch c.Expected.Reason {
		case "type", "redacted", "sender", "content", "field":
			continue
		}
		field := c.Event.Content["chat.bipe.draft"].(map[string]any)
		str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
		in := MessageInput{
			RoomID:     str(field, "room_id"),
			Body:       str(c.Event.Content, "body"),
			ReplyTo:    str(field, "reply_to"),
			ThreadRoot: str(field, "thread_root"),
		}
		rawClient := str(field, "client")
		// The writer never writes a blank or long client: ClientName fixes
		// it. The vector cases for bad clients test the reader.
		if c.Expected.Reason == "client" {
			name := bipe.ClientName(rawClient)
			if strings.TrimSpace(name) == "" || bipe.UTF16Len(name) > 64 {
				t.Errorf("%s: ClientName gave %q", c.Name, name)
			}
			checked++
			continue
		}
		err := in.Validate(v.Context.JoinedRoomIDs)
		if (err == nil) != c.Expected.Show {
			t.Errorf("%s: writer accepts=%v, reader shows=%v (%v)", c.Name, err == nil, c.Expected.Show, err)
			continue
		}
		checked++
		if err != nil {
			continue
		}
		content := DraftContent(in, bipe.ClientName(rawClient))
		got := content["chat.bipe.draft"].(map[string]any)
		want := c.Expected.Draft
		if content["msgtype"] != "m.text" || content["body"] != want.Body || got["room_id"] != want.RoomID || got["client"] != want.Client {
			t.Errorf("%s: content %v, want %+v", c.Name, content, want)
		}
		if (want.ReplyTo == nil) != (got["reply_to"] == nil) || want.ReplyTo != nil && got["reply_to"] != *want.ReplyTo {
			t.Errorf("%s: reply_to %v", c.Name, got["reply_to"])
		}
		if (want.ThreadRoot == nil) != (got["thread_root"] == nil) || want.ThreadRoot != nil && got["thread_root"] != *want.ThreadRoot {
			t.Errorf("%s: thread_root %v", c.Name, got["thread_root"])
		}
	}
	if checked < 8 {
		t.Fatalf("only %d vector cases checked", checked)
	}
}

func TestSendMessageSwitch(t *testing.T) {
	data, err := os.ReadFile("../testdata/assistant-send.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Switch []struct {
			Name     string          `json:"name"`
			Content  json.RawMessage `json:"content"`
			Expected bool            `json:"expected"`
		} `json:"switch"`
	}
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Switch {
		f := newFake()
		if string(c.Content) != "null" {
			f.accountData["chat.bipe.assistant_send"] = c.Content
		}
		svc := NewService(f, allScopes(t))
		_, msg := call(t, svc, "send_message", `{"room_id":"!chat:bipe.net.br","body":"oi"}`)
		if sent := len(f.sent) == 1; sent != c.Expected {
			t.Errorf("%s: sent=%v, want %v (%q)", c.Name, sent, c.Expected, msg)
		}
		if !c.Expected && msg != bipe.MCPSendSwitchOff {
			t.Errorf("%s: message %q", c.Name, msg)
		}
		if c.Expected && bipe.AssistantSender(f.sent[0].content) != "Claude Desktop" {
			t.Errorf("%s: no marker in %v", c.Name, f.sent[0].content)
		}
	}

	// The switch is read before every send: off on the phone stops the
	// next send.
	f := newFake()
	f.accountData["chat.bipe.assistant_send"] = json.RawMessage(`{"enabled":true}`)
	svc := NewService(f, allScopes(t))
	if _, msg := call(t, svc, "send_message", `{"room_id":"!chat:bipe.net.br","body":"oi"}`); msg != "" {
		t.Fatal(msg)
	}
	f.accountData["chat.bipe.assistant_send"] = json.RawMessage(`{"enabled":false}`)
	if _, msg := call(t, svc, "send_message", `{"room_id":"!chat:bipe.net.br","body":"oi"}`); msg != bipe.MCPSendSwitchOff || len(f.sent) != 1 {
		t.Fatalf("switch off: %q, %d sent", msg, len(f.sent))
	}
	// A read error is off.
	f.accountData["chat.bipe.assistant_send"] = json.RawMessage(`{"enabled":true}`)
	f.accountErr = errors.New("network")
	if _, msg := call(t, svc, "send_message", `{"room_id":"!chat:bipe.net.br","body":"oi"}`); len(f.sent) != 1 || msg == "" {
		t.Fatalf("read error: %q, %d sent", msg, len(f.sent))
	}
}

func TestSendContent(t *testing.T) {
	in := MessageInput{RoomID: "!chat:bipe.net.br", Body: "oi"}
	c := SendContent(in, "Cursor")
	if bipe.AssistantSender(c) != "Cursor" || c["msgtype"] != "m.text" || c["body"] != "oi" || c["m.relates_to"] != nil {
		t.Fatalf("plain: %v", c)
	}
	in.ReplyTo = "$r"
	rel := SendContent(in, "Cursor")["m.relates_to"].(map[string]any)
	if rel["m.in_reply_to"].(map[string]any)["event_id"] != "$r" || rel["rel_type"] != nil {
		t.Fatalf("reply: %v", rel)
	}
	in.ThreadRoot = "$root"
	rel = SendContent(in, "Cursor")["m.relates_to"].(map[string]any)
	if rel["rel_type"] != "m.thread" || rel["event_id"] != "$root" || rel["is_falling_back"] != false || rel["m.in_reply_to"].(map[string]any)["event_id"] != "$r" {
		t.Fatalf("thread reply: %v", rel)
	}
	in.ReplyTo = ""
	rel = SendContent(in, "Cursor")["m.relates_to"].(map[string]any)
	if rel["is_falling_back"] != true {
		t.Fatalf("thread: %v", rel)
	}
}

func TestListChatsHidesDraftsRoom(t *testing.T) {
	f := newFake()
	f.chats = []Chat{{RoomID: "!chat:bipe.net.br", Name: "Bia"}, {RoomID: "!drafts:bipe.net.br", Name: "Rascunhos"}}
	f.accountData["chat.bipe.drafts_room"] = json.RawMessage(`{"room_id":"!drafts:bipe.net.br"}`)
	s, _ := ParseScopes("chats:read")
	out, msg := call(t, NewService(f, s), "list_chats", `{}`)
	if msg != "" {
		t.Fatal(msg)
	}
	chats := out["chats"].([]any)
	if len(chats) != 1 || chats[0].(map[string]any)["room_id"] != "!chat:bipe.net.br" {
		t.Fatalf("chats %v", chats)
	}
}
