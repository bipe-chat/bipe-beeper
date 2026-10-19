package connector

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
	"github.com/bipe-chat/bipe-beeper/pkg/qrlogin"
)

// The OAuth session of each QR login. One oauthSession per login owns the
// tokens: MAS rotates the refresh token on each refresh, so two refreshers
// would end the session. The Matrix client gets the access token from here
// (AccessToken, OnToken) and reports a revoked token with Ended.

const (
	// refreshMargin is how long before expiry the session refreshes.
	refreshMargin   = 60 * time.Second
	minRefreshWait  = 5 * time.Second
	maxRetryBackoff = 5 * time.Minute
)

func (m *UserLoginMetadata) setTokens(t qrlogin.Tokens) {
	m.AccessToken = t.AccessToken
	m.RefreshToken = t.RefreshToken
	m.AccessTokenExpiry = t.ExpiresAt
}

type sessionRegistry struct {
	mu       sync.Mutex
	sessions map[networkid.UserLoginID]*oauthSession
}

// start begins the refresh loop of a login with a refresh token. It replaces
// an older session of the same login.
func (r *sessionRegistry) start(c *Connector, login *bridgev2.UserLogin) *oauthSession {
	meta, ok := login.Metadata.(*UserLoginMetadata)
	if !ok || meta.RefreshToken == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = make(map[networkid.UserLoginID]*oauthSession)
	}
	if old := r.sessions[login.ID]; old != nil {
		old.stop()
	}
	ctx, cancel := context.WithCancel(login.Log.WithContext(context.Background()))
	s := &oauthSession{login: login, meta: meta, cancel: cancel, http: http.DefaultClient}
	r.sessions[login.ID] = s
	go s.run(ctx)
	return s
}

// get returns the session of a login, or nil.
func (r *sessionRegistry) get(id networkid.UserLoginID) *oauthSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

// remove stops and forgets the session of a login.
func (r *sessionRegistry) remove(id networkid.UserLoginID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sessions[id]; s != nil {
		s.stop()
		delete(r.sessions, id)
	}
}

type oauthSession struct {
	login  *bridgev2.UserLogin
	http   *http.Client
	cancel context.CancelFunc

	mu       sync.Mutex
	meta     *UserLoginMetadata
	listener func(accessToken string, expiry time.Time)
	ended    bool
}

func (s *oauthSession) stop() { s.cancel() }

// AccessToken returns the current access token.
func (s *oauthSession) AccessToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta.AccessToken
}

// SetListener sets the function that gets each new access token. It
// replaces the previous one.
func (s *oauthSession) SetListener(fn func(accessToken string, expiry time.Time)) {
	s.mu.Lock()
	s.listener = fn
	s.mu.Unlock()
}

// expiresWithin tells if the access token expires within d.
func (s *oauthSession) expiresWithin(d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.meta.AccessTokenExpiry.IsZero() && time.Until(s.meta.AccessTokenExpiry) < d
}

// RefreshNow refreshes at once, for example after a 401 from the
// homeserver. It returns after the refresh.
func (s *oauthSession) RefreshNow(ctx context.Context) error {
	return s.refresh(ctx)
}

