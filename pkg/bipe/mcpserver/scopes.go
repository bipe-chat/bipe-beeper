// Package mcpserver is the tool layer of bipe-mcp, the personal MCP server
// (bipe ADR 0130 surface 3, ADR 0210; bipe-chat/bipe#439). It serves the
// personal tools of the bipe-shared registry over a Matrix interface, so the
// rules can be tested without a homeserver.
package mcpserver

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

// DefaultScopes are the scopes when the user grants none: chat metadata and
// the user's own messages.
const DefaultScopes = "chats:read,content-own"

// Scopes is the set of scopes that the user granted.
type Scopes map[string]bool

// KnownScopes returns the scopes of the personal tools, in registry order.
func KnownScopes() []string {
	var out []string
	for _, t := range bipe.PersonalTools() {
		if !slices.Contains(out, t.Scope) {
			out = append(out, t.Scope)
		}
	}
	return out
}

// ParseScopes reads a comma-separated scope list. An empty list means
// DefaultScopes. An unknown scope is an error.
func ParseScopes(s string) (Scopes, error) {
	if strings.TrimSpace(s) == "" {
		s = DefaultScopes
	}
	known := KnownScopes()
	out := Scopes{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !slices.Contains(known, part) {
			return nil, fmt.Errorf("unknown scope %q (known: %s)", part, strings.Join(known, ", "))
		}
		out[part] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scope")
	}
	return out, nil
}

// String lists the scopes in registry order.
func (s Scopes) String() string {
	var out []string
	for _, k := range KnownScopes() {
		if s[k] {
			out = append(out, k)
		}
	}
	return strings.Join(out, ",")
}

// Tools returns the personal tools whose scope is granted.
func (s Scopes) Tools() []bipe.MCPTool {
	var out []bipe.MCPTool
	for _, t := range bipe.PersonalTools() {
		if s[t.Scope] {
			out = append(out, t)
		}
	}
	return out
}

// GrantsContent tells if a granted scope belongs to a content tool. Then
// message text goes to the MCP client's model.
func (s Scopes) GrantsContent() bool {
	for _, t := range s.Tools() {
		if t.Class == bipe.ClassContent {
			return true
		}
	}
	return false
}
