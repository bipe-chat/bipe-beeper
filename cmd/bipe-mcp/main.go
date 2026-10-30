// bipe-mcp is the personal MCP server of Bipe (bipe ADR 0130 surface 3,
// ADR 0210; bipe-chat/bipe#439). It runs on the user's machine, signs in as
// a new device with the phone (MSC4108 QR login), and serves the personal
// tools of the bipe-shared registry to one MCP client over stdio. It has no
// network listener.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
	"github.com/bipe-chat/bipe-beeper/pkg/bipe/mcpserver"
)

// Set at build time with -ldflags -X.
var Version = "0.1.0"

const usage = `bipe-mcp: the Bipe MCP server on your machine.

Usage:
  bipe-mcp login   [--homeserver URL] [--config DIR]
  bipe-mcp         [--scopes LIST] [--config DIR]     (the MCP client starts this)
  bipe-mcp logout  [--config DIR]

Scopes (--scopes, comma-separated, default chats:read,content-own):
  chats:read     list_chats, set_full_content
  content-own    read_my_messages, draft_message
  content-full   read_chat (only chats with full reading on)
  content-send   send_message (only while "Assistente pode enviar" is on)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stderr))
}

func run(args []string, stdin io.Reader, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && (args[0] == "login" || args[0] == "logout" || args[0] == "serve") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("bipe-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	scopeList := fs.String("scopes", envOr("BIPE_MCP_SCOPES", mcpserver.DefaultScopes), "granted scopes")
	homeserver := fs.String("homeserver", envOr("BIPE_MCP_HOMESERVER", bipe.DefaultHomeserverURL), "homeserver URL")
	configDir := fs.String("config", os.Getenv("BIPE_MCP_CONFIG"), "config dir (default: the user config dir + /bipe-mcp)")
	version := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *version {
		fmt.Fprintln(stderr, "bipe-mcp", Version)
		return 0
	}
	log := zerolog.New(zerolog.ConsoleWriter{Out: stderr, NoColor: true}).With().Timestamp().Logger().Level(zerolog.InfoLevel)

	dir, err := resolveDir(*configDir)
	if err != nil {
		fmt.Fprintln(stderr, "bipe-mcp:", err)
		return 1
	}
	if ok, err := lockDir(dir); err != nil {
		fmt.Fprintln(stderr, "bipe-mcp:", err)
		return 1
	} else if !ok {
		fmt.Fprintln(stderr, bipe.MCPAlreadyRunning)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = log.WithContext(ctx)

	switch cmd {
	case "login":
		if err = runLogin(ctx, dir, *homeserver, stdin, stderr, log); err != nil {
			fmt.Fprintln(stderr, "bipe-mcp:", err)
			return 1
		}
		return 0
	case "logout":
		return runLogout(ctx, dir, stderr, log)
	}

	scopes, err := mcpserver.ParseScopes(*scopeList)
	if err != nil {
		fmt.Fprintln(stderr, "bipe-mcp:", err)
		return 2
	}
	sess, err := loadSession(dir)
	if err != nil {
		fmt.Fprintln(stderr, "bipe-mcp:", err)
		return 1
	}
	if sess == nil {
		if !isTerminal(stdin) {
			fmt.Fprintln(stderr, bipe.MCPNotSignedIn)
			return 1
		}
		// First run in a terminal: sign in, then let the MCP client start
		// bipe-mcp.
		if err = runLogin(ctx, dir, *homeserver, stdin, stderr, log); err != nil {
			fmt.Fprintln(stderr, "bipe-mcp:", err)
			return 1
		}
		return 0
	}
	if notice := mcpserver.ContentNotice(scopes); notice != "" {
		fmt.Fprintln(stderr, notice)
	}
	if err = serve(ctx, dir, sess, scopes, log); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "bipe-mcp:", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, dir string, sess *storedSession, scopes mcpserver.Scopes, log zerolog.Logger) error {
	tokens := newTokenKeeper(dir, sess, log)
	m, err := openMatrix(ctx, dir, sess, tokens, log)
	if errors.Is(err, errSessionEnded) {
		return errors.New(bipe.MCPSessionEnded)
	} else if err != nil {
		return err
	}
	defer m.close()
	m.loadSecrets(ctx)
	go tokens.run(ctx)
	m.startSync(ctx)
	log.Info().Str("scopes", scopes.String()).Msg("Serving MCP on stdio")
	server := mcpserver.NewServer(mcpserver.NewService(m, scopes), Version, log)
	return server.Run(ctx, &mcp.StdioTransport{})
}

func runLogout(ctx context.Context, dir string, stderr io.Writer, log zerolog.Logger) int {
	sess, err := loadSession(dir)
	if err != nil || sess == nil {
		fmt.Fprintln(stderr, bipe.MCPNotSignedIn)
		return 1
	}
	if err = newTokenKeeper(dir, sess, log).revoke(ctx); err != nil {
		log.Warn().Str("reason", errorKind(err)).Msg("Failed to revoke the session")
	}
	for _, p := range []string{sessionPath(dir), cryptoPath(dir), cryptoPath(dir) + "-wal", cryptoPath(dir) + "-shm"} {
		if err = os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "bipe-mcp:", err)
			return 1
		}
	}
	return 0
}

// resolveDir returns the config dir and makes it with mode 0700.
func resolveDir(dir string) (string, error) {
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "bipe-mcp")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, os.Chmod(dir, 0o700)
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
