package qrlogin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/oauth"
)

var (
	// ErrNotSupported is returned when the homeserver advertises neither
	// MSC4108 nor MSC4388.
	ErrNotSupported = errors.New("the homeserver does not support QR code login")
	// ErrDeclined is returned when the user denies the login on the other
	// device.
	ErrDeclined = errors.New("the login was declined")
	// ErrAuthorizationExpired is returned when nobody approves the device
	// code in time.
	ErrAuthorizationExpired = errors.New("the login authorization expired")
	// ErrNoSecrets is returned by ReceiveSecrets when the other device does
	// not send m.login.secrets. The login itself is valid.
	ErrNoSecrets = errors.New("the other device sent no secrets")
)

// Config configures one login.
type Config struct {
	// HomeserverURL is the client-server API base URL.
	HomeserverURL string
	// HTTPClient is used for all requests. nil means http.DefaultClient.
	HTTPClient *http.Client
	// Client is the OAuth client metadata for dynamic registration. Set at
	// least ClientName and ClientURI. The login sets the grant types,
	// application type and auth method.
	Client oauth.ClientMetadata
	// DeviceID is the Matrix device ID to request. Empty means a random ID.
	DeviceID string
	// DisableMSC4388 uses MSC4108 even when the server advertises MSC4388.
	DisableMSC4388 bool
}

// Login is one QR code login from the new-device side. Call the methods in
// this order: Start, WaitForScan, ConfirmCheckCode, RequestAuthorization,
// WaitForApproval, ReceiveSecrets. Login is not safe for concurrent use.
type Login struct {
	cfg        Config
	http       *http.Client
	homeserver string
	variant    Variant
	deviceID   string

	pending *pendingChannel
	channel *secureChannel

	meta     *oauth.ServerMetadata
	clientID string
	code     *oauth.DeviceCodeResponse
	tokens   *Tokens
	userID   id.UserID
}

