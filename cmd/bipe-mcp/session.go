package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"

	"github.com/bipe-chat/bipe-beeper/pkg/qrlogin"
)

// storedSession is session.json in the config dir (mode 0600). It holds the
// OAuth tokens of the bipe-mcp device and the pickle key of its crypto
// store. Never log it.
type storedSession struct {
	HomeserverURL string `json:"homeserver_url"`
	UserID        string `json:"user_id"`
	DeviceID      string `json:"device_id"`
	PickleKey     string `json:"pickle_key"`

	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	AccessTokenExpiry time.Time `json:"access_token_expiry,omitzero"`

	OAuthClientID      string `json:"oauth_client_id"`
	TokenEndpoint      string `json:"oauth_token_endpoint"`
	RevocationEndpoint string `json:"oauth_revocation_endpoint,omitempty"`
}

func (s *storedSession) String() string   { return "storedSession{" + s.UserID + " " + s.DeviceID + "}" }
func (s *storedSession) GoString() string { return s.String() }

func (s *storedSession) oauth() qrlogin.Session {
	return qrlogin.Session{
		ClientID:           s.OAuthClientID,
		TokenEndpoint:      s.TokenEndpoint,
		RevocationEndpoint: s.RevocationEndpoint,
		Tokens: qrlogin.Tokens{
			AccessToken:  s.AccessToken,
			RefreshToken: s.RefreshToken,
			ExpiresAt:    s.AccessTokenExpiry,
		},
	}
}

func (s *storedSession) setTokens(t qrlogin.Tokens) {
	s.AccessToken = t.AccessToken
	s.RefreshToken = t.RefreshToken
	s.AccessTokenExpiry = t.ExpiresAt
}

func sessionPath(dir string) string { return filepath.Join(dir, "session.json") }
func cryptoPath(dir string) string  { return filepath.Join(dir, "crypto.db") }

// loadSession returns nil, nil when bipe-mcp has not signed in.
func loadSession(dir string) (*storedSession, error) {
	data, err := os.ReadFile(sessionPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var s storedSession
	if err = json.Unmarshal(data, &s); err != nil {
		return nil, errors.New("session.json is not valid")
	}
	if s.AccessToken == "" || s.UserID == "" || s.DeviceID == "" || s.PickleKey == "" {
		return nil, errors.New("session.json is incomplete")
	}
	return &s, nil
}

// saveSession writes the file atomically with mode 0600.
func saveSession(dir string, s *storedSession) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".session-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), sessionPath(dir))
}

// Refresh timing, as in the bridge connector (pkg/connector/session.go).
const (
	refreshMargin   = 60 * time.Second
	minRefreshWait  = 5 * time.Second
	maxRetryBackoff = 5 * time.Minute
)

// tokenKeeper is the one owner of the OAuth tokens of this process. MAS
// rotates the refresh token on each refresh, so a second refresher would end
// the session: the config dir lock keeps a second bipe-mcp out, and the mutex
// keeps refreshes in this process one at a time. It refreshes with
// qrlogin.Session, never with the mautrix-go OAuth calls, and gives the
// Matrix client only the access token.
type tokenKeeper struct {
	dir  string
	http *http.Client
	log  zerolog.Logger

	mu    sync.Mutex
	sess  *storedSession
	cli   *mautrix.Client
	ended bool
}

func newTokenKeeper(dir string, sess *storedSession, log zerolog.Logger) *tokenKeeper {
	return &tokenKeeper{dir: dir, sess: sess, http: http.DefaultClient, log: log}
}

// attach gives the current access token to the Matrix client, now and after
// each refresh. No refresh token goes to mautrix-go.
func (k *tokenKeeper) attach(cli *mautrix.Client) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.cli = cli
	cli.OAuthSetTokens("", "", k.sess.AccessToken, k.sess.AccessTokenExpiry)
}

// Ended tells if the session ended for good.
func (k *tokenKeeper) Ended() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.ended
}

// markEnded marks the session as ended: the homeserver no longer knows the
// device.
func (k *tokenKeeper) markEnded() {
	k.mu.Lock()
	k.ended = true
	k.mu.Unlock()
}

func (k *tokenKeeper) expiresWithin(d time.Duration) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return !k.sess.AccessTokenExpiry.IsZero() && time.Until(k.sess.AccessTokenExpiry) < d
}

// Refresh refreshes at once. An invalid_grant ends the session.
func (k *tokenKeeper) Refresh(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ended {
		return errSessionEnded
	}
	o := k.sess.oauth()
	if err := o.Refresh(ctx, k.http); err != nil {
		if qrlogin.IsOAuthError(err, qrlogin.OAuthInvalidGrant) {
			k.ended = true
			return errSessionEnded
		}
		return err
	}
	k.sess.setTokens(o.Tokens)
	if err := saveSession(k.dir, k.sess); err != nil {
		// The new refresh token is only in memory now. The next refresh
		// saves it again.
		k.log.Err(err).Msg("Failed to save the refreshed token")
	}
	if k.cli != nil {
		k.cli.OAuthSetTokens("", "", o.AccessToken, o.ExpiresAt)
	}
	return nil
}

var errSessionEnded = errors.New("the session ended")

// run refreshes before the access token expires, until ctx ends or the
// session ends.
func (k *tokenKeeper) run(ctx context.Context) {
	backoff := 30 * time.Second
	for {
		k.mu.Lock()
		expiry, ended, hasRefresh := k.sess.AccessTokenExpiry, k.ended, k.sess.RefreshToken != ""
		k.mu.Unlock()
		if ended || expiry.IsZero() || !hasRefresh {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(time.Until(expiry)-refreshMargin, minRefreshWait)):
		}
		err := k.Refresh(ctx)
		switch {
		case err == nil:
			backoff = 30 * time.Second
			continue
		case ctx.Err() != nil, errors.Is(err, errSessionEnded):
			return
		}
		k.log.Warn().Str("reason", errorKind(err)).Dur("retry_in", backoff).Msg("Failed to refresh the token")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxRetryBackoff)
	}
}

// revoke ends the device session on the server.
func (k *tokenKeeper) revoke(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ended = true
	o := k.sess.oauth()
	return o.Revoke(ctx, k.http)
}

// errorKind is a short, token-free description of an error.
func errorKind(err error) string {
	var failure *qrlogin.LoginFailureError
	var oauthErr *qrlogin.OAuthError
	var httpErr mautrix.HTTPError
	switch {
	case errors.As(err, &failure):
		return string(failure.Reason)
	case errors.As(err, &oauthErr):
		return oauthErr.Code
	case errors.As(err, &httpErr) && httpErr.RespError != nil:
		return httpErr.RespError.ErrCode
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}
