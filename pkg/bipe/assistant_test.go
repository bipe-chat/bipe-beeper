package bipe

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedRoot is the bipe-shared checkout next to this repo.
const sharedRoot = "../../../bipe-shared"

// The embedded registries and the test vectors must equal bipe-shared when
// bipe-shared is on disk. Copy the files again after a change there.
func TestCopiesMatchBipeShared(t *testing.T) {
	for local, shared := range map[string]string{
		"mcp-tools.json":               "registries/mcp-tools.json",
		"drafts.json":                  "registries/drafts.json",
		"testdata/assistant-send.json": "test-vectors/assistant-send.json",
		"testdata/drafts-vectors.json": "test-vectors/drafts.json",
	} {
		want, err := os.ReadFile(filepath.Join(sharedRoot, shared))
		if os.IsNotExist(err) {
			t.Logf("skip %s: bipe-shared is not on disk", shared)
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(local)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("pkg/bipe/%s differs from bipe-shared %s: copy it again", local, shared)
		}
	}
}

func TestPersonalTools(t *testing.T) {
	want := map[string]string{
		"list_chats":       "chats:read",
		"read_my_messages": "content-own",
		"read_chat":        "content-full",
		"draft_message":    "content-own",
		"send_message":     "content-send",
		"set_full_content": "chats:read",
	}
	tools := PersonalTools()
	if len(tools) != len(want) {
		t.Fatalf("got %d personal tools, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		if want[tool.Name] != tool.Scope {
			t.Errorf("%s: scope %q, want %q", tool.Name, tool.Scope, want[tool.Name])
		}
		if tool.EN == "" || tool.PT == "" {
			t.Errorf("%s: no description", tool.Name)
		}
	}
	ps := MCPTools().PersonalSend
	if ps.AccountDataType != "chat.bipe.assistant_send" || ps.Default || ps.Scope != "content-send" || ps.MarkerField != "chat.bipe.assistant" {
		t.Fatalf("unexpected personalSend %+v", ps)
	}
	d := DraftsRegistry()
	if d.AccountDataType != "chat.bipe.drafts_room" || d.Field != "chat.bipe.draft" || d.RoomType != "chat.bipe.drafts" || d.BodyMaxLength != 8000 {
		t.Fatalf("unexpected drafts %+v", d)
	}
}

type assistantVectors struct {
	Switch []struct {
		Name     string          `json:"name"`
		Content  json.RawMessage `json:"content"`
		Expected bool            `json:"expected"`
	} `json:"switch"`
	Label []struct {
		Name     string         `json:"name"`
		Content  map[string]any `json:"content"`
		Expected *string        `json:"expected"`
	} `json:"label"`
}

func loadAssistantVectors(t *testing.T) assistantVectors {
	data, err := os.ReadFile("testdata/assistant-send.json")
	if err != nil {
		t.Fatal(err)
	}
	var v assistantVectors
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Switch) == 0 || len(v.Label) == 0 {
		t.Fatal("no vectors")
	}
	return v
}

func TestAssistantSendAllowedVectors(t *testing.T) {
	for _, c := range loadAssistantVectors(t).Switch {
		content := c.Content
		if string(content) == "null" {
			content = nil
		}
		if got := AssistantSendAllowed(content); got != c.Expected {
			t.Errorf("%s: got %v, want %v", c.Name, got, c.Expected)
		}
	}
	// Extra cases outside the vectors: arrays and broken JSON are off.
	for _, s := range []string{`[]`, `[{"enabled":true}]`, `{"enabled":true`, `"enabled"`, `null`, `{"enabled":null}`} {
		if AssistantSendAllowed(json.RawMessage(s)) {
			t.Errorf("%s: allowed", s)
		}
	}
}

func TestAssistantSenderVectors(t *testing.T) {
	for _, c := range loadAssistantVectors(t).Label {
		want := ""
		if c.Expected != nil {
			want = *c.Expected
		}
		if got := AssistantSender(c.Content); got != want {
			t.Errorf("%s: got %q, want %q", c.Name, got, want)
		}
	}
}

func TestFullContentOn(t *testing.T) {
	for s, want := range map[string]bool{
		`{"full_content":true}`:   true,
		`{"full_content": true }`: true,
		`{"full_content":false}`:  false,
		`{"full_content":"true"}`: false,
		`{"full_content":1}`:      false,
		`{}`:                      false,
		`[]`:                      false,
		``:                        false,
		`null`:                    false,
	} {
		if got := FullContentOn(json.RawMessage(s)); got != want {
			t.Errorf("%q: got %v, want %v", s, got, want)
		}
	}
}

func TestClientName(t *testing.T) {
	for in, want := range map[string]string{
		"Claude Desktop":        "Claude Desktop",
		"  Cursor  ":            "Cursor",
		"":                      DefaultClientName,
		"   ":                   DefaultClientName,
		strings.Repeat("c", 65): strings.Repeat("c", 64),
		strings.Repeat("😀", 40): strings.Repeat("😀", 32),
	} {
		if got := ClientName(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
		if UTF16Len(ClientName(in)) > 64 {
			t.Errorf("%q: too long", in)
		}
	}
}