// Start checks the homeserver and makes the first QR code.
func Start(ctx context.Context, cfg Config) (*Login, error) {
	l := &Login{cfg: cfg, http: cfg.HTTPClient, homeserver: strings.TrimRight(cfg.HomeserverURL, "/") + "/"}
	if l.http == nil {
		l.http = http.DefaultClient
	}
	if u, err := url.Parse(l.homeserver); err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid homeserver URL")
	}
	l.deviceID = cfg.DeviceID
	if l.deviceID == "" {
		l.deviceID = NewDeviceID()
	}
	var err error
	if l.variant, err = detectVariant(ctx, l.http, l.homeserver, cfg.DisableMSC4388); err != nil {
		return nil, err
	}
	if err = l.RefreshQRCode(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// NewDeviceID makes a random device ID of ten capital letters, the form that
// Synapse uses.
func NewDeviceID() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

func detectVariant(ctx context.Context, cli *http.Client, homeserver string, disable4388 bool) (Variant, error) {
	endpoint, err := joinClientPath(homeserver, "/_matrix/client/versions")
	if err != nil {
		return 0, err
	}
	var versions struct {
		UnstableFeatures map[string]bool `json:"unstable_features"`
	}
	if err = getJSON(ctx, cli, endpoint, "", &versions); err != nil {
		return 0, fmt.Errorf("failed to get the homeserver versions: %w", err)
	}
	switch {
	case versions.UnstableFeatures["io.element.msc4388"] && !disable4388:
		return VariantMSC4388, nil
	case versions.UnstableFeatures["org.matrix.msc4108"]:
		return VariantMSC4108, nil
	default:
		return 0, ErrNotSupported
	}
}

// RefreshQRCode makes a new rendezvous session and key. Call it when
// WaitForScan returns ErrRendezvousExpired before a scan.
func (l *Login) RefreshQRCode(ctx context.Context) error {
	if l.channel != nil {
		return errors.New("the QR code was already scanned")
	}
	if l.pending != nil {
		l.pending.rv.close(ctx)
	}
	p, err := newPendingChannel(ctx, l.http, l.homeserver, l.variant)
	if err != nil {
		return fmt.Errorf("failed to create the rendezvous session: %w", err)
	}
	l.pending = p
	return nil
}

// QRCode returns the bytes to show as a QR code in byte mode.
func (l *Login) QRCode() ([]byte, error) {
	return l.pending.qr.Bytes()
}

// Variant tells which channel this login uses.
func (l *Login) Variant() Variant { return l.variant }

// DeviceID is the requested Matrix device ID.
func (l *Login) DeviceID() string { return l.deviceID }

// WaitForScan waits until the other device scans the QR code and opens the
// channel. It returns ErrRendezvousExpired when the session expires first.
func (l *Login) WaitForScan(ctx context.Context) error {
	if l.pending == nil {
		return errors.New("no QR code")
	}
	ch, err := l.pending.connect(ctx)
	if err != nil {
		return err
	}
	l.channel = ch
	return nil
}

// ConfirmCheckCode compares the two digits that the other device shows with
// the channel. On a mismatch the login cannot continue: start a new one.
func (l *Login) ConfirmCheckCode(ctx context.Context, typed string) error {
	if l.channel == nil {
		return errors.New("the QR code was not scanned")
	}
	if err := l.channel.confirm(typed); err != nil {
		l.Close(ctx)
		return err
	}
	return nil
}

// Authorization is what the user needs to approve the login on the other
// device.
type Authorization struct {
	// UserCode is the code that the authorization server asks for when it
	// does not get VerificationURIComplete. Show it to the user.
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresAt               time.Time
}

// RequestAuthorization reads the protocols of the other device, registers
// the client, starts the device authorization grant and sends it to the
// other device.
func (l *Login) RequestAuthorization(ctx context.Context) (*Authorization, error) {
	ch := l.channel
	if ch == nil || !ch.confirmed {
		return nil, errors.New("the secure channel is not confirmed")
	}
	msg, err := ch.receiveMessage(ctx)
	if err != nil {
		return nil, err
	} else if msg.Type != msgLoginProtocols {
		ch.sendFailure(ctx, FailureUnexpectedMessageReceived)
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrUnexpectedMessage, msgLoginProtocols, msg.Type)
	}
	if !containsString(msg.Protocols, protocolDeviceAuthorizationGrant) {
		ch.sendFailure(ctx, FailureUnsupportedProtocol)
		return nil, &LoginFailureError{Reason: FailureUnsupportedProtocol}
	}
	if hs := msg.homeserverURL(l.variant); hs != "" && !sameURL(hs, l.homeserver) {
		// The other device is authenticated by the check code, so follow
		// its homeserver, as matrix-rust-sdk does.
		u, err := url.Parse(hs)
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
			ch.sendFailure(ctx, FailureUnexpectedMessageReceived)
			return nil, fmt.Errorf("%w: invalid homeserver", ErrUnexpectedMessage)
		}
		l.homeserver = strings.TrimRight(hs, "/") + "/"
	}

	if l.meta, err = fetchServerMetadata(ctx, l.http, l.homeserver); err != nil {
		return nil, err
	}
	client := l.cfg.Client
	client.ApplicationType = oauth.ApplicationTypeNative
	client.GrantTypes = []oauth.GrantType{oauth.GrantTypeDeviceCode, oauth.GrantTypeRefreshToken}
	client.TokenEndpointAuthMethod = oauth.AuthMethodNone
	client.RedirectURIs = nil
	client.ResponseTypes = nil
	if l.clientID, err = registerClient(ctx, l.http, l.meta, &client); err != nil {
		return nil, err
	}
	if l.code, err = requestDeviceAuthorization(ctx, l.http, l.meta, l.clientID, l.deviceID); err != nil {
		return nil, err
	}

	err = ch.send(ctx, loginProtocolMessage(l.variant, l.deviceID, l.code.VerificationURI, l.code.VerificationURIComplete))
	if err != nil {
		return nil, err
	}
	msg, err = ch.receiveMessage(ctx)
	if err != nil {
		return nil, err
	} else if msg.Type != msgLoginProtocolAccepted {
		ch.sendFailure(ctx, FailureUnexpectedMessageReceived)
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrUnexpectedMessage, msgLoginProtocolAccepted, msg.Type)
	}
	auth := &Authorization{
		UserCode:                l.code.UserCode,
		VerificationURI:         l.code.VerificationURI,
		VerificationURIComplete: l.code.VerificationURIComplete,
	}
	if l.code.ExpiresIn.Duration > 0 {
		auth.ExpiresAt = time.Now().Add(l.code.ExpiresIn.Duration)
	}
	return auth, nil
}

