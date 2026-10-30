package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

// UserError makes a tool error with a pt-BR message for the user. The
// Matrix layer uses it, for example while the first sync runs.
func UserError(msg string) error { return userError(msg) }

// The JSON Schema of each tool's arguments.
var inputSchemas = map[string]string{
	"list_chats": `{"type":"object","properties":{},"additionalProperties":false}`,
	"read_my_messages": `{"type":"object","required":["room_id"],"additionalProperties":false,"properties":{
		"room_id":{"type":"string","description":"Room ID from list_chats (starts with !)."},
		"limit":{"type":"integer","minimum":1,"maximum":200,"description":"Most recent messages to return. Default 50."}}}`,
	"read_chat": `{"type":"object","required":["room_id"],"additionalProperties":false,"properties":{
		"room_id":{"type":"string","description":"Room ID from list_chats (starts with !)."},
		"limit":{"type":"integer","minimum":1,"maximum":200,"description":"Most recent messages to return. Default 50."}}}`,
	"set_full_content": `{"type":"object","required":["room_id","enabled"],"additionalProperties":false,"properties":{
		"room_id":{"type":"string","description":"Room ID from list_chats (starts with !)."},
		"enabled":{"type":"boolean","description":"true turns full reading on and shows every member a notice; false turns it off."}}}`,
	"draft_message": messageSchema,
	"send_message":  messageSchema,
}

const messageSchema = `{"type":"object","required":["room_id","body"],"additionalProperties":false,"properties":{
	"room_id":{"type":"string","description":"Target room ID from list_chats (starts with !)."},
	"body":{"type":"string","description":"Message text, at most 8000 characters. Markdown is fine."},
	"reply_to":{"type":"string","description":"Optional event ID to reply to (starts with $)."},
	"thread_root":{"type":"string","description":"Optional thread root event ID (starts with $)."}}}`

// NewServer makes the MCP server. It lists only the tools whose scope is
// granted; the SDK answers a call to any other tool with an error.
func NewServer(svc *Service, version string, log zerolog.Logger) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "bipe-mcp", Title: "Bipe", Version: version}, &mcp.ServerOptions{
		Instructions: "Tools for the user's own Bipe chats. Prefer draft_message: the user sends the draft from the phone.",
		// The SDK logs only protocol events. Keep them off: stdout is the
		// protocol and the log must never hold message text.
		Logger: slog.New(slog.DiscardHandler),
	})
	for _, t := range svc.Scopes().Tools() {
		readOnly := t.Writes == "none"
		destructive := false
		tool := &mcp.Tool{
			Name:        t.Name,
			Description: t.EN + "\n\n" + t.PT,
			InputSchema: json.RawMessage(inputSchemas[t.Name]),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive},
		}
		name := t.Name
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			client := ""
			if info := req.ClientInfo(); info != nil {
				client = info.Name
			}
			res, err := svc.Call(ctx, name, client, req.Params.Arguments)
			if err != nil {
				msg, ok := UserMessage(err)
				if !ok {
					msg = "Erro interno do bipe-mcp."
				}
				// The cause never holds a token or message text: Matrix
				// errors name the call and the error code.
				log.Warn().Str("tool", name).Str("error", errorCause(err)).Msg("Tool call failed")
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil
			}
			data, err := json.Marshal(res)
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
		})
	}
	return server
}

func errorCause(err error) string {
	if _, ok := err.(*inputError); ok {
		return "input"
	}
	return err.Error()
}

// ContentNotice is the line that bipe-mcp prints at start when a content
// scope is granted, or "".
func ContentNotice(scopes Scopes) string {
	if scopes.GrantsContent() {
		return bipe.MCPContentWarning
	}
	return ""
}
