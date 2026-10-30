package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

func TestSessionFile(t *testing.T) {
	dir := t.TempDir()
	if s, err := loadSession(dir); err != nil || s != nil {
		t.Fatalf("empty dir: %v %v", s, err)
	}
	in := &storedSession{
		HomeserverURL: "https://matrix.bipe.net.br", UserID: "@ana:bipe.net.br", DeviceID: "ABCDEFGHIJ",
		PickleKey: "pk", AccessToken: "secret-access", RefreshToken: "secret-refresh",
		AccessTokenExpiry: time.Now().Add(time.Hour).Truncate(time.Second),
		OAuthClientID:     "client", TokenEndpoint: "https://auth/token",
	}
	if err := saveSession(dir, in); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(sessionPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	out, err := loadSession(dir)
	if err != nil || out.RefreshToken != "secret-refresh" || !out.AccessTokenExpiry.Equal(in.AccessTokenExpiry) {
		t.Fatalf("%v %v", out, err)
	}
	// Printing a session never shows a token.
	for _, s := range []string{out.String(), out.GoString()} {
		if strings.Contains(s, "secret") || strings.Contains(s, "pk") {
			t.Fatalf("leaks: %s", s)
		}
	}
}

func TestRunWithoutSession(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--config", t.TempDir()}, strings.NewReader(""), &stderr)
	if code != 1 || !strings.Contains(stderr.String(), bipe.MCPNotSignedIn) {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestRunBadScope(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--config", t.TempDir(), "--scopes", "chats:read,everything"}, strings.NewReader(""), &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "unknown scope") {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
}

func TestLockDir(t *testing.T) {
	dir := t.TempDir()
	ok, err := lockDir(dir)
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	// flock locks are per open file, so a second open in this process
	// conflicts like a second process would.
	if lockFile == nil {
		t.Skip("no flock on this platform")
	}
	held := lockFile
	ok, err = lockDir(dir)
	if err != nil || ok {
		t.Fatalf("second lock: %v %v", ok, err)
	}
	held.Close()
}