// Result is the session of the new device.
type Result struct {
	HomeserverURL string
	UserID        id.UserID
	DeviceID      id.DeviceID
	Session       Session
}

// WaitForApproval polls the authorization server until the user approves
// the login on the other device. Then it checks the token with whoami.
func (l *Login) WaitForApproval(ctx context.Context) (*Result, error) {
	ch := l.channel
	if l.code == nil {
		return nil, errors.New("no device authorization")
	}
	tokens, err := pollDeviceCode(ctx, l.http, l.meta, l.clientID, l.code)
	switch {
	case IsOAuthError(err, oauthAccessDenied):
		_ = ch.send(ctx, &channelMessage{Type: msgLoginDeclined})
		return nil, ErrDeclined
	case IsOAuthError(err, oauthExpiredToken):
		ch.sendFailure(ctx, FailureAuthorizationExpired)
		return nil, ErrAuthorizationExpired
	case err != nil:
		return nil, err
	}
	l.tokens = tokens

	endpoint, err := joinClientPath(l.homeserver, "/_matrix/client/v3/account/whoami")
	if err != nil {
		return nil, err
	}
	var whoami struct {
		UserID   id.UserID   `json:"user_id"`
		DeviceID id.DeviceID `json:"device_id"`
	}
	if err = getJSON(ctx, l.http, endpoint, tokens.AccessToken, &whoami); err != nil {
		return nil, fmt.Errorf("whoami failed: %w", err)
	} else if whoami.UserID == "" {
		return nil, errors.New("whoami returned no user ID")
	} else if whoami.DeviceID != "" && string(whoami.DeviceID) != l.deviceID {
		return nil, errors.New("whoami returned another device ID")
	}
	l.userID = whoami.UserID
	return &Result{
		HomeserverURL: strings.TrimRight(l.homeserver, "/"),
		UserID:        whoami.UserID,
		DeviceID:      id.DeviceID(l.deviceID),
		Session: Session{
			ClientID:           l.clientID,
			TokenEndpoint:      l.meta.TokenEndpoint,
			RevocationEndpoint: l.meta.RevocationEndpoint,
			Tokens:             *tokens,
		},
	}, nil
}

// ReceiveSecrets tells the other device that the login worked and waits for
// m.login.secrets. The other device sends them after it sees the new device
// in the device list. ErrNoSecrets means that it sent none; the login stays
// valid but unverified. ReceiveSecrets closes the channel.
func (l *Login) ReceiveSecrets(ctx context.Context) (*SecretsBundle, error) {
	ch := l.channel
	if l.tokens == nil {
		return nil, errors.New("the login is not approved")
	}
	defer l.Close(context.WithoutCancel(ctx))
	if err := ch.send(ctx, &channelMessage{Type: msgLoginSuccess}); err != nil {
		return nil, err
	}
	msgType, data, err := ch.receive(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoSecrets, err)
	}
	defer clear(data)
	switch msgType {
	case msgLoginSecrets:
		return parseSecrets(data)
	case msgLoginFailure:
		return nil, fmt.Errorf("%w: the other device sent m.login.failure", ErrNoSecrets)
	default:
		ch.sendFailure(ctx, FailureUnexpectedMessageReceived)
		return nil, fmt.Errorf("%w: unexpected %s", ErrNoSecrets, msgType)
	}
}

// Cancel tells the other device that the user cancelled, and closes the
// channel.
func (l *Login) Cancel(ctx context.Context) {
	if l.channel != nil {
		l.channel.sendFailure(ctx, FailureUserCancelled)
	}
	l.Close(ctx)
}

// Close deletes the rendezvous session.
func (l *Login) Close(ctx context.Context) {
	if l.pending != nil {
		l.pending.rv.close(ctx)
		l.pending = nil
	}
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func sameURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}
