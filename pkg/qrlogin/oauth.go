package qrlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/oauth"
)

// The OAuth 2.0 calls of the login. mautrix-go has the same calls on
// mautrix.Client, but it logs form request bodies at debug level, and those
// bodies hold the device code and the refresh token. These calls log nothing.

// OAuthError is an error response from the authorization server (RFC 6749
// section 5.2). It never holds a token.
type OAuthError struct {
	Status      int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("oauth: %s (HTTP %d): %s", e.Code, e.Status, e.Description)
	}
	return fmt.Sprintf("oauth: %s (HTTP %d)", e.Code, e.Status)
}

// IsOAuthError tells if err is an OAuth error with the code.
func IsOAuthError(err error, code string) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && oe.Code == code
}

const (
	oauthAuthorizationPending = "authorization_pending"
	oauthSlowDown             = "slow_down"
	oauthAccessDenied         = "access_denied"
	oauthExpiredToken         = "expired_token"
	// OAuthInvalidGrant is the code when a refresh token is revoked, for
	// example because the user removed the device.
	OAuthInvalidGrant = "invalid_grant"
)

const maxOAuthBody = 1 << 20

// devicePollInterval overrides the poll interval of the server. Tests set it.
var devicePollInterval time.Duration

func readOAuthResponse(resp *http.Response, out any) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		oe := &OAuthError{Status: resp.StatusCode}
		var body struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
			ErrCode          string `json:"errcode"`
		}
		if json.Unmarshal(data, &body) == nil {
			oe.Code = body.Error
			if oe.Code == "" {
				oe.Code = body.ErrCode
			}
			oe.Description = body.ErrorDescription
		}
		if oe.Code == "" {
			oe.Code = "unknown"
		}
		return oe
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("oauth: invalid response: %w", err)
	}
	return nil
}

func postForm(ctx context.Context, cli *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer resp.Body.Close()
	return readOAuthResponse(resp, out)
}

// redactURLError drops the request from a *url.Error. The URL of a form POST
// holds no secret, but this keeps errors uniform.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %s: %w", ue.Op, ue.URL, ue.Err)
	}
	return err
}

func getJSON(ctx context.Context, cli *http.Client, endpoint, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer resp.Body.Close()
	return readOAuthResponse(resp, out)
}

// fetchServerMetadata reads the authorization server metadata of the
// homeserver (MSC2965).
func fetchServerMetadata(ctx context.Context, cli *http.Client, homeserver string) (*oauth.ServerMetadata, error) {
	var meta oauth.ServerMetadata
	var lastErr error
	for _, path := range []string{
		"/_matrix/client/v1/auth_metadata",
		"/_matrix/client/unstable/org.matrix.msc2965/auth_metadata",
	} {
		endpoint, err := joinClientPath(homeserver, path)
		if err != nil {
			return nil, err
		}
		if lastErr = getJSON(ctx, cli, endpoint, "", &meta); lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("failed to get the authorization server metadata: %w", lastErr)
	}
	if meta.DeviceAuthorizationEndpoint == "" || meta.TokenEndpoint == "" || meta.RegistrationEndpoint == "" {
		return nil, errors.New("the authorization server does not support the device authorization grant or client registration")
	}
	for _, endpoint := range []string{meta.DeviceAuthorizationEndpoint, meta.TokenEndpoint, meta.RegistrationEndpoint} {
		if u, err := url.Parse(endpoint); err != nil || u.Scheme != "https" && u.Scheme != "http" {
			return nil, errors.New("the authorization server metadata has an invalid endpoint")
		}
	}
	return &meta, nil
}

// registerClient registers the client dynamically (RFC 7591) and returns
// the client ID.
func registerClient(ctx context.Context, cli *http.Client, meta *oauth.ServerMetadata, client *oauth.ClientMetadata) (string, error) {
	body, err := json.Marshal(client)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return "", redactURLError(err)
	}
	defer resp.Body.Close()
	var registered oauth.ClientMetadata
	if err = readOAuthResponse(resp, &registered); err != nil {
		return "", fmt.Errorf("client registration failed: %w", err)
	} else if registered.ClientID == "" {
		return "", errors.New("client registration returned no client ID")
	}
	return registered.ClientID, nil
}

// deviceScopes are the scopes of a Matrix device (MSC2967).
func deviceScopes(deviceID string) string {
	return oauth.ScopeList{oauth.ScopeClientAPI, oauth.ScopeDevice(id.DeviceID(deviceID))}.String()
}