func (s *oauthSession) run(ctx context.Context) {
	backoff := 30 * time.Second
	for {
		s.mu.Lock()
		expiry, ended := s.meta.AccessTokenExpiry, s.ended
		s.mu.Unlock()
		if ended || expiry.IsZero() {
			return
		}
		wait := max(time.Until(expiry)-refreshMargin, minRefreshWait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		err := s.refresh(ctx)
		if err == nil {
			backoff = 30 * time.Second
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if qrlogin.IsOAuthError(err, qrlogin.OAuthInvalidGrant) {
			s.Ended(ctx)
			return
		}
		s.login.Log.Warn().Str("reason", errorKind(err)).Dur("retry_in", backoff).Msg("Failed to refresh the Bipe token")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxRetryBackoff)
	}
}

func (s *oauthSession) refresh(ctx context.Context) error {
	tokens, listener, err := s.refreshLocked(ctx)
	if err != nil {
		return err
	}
	// Call the listener without the lock, so it can read the session.
	if listener != nil {
		listener(tokens.AccessToken, tokens.ExpiresAt)
	}
	return nil
}

func (s *oauthSession) refreshLocked(ctx context.Context) (qrlogin.Tokens, func(string, time.Time), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return qrlogin.Tokens{}, nil, context.Canceled
	}
	sess := qrlogin.Session{
		ClientID:           s.meta.OAuthClientID,
		TokenEndpoint:      s.meta.TokenEndpoint,
		RevocationEndpoint: s.meta.RevocationEndpoint,
		Tokens: qrlogin.Tokens{
			AccessToken:  s.meta.AccessToken,
			RefreshToken: s.meta.RefreshToken,
			ExpiresAt:    s.meta.AccessTokenExpiry,
		},
	}
	if err := sess.Refresh(ctx, s.http); err != nil {
		return qrlogin.Tokens{}, nil, err
	}
	s.meta.setTokens(sess.Tokens)
	if err := s.login.Save(ctx); err != nil {
		// The new refresh token is only in memory now. The next refresh
		// saves it again.
		s.login.Log.Err(err).Msg("Failed to save the refreshed Bipe token")
	}
	return sess.Tokens, s.listener, nil
}

// Ended marks the session as ended: the refresh failed for good or the
// homeserver no longer knows the token (the user removed the device). It
// tells the user and stops the sync.
func (s *oauthSession) Ended(ctx context.Context) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.mu.Unlock()
	s.cancel()
	s.login.Log.Warn().Msg("The Bipe session ended")
	if client, ok := s.login.Client.(*Client); ok {
		client.loggedIn.Store(false)
	}
	s.login.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateBadCredentials,
		Error:      "chat.bipe.session_ended",
		Message:    bipe.SessionEnded,
	})
	if s.login.Client != nil {
		go s.login.Client.Disconnect()
	}
}

// Revoke ends the session on the server, for a logout from the bridge.
func (s *oauthSession) Revoke(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := qrlogin.Session{
		ClientID:           s.meta.OAuthClientID,
		TokenEndpoint:      s.meta.TokenEndpoint,
		RevocationEndpoint: s.meta.RevocationEndpoint,
		Tokens:             qrlogin.Tokens{AccessToken: s.meta.AccessToken, RefreshToken: s.meta.RefreshToken},
	}
	s.ended = true
	s.cancel()
	return sess.Revoke(ctx, s.http)
}

// oauthSession returns the OAuth session of this login, or nil for a login
// without a refresh token (the development token login).
func (c *Client) oauthSession() *oauthSession {
	return c.main.sessions.get(c.login.ID)
}

// useOAuthSession gives new access tokens to the Matrix client, and
// refreshes a token that expired while the bridge was off.
func (c *Client) useOAuthSession(ctx context.Context) {
	s := c.oauthSession()
	if s == nil {
		return
	}
	s.SetListener(func(accessToken string, expiry time.Time) {
		// No refresh token here: only the oauthSession refreshes.
		c.cli.OAuthSetTokens("", "", accessToken, expiry)
	})
	if s.expiresWithin(refreshMargin) {
		if err := s.RefreshNow(ctx); qrlogin.IsOAuthError(err, qrlogin.OAuthInvalidGrant) {
			s.Ended(ctx)
		} else if err != nil {
			c.login.Log.Warn().Str("reason", errorKind(err)).Msg("Failed to refresh the Bipe token before connecting")
		}
	}
}

// recoverUnknownToken handles M_UNKNOWN_TOKEN. With an OAuth session it
// refreshes the token and returns true to retry. Without one, or when the
// session ended, it reports bad credentials and returns false.
func (c *Client) recoverUnknownToken(ctx context.Context, err error) bool {
	s := c.oauthSession()
	if s == nil {
		c.setBadCredentials(err)
		return false
	}
	rerr := s.RefreshNow(ctx)
	if qrlogin.IsOAuthError(rerr, qrlogin.OAuthInvalidGrant) || errors.Is(rerr, context.Canceled) && ctx.Err() == nil {
		s.Ended(ctx)
		return false
	} else if rerr != nil {
		c.login.Log.Warn().Str("reason", errorKind(rerr)).Msg("Failed to refresh the Bipe token")
	}
	return true
}
