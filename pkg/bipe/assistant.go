package bipe

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// The personal MCP surface (bipe ADR 0130, ADR 0210; bipe-chat/bipe#439).
// mcp-tools.json and drafts.json are copies of the bipe-shared registries
// (registries/mcp-tools.json, registries/drafts.json). A test fails when a
// copy differs from bipe-shared on disk. Update the copies, do not edit them.

//go:embed mcp-tools.json
var mcpToolsJSON []byte

//go:embed drafts.json
var draftsJSON []byte

// MCPTool is one tool of the bipe-shared MCP tool registry.
type MCPTool struct {
	Name    string `json:"name"`
	Surface string `json:"surface"`
	Class   string `json:"class"`
	Writes  string `json:"writes"`
	Scope   string `json:"scope"`
	EN      string `json:"en"`
	PT      string `json:"pt"`
}

// PersonalSend is the send switch of the personal surface.
type PersonalSend struct {
	AccountDataType string `json:"accountDataType"`
	Default         bool   `json:"default"`
	Scope           string `json:"scope"`
	MarkerField     string `json:"markerField"`
	ClientMaxLength int    `json:"clientMaxLength"`
}

// MCPRegistry is registries/mcp-tools.json.
type MCPRegistry struct {
	Version      int          `json:"version"`
	PersonalSend PersonalSend `json:"personalSend"`
	Tools        []MCPTool    `json:"tools"`
}

// Drafts is registries/drafts.json.
type Drafts struct {
	Version         int    `json:"version"`
	RoomType        string `json:"roomType"`
	AccountDataType string `json:"accountDataType"`
	Field           string `json:"field"`
	BodyMaxLength   int    `json:"bodyMaxLength"`
	ClientMaxLength int    `json:"clientMaxLength"`
}

// SurfacePersonal is the surface of bipe-mcp.
const SurfacePersonal = "personal"

// ClassContent is the data class of tools that hand message text to the
// MCP client.
const ClassContent = "content"

// FullContentEventType is the room state event that turns on full reading
// of one chat. The state key is the member's user ID. Content
// {"full_content": true} means on; anything else means off.
const FullContentEventType = "chat.bipe.assistant"

var (
	mcpRegistry MCPRegistry
	drafts      Drafts
)

func init() {
	if err := json.Unmarshal(mcpToolsJSON, &mcpRegistry); err != nil {
		panic(fmt.Errorf("bipe: invalid mcp-tools.json: %w", err))
	}
	if err := json.Unmarshal(draftsJSON, &drafts); err != nil {
		panic(fmt.Errorf("bipe: invalid drafts.json: %w", err))
	}
}

// MCPTools returns the MCP tool registry.
func MCPTools() MCPRegistry { return mcpRegistry }

// DraftsRegistry returns the drafts registry.
func DraftsRegistry() Drafts { return drafts }

// PersonalTools returns the tools of the personal surface, in registry order.
func PersonalTools() []MCPTool {
	var out []MCPTool
	for _, t := range mcpRegistry.Tools {
		if t.Surface == SurfacePersonal {
			out = append(out, t)
		}
	}
	return out
}

// AssistantSendAllowed is the send switch rule (assistantSendAllowed in
// bipe-shared tools/lib/registries.mjs). content is the global account data
// chat.bipe.assistant_send, or nil when the account has none. Only an object
// with "enabled": true allows a send. Anything else is off.
func AssistantSendAllowed(content json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if len(content) == 0 || json.Unmarshal(content, &obj) != nil || obj == nil {
		return false
	}
	return strings.TrimSpace(string(obj["enabled"])) == "true"
}

// AssistantSender returns the client name of a message that an assistant
// sent, or "" (assistantSender in registries.mjs).
func AssistantSender(content map[string]any) string {
	m, ok := content[mcpRegistry.PersonalSend.MarkerField].(map[string]any)
	if !ok {
		return ""
	}
	client, _ := m["client"].(string)
	client = strings.TrimSpace(client)
	if client == "" || UTF16Len(client) > mcpRegistry.PersonalSend.ClientMaxLength {
		return ""
	}
	return client
}

// FullContentOn tells if the content of a chat.bipe.assistant state event
// turns full reading on. Only {"full_content": true} does.
func FullContentOn(content json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if len(content) == 0 || json.Unmarshal(content, &obj) != nil || obj == nil {
		return false
	}
	return strings.TrimSpace(string(obj["full_content"])) == "true"
}

// UTF16Len is the length that JavaScript gives a string. The registry limits
// count UTF-16 code units, like the reference rules.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// ClientName makes the client name that drafts and sent messages carry, from
// the MCP initialize clientInfo.name: trimmed, cut to the registry limit.
// A blank name becomes DefaultClientName.
func ClientName(name string) string {
	name = strings.TrimSpace(name)
	limit := min(drafts.ClientMaxLength, mcpRegistry.PersonalSend.ClientMaxLength)
	if UTF16Len(name) > limit {
		n := 0
		for i, r := range name {
			if n+utf16.RuneLen(r) > limit {
				name = strings.TrimSpace(name[:i])
				break
			}
			n += utf16.RuneLen(r)
		}
	}
	if name == "" {
		return DefaultClientName
	}
	return name
}

// DefaultClientName is the client name when the MCP client sends none.
const DefaultClientName = "Assistente"