func requestDeviceAuthorization(ctx context.Context, cli *http.Client, meta *oauth.ServerMetadata, clientID, deviceID string) (*oauth.DeviceCodeResponse, error) {
	var resp oauth.DeviceCodeResponse
	err := postForm(ctx, cli, meta.DeviceAuthorizationEndpoint, url.Values{
		"client_id": {clientID},
		"scope":     {deviceScopes(deviceID)},
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("device authorization failed: %w", err)
	} else if resp.DeviceCode == "" || resp.VerificationURI == "" {
		return nil, errors.New("device authorization returned no device code or verification URI")
	}
	return &resp, nil
}

// Tokens is an OAuth 2.0 token pair. It never prints the tokens.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func (t Tokens) String() string {
	return fmt.Sprintf("Tokens{expires_at: %s, refresh: %t}", t.ExpiresAt.Format(time.RFC3339), t.RefreshToken != "")
}

func (t Tokens) GoString() string { return t.String() }

func tokensFrom(resp *oauth.TokenResponse, start time.Time) (*Tokens, error) {
	if resp.AccessToken == "" {
		return nil, errors.New("token response has no access token")
	}
	t := &Tokens{AccessToken: resp.AccessToken, RefreshToken: resp.RefreshToken}
	if resp.ExpiresIn.Duration > 0 {
		t.ExpiresAt = start.Add(resp.ExpiresIn.Duration)
	}
	return t, nil
}

// pollDeviceCode polls the token endpoint until the user approves, denies or
// the code expires (RFC 8628 section 3.4 and 3.5).
func pollDeviceCode(ctx context.Context, cli *http.Client, meta *oauth.ServerMetadata, clientID string, code *oauth.DeviceCodeResponse) (*Tokens, error) {
	interval := code.Interval.Duration
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if devicePollInterval > 0 {
		interval = devicePollInterval
	}
	var deadline time.Time
	if code.ExpiresIn.Duration > 0 {
		deadline = time.Now().Add(code.ExpiresIn.Duration)
	}
	for {
		if err := sleepCtx(ctx, interval); err != nil {
			return nil, err
		}
		var resp oauth.TokenResponse
		start := time.Now()
		err := postForm(ctx, cli, meta.TokenEndpoint, url.Values{
			"grant_type":  {string(oauth.GrantTypeDeviceCode)},
			"device_code": {code.DeviceCode},
			"client_id":   {clientID},
		}, &resp)
		switch {
		case err == nil:
			return tokensFrom(&resp, start)
		case IsOAuthError(err, oauthAuthorizationPending):
		case IsOAuthError(err, oauthSlowDown):
			interval += 5 * time.Second
		default:
			return nil, err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, &OAuthError{Status: http.StatusBadRequest, Code: oauthExpiredToken}
		}
	}
}

// Session is what a client needs to keep and refresh its OAuth tokens.
type Session struct {
	ClientID           string
	TokenEndpoint      string
	RevocationEndpoint string
	Tokens
}

// Refresh exchanges the refresh token for a new token pair (RFC 6749
// section 6). The server may keep the old refresh token; then Refresh keeps
// it too. An OAuthError with code OAuthInvalidGrant means that the session
// ended.
func (s *Session) Refresh(ctx context.Context, cli *http.Client) error {
	if cli == nil {
		cli = http.DefaultClient
	}
	if s.RefreshToken == "" {
		return errors.New("no refresh token")
	}
	var resp oauth.TokenResponse
	start := time.Now()
	err := postForm(ctx, cli, s.TokenEndpoint, url.Values{
		"grant_type":    {string(oauth.GrantTypeRefreshToken)},
		"refresh_token": {s.RefreshToken},
		"client_id":     {s.ClientID},
	}, &resp)
	if err != nil {
		return err
	}
	tokens, err := tokensFrom(&resp, start)
	if err != nil {
		return err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = s.RefreshToken
	}
	s.Tokens = *tokens
	return nil
}

// Revoke revokes the refresh token, which ends the device session on the
// server (RFC 7009).
func (s *Session) Revoke(ctx context.Context, cli *http.Client) error {
	if cli == nil {
		cli = http.DefaultClient
	}
	if s.RevocationEndpoint == "" {
		return errors.New("the authorization server has no revocation endpoint")
	}
	form := url.Values{"client_id": {s.ClientID}}
	if s.RefreshToken != "" {
		form.Set("token", s.RefreshToken)
		form.Set("token_type_hint", "refresh_token")
	} else {
		form.Set("token", s.AccessToken)
		form.Set("token_type_hint", "access_token")
	}
	return postForm(ctx, cli, s.RevocationEndpoint, form, nil)
}
