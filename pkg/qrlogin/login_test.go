package qrlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/oauth"
)

func TestMain(m *testing.M) {
	pollInterval = 10 * time.Millisecond
	devicePollInterval = 10 * time.Millisecond
	os.Exit(m.Run())
}

// fakeServer is a homeserver with the Synapse rendezvous endpoints of both
// MSCs, and an authorization server with the MAS device code grant.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	advertise4388 bool

	mu         sync.Mutex
	sessions   map[string]*fakeSession
	nextID     int
	approve    chan bool // true approves, false denies
	approved   *bool
	deviceCode string
	scope      string
	clientName string
	refreshes  int
}

type fakeSession struct {
	data    string
	version int
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, sessions: map[string]*fakeSession{}, approve: make(chan bool, 1)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/_matrix/client/versions":
		f.json(w, 200, map[string]any{"unstable_features": map[string]bool{
			"org.matrix.msc4108": true, "io.element.msc4388": f.advertise4388,
		}})
	case path == "/_matrix/client/unstable/org.matrix.msc4108/rendezvous" && r.Method == http.MethodPost:
		f.nextID++
		id := fmt.Sprintf("S%d", f.nextID)
		f.sessions[id] = &fakeSession{version: 1}
		w.Header().Set("ETag", `"1"`)
		f.json(w, http.StatusCreated, map[string]string{"url": f.srv.URL + "/_synapse/client/rendezvous/" + id})
	case strings.HasPrefix(path, "/_synapse/client/rendezvous/"):
		f.serveMSC4108(w, r, strings.TrimPrefix(path, "/_synapse/client/rendezvous/"))
	case path == msc4388Path && r.Method == http.MethodPost:
		f.nextID++
		id := fmt.Sprintf("R%d", f.nextID)
		f.sessions[id] = &fakeSession{version: 1}
		f.json(w, 200, msc4388Session{ID: id, SequenceToken: "1", ExpiresInMS: 60000})
	case strings.HasPrefix(path, msc4388Path+"/"):
		f.serveMSC4388(w, r, strings.Split(strings.TrimPrefix(path, msc4388Path+"/"), "/")[0])
	case path == "/_matrix/client/v1/auth_metadata":
		f.json(w, 200, oauth.ServerMetadata{
			Issuer:                      f.srv.URL + "/",
			RegistrationEndpoint:        f.srv.URL + "/oauth2/registration",
			DeviceAuthorizationEndpoint: f.srv.URL + "/oauth2/device",
			TokenEndpoint:               f.srv.URL + "/oauth2/token",
			RevocationEndpoint:          f.srv.URL + "/oauth2/revoke",
		})
	case path == "/oauth2/registration":
		var meta oauth.ClientMetadata
		_ = json.NewDecoder(r.Body).Decode(&meta)
		f.clientName = meta.ClientName
		if len(meta.GrantTypes) != 2 || meta.TokenEndpointAuthMethod != oauth.AuthMethodNone {
			f.json(w, 400, map[string]string{"error": "invalid_client_metadata"})
			return
		}
		meta.ClientID = "CLIENT1"
		f.json(w, 201, meta)
	case path == "/oauth2/device":
		_ = r.ParseForm()
		f.scope = r.PostForm.Get("scope")
		f.deviceCode = "DEVCODE"
		f.json(w, 200, map[string]any{
			"device_code": f.deviceCode, "user_code": "ABCD-1234",
			"verification_uri": f.srv.URL + "/link", "expires_in": 600, "interval": 1,
		})
	case path == "/oauth2/token":
		_ = r.ParseForm()
		switch r.PostForm.Get("grant_type") {
		case string(oauth.GrantTypeDeviceCode):
			if f.approved == nil {
				select {
				case ok := <-f.approve:
					f.approved = &ok
				default:
				}
			}
			switch {
			case r.PostForm.Get("device_code") != f.deviceCode:
				f.json(w, 400, map[string]string{"error": "invalid_grant"})
			case f.approved == nil:
				f.json(w, 400, map[string]string{"error": "authorization_pending"})
			case !*f.approved:
				f.json(w, 403, map[string]string{"error": "access_denied"})
			default:
				f.json(w, 200, map[string]any{"access_token": "AT1", "refresh_token": "RT1", "expires_in": 300, "token_type": "Bearer"})
			}
		case string(oauth.GrantTypeRefreshToken):
			if r.PostForm.Get("refresh_token") == "REVOKED" {
				f.json(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			f.refreshes++
			f.json(w, 200, map[string]any{"access_token": fmt.Sprintf("AT%d", f.refreshes+1), "expires_in": 300, "token_type": "Bearer"})
		}
	case path == "/_matrix/client/v3/account/whoami":
		if r.Header.Get("Authorization") != "Bearer AT1" {
			f.json(w, 401, map[string]string{"errcode": "M_UNKNOWN_TOKEN"})
			return
		}
		device := strings.TrimPrefix(strings.Fields(f.scope)[1], "urn:matrix:client:device:")
		f.json(w, 200, map[string]string{"user_id": "@alice:example.org", "device_id": device})
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeServer) serveMSC4108(w http.ResponseWriter, r *http.Request, id string) {
	s := f.sessions[id]
	if s == nil {
		f.json(w, 404, map[string]string{"errcode": "M_NOT_FOUND"})
		return
	}
	etag := strconv.Quote(strconv.Itoa(s.version))
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, s.data)
	case http.MethodPut:
		if r.Header.Get("If-Match") != etag {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.data = string(body)
		s.version++
		w.Header().Set("ETag", strconv.Quote(strconv.Itoa(s.version)))
		w.WriteHeader(http.StatusAccepted)
	case http.MethodDelete:
		delete(f.sessions, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeServer) serveMSC4388(w http.ResponseWriter, r *http.Request, id string) {
	s := f.sessions[id]
	if s == nil {
		f.json(w, 404, map[string]string{"errcode": "M_NOT_FOUND"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.json(w, 200, msc4388Session{SequenceToken: strconv.Itoa(s.version), Data: s.data, ExpiresInMS: 60000})
	case http.MethodPut:
		var req msc4388Session
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.SequenceToken != strconv.Itoa(s.version) {
			f.json(w, 409, map[string]string{"errcode": "M_CONCURRENT_WRITE"})
			return
		}
		s.data = req.Data
		s.version++
		f.json(w, 200, map[string]string{"sequence_token": strconv.Itoa(s.version)})
	case http.MethodDelete:
		delete(f.sessions, id)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "{}")
	}
}

// fakePhone is the signed-in device: the rust-sdk GrantLoginWithScannedQrCode
// flow.
type fakePhone struct {
	f          *fakeServer
	ch         *secureChannel
	checkCode  uint8
	sendSecret bool
	deny       bool
}

func (p *fakePhone) scan(ctx context.Context, qrBytes []byte) error {
	qr, err := ParseQRCode(qrBytes)
	if err != nil {
		return err
	} else if qr.Intent != IntentLogin {
		return errors.New("phone: QR code intent is not login")
	}
	cli := p.f.srv.Client()
	sc := &secureChannel{variant: qr.Variant, confirmed: true}
	switch qr.Variant {
	case VariantMSC4108:
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, qr.RendezvousURL, nil)
		resp, err := cli.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		rv := &msc4108Rendezvous{http: cli, url: qr.RendezvousURL, etag: resp.Header.Get("ETag")}
		key, _ := newECIESKeyPair()
		ch, initial, err := key.establishOutbound(qr.PublicKey, []byte(loginInitiateMessage))
		if err != nil {
			return err
		}
		if err = rv.send(ctx, initial); err != nil {
			return err
		}
		sc.rv, sc.crypto = rv, ch
		if ok, err := sc.receiveRaw(ctx); err != nil || string(ok) != loginOKMessage {
			return fmt.Errorf("phone: no LOGIN_OK: %v", err)
		}
		p.checkCode = ch.checkDigits()
	case VariantMSC4388:
		var sess msc4388Session
		endpoint, _ := joinClientPath(qr.BaseURL, msc4388Path+"/"+qr.RendezvousID)
		if err := doJSON(ctx, cli, http.MethodGet, endpoint, nil, &sess, "get"); err != nil {
			return err
		}
		rv := &msc4388Rendezvous{http: cli, baseURL: qr.BaseURL, id: qr.RendezvousID, sequenceToken: sess.SequenceToken}
		sender, initial, err := newHPKESender(qr.PublicKey, []byte(loginInitiateMessage), rv.aad())
		if err != nil {
			return err
		}
		if err = rv.send(ctx, initial); err != nil {
			return err
		}
		aad := rv.aad()
		response, err := rv.receive(ctx)
		if err != nil {
			return err
		}
		ch, ok, err := sender.finish(response, aad)
		if err != nil || string(ok) != loginOKMessage {
			return fmt.Errorf("phone: no LOGIN_OK: %v", err)
		}
		sc.rv, sc.crypto = rv, ch
		p.checkCode = ch.checkDigits()
	}
	p.ch = sc
	return nil
}

func (p *fakePhone) grant(ctx context.Context) error {
	ch := p.ch
	protocols := &channelMessage{Type: msgLoginProtocols, Protocols: []string{protocolDeviceAuthorizationGrant}}
	if ch.variant == VariantMSC4388 {
		protocols.BaseURL = p.f.srv.URL + "/"
	} else {
		protocols.Homeserver = p.f.srv.URL + "/"
	}
	if err := ch.send(ctx, protocols); err != nil {
		return err
	}
	msg, err := ch.receiveMessage(ctx)
	if err != nil {
		return err
	}
	uri := msg.VerificationURI
	if ch.variant == VariantMSC4108 && msg.DeviceAuthorizationGrant != nil {
		uri = msg.DeviceAuthorizationGrant.VerificationURI
	}
	if msg.Type != msgLoginProtocol || msg.Protocol != protocolDeviceAuthorizationGrant || msg.DeviceID == "" || uri == "" {
		return fmt.Errorf("phone: bad m.login.protocol: %+v", msg)
	}
	if err = ch.send(ctx, &channelMessage{Type: msgLoginProtocolAccepted}); err != nil {
		return err
	}
	p.f.approve <- !p.deny
	msgType, _, err := ch.receive(ctx)
	if err != nil {
		return err
	}
	if p.deny {
		if msgType != msgLoginDeclined {
			return fmt.Errorf("phone: got %s, want declined", msgType)
		}
		return nil
	}
	if msgType != msgLoginSuccess {
		return fmt.Errorf("phone: got %s, want success", msgType)
	}
	if !p.sendSecret {
		return ch.send(ctx, failureMessage(FailureDeviceNotFound))
	}
	return ch.sendRaw(ctx, `{"type":"m.login.secrets","cross_signing":{"master_key":"MSK","user_signing_key":"USK","self_signing_key":"SSK"},"backup":{"algorithm":"m.megolm_backup.v1.curve25519-aes-sha2","backup_version":"2","key":"AAAA"}}`)
}

func runLogin(t *testing.T, f *fakeServer, phone *fakePhone, typedCode func(real uint8) string) (*Result, *SecretsBundle, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	l, err := Start(ctx, Config{
		HomeserverURL: f.srv.URL,
		HTTPClient:    f.srv.Client(),
		Client:        oauth.ClientMetadata{ClientName: "Test bridge", ClientURI: "https://example.org"},
	})
	if err != nil {
		return nil, nil, err
	}
	qr, err := l.QRCode()
	if err != nil {
		return nil, nil, err
	}
	phoneErr := make(chan error, 1)
	scanned := make(chan struct{})
	go func() {
		if err := phone.scan(ctx, qr); err != nil {
			phoneErr <- err
			close(scanned)
			return
		}
		close(scanned)
		phoneErr <- phone.grant(ctx)
	}()
	if err = l.WaitForScan(ctx); err != nil {
		return nil, nil, err
	}
	<-scanned
	if err = l.ConfirmCheckCode(ctx, typedCode(phone.checkCode)); err != nil {
		return nil, nil, err
	}
	auth, err := l.RequestAuthorization(ctx)
	if err != nil {
		return nil, nil, err
	}
	if auth.UserCode != "ABCD-1234" {
		t.Errorf("user code %q", auth.UserCode)
	}
	res, err := l.WaitForApproval(ctx)
	if err != nil {
		if perr := <-phoneErr; perr != nil {
			t.Errorf("phone: %v", perr)
		}
		return nil, nil, err
	}
	secrets, err := l.ReceiveSecrets(ctx)
	if perr := <-phoneErr; perr != nil {
		t.Errorf("phone: %v", perr)
	}
	return res, secrets, err
}

func TestLoginEndToEnd(t *testing.T) {
	for _, v := range []Variant{VariantMSC4108, VariantMSC4388} {
		t.Run(v.String(), func(t *testing.T) {
			f := newFakeServer(t)
			f.advertise4388 = v == VariantMSC4388
			res, secrets, err := runLogin(t, f, &fakePhone{f: f, sendSecret: true}, func(c uint8) string { return fmt.Sprintf("%02d", c) })
			if err != nil {
				t.Fatal(err)
			}
			if res.UserID != "@alice:example.org" || res.Session.AccessToken != "AT1" || res.Session.RefreshToken != "RT1" ||
				res.Session.ClientID != "CLIENT1" || res.Session.ExpiresAt.IsZero() || len(res.DeviceID) != 10 {
				t.Fatalf("result %+v", res)
			}
			if f.clientName != "Test bridge" {
				t.Fatalf("client name %q", f.clientName)
			}
			if !strings.HasPrefix(f.scope, "urn:matrix:client:api:* urn:matrix:client:device:") {
				t.Fatalf("scope %q", f.scope)
			}
			if secrets == nil || secrets.CrossSigning.MasterKey != "MSK" || secrets.Backup == nil {
				t.Fatalf("secrets %v", secrets)
			}
			if strings.Contains(fmt.Sprintf("%v %+v %#v", secrets, res, &res.Session.Tokens), "RT1") {
				t.Fatal("a token or key reached formatted output")
			}
			if len(f.sessions) != 0 {
				t.Fatal("rendezvous session not deleted")
			}

			if err = res.Session.Refresh(context.Background(), f.srv.Client()); err != nil {
				t.Fatal(err)
			}
			if res.Session.AccessToken != "AT2" || res.Session.RefreshToken != "RT1" {
				t.Fatal("refresh did not keep the old refresh token")
			}
			res.Session.RefreshToken = "REVOKED"
			if err = res.Session.Refresh(context.Background(), f.srv.Client()); !IsOAuthError(err, OAuthInvalidGrant) {
				t.Fatalf("revoked refresh: %v", err)
			}
		})
	}
}

func TestLoginNoSecrets(t *testing.T) {
	f := newFakeServer(t)
	res, _, err := runLogin(t, f, &fakePhone{f: f}, func(c uint8) string { return strconv.Itoa(int(c)) })
	if !errors.Is(err, ErrNoSecrets) || res == nil {
		t.Fatalf("got %v, want ErrNoSecrets with a result", err)
	}
}

func TestLoginDeclined(t *testing.T) {
	f := newFakeServer(t)
	_, _, err := runLogin(t, f, &fakePhone{f: f, deny: true}, func(c uint8) string { return strconv.Itoa(int(c)) })
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("got %v, want ErrDeclined", err)
	}
}

func TestLoginWrongCheckCode(t *testing.T) {
	f := newFakeServer(t)
	_, _, err := runLogin(t, f, &fakePhone{f: f}, func(c uint8) string { return strconv.Itoa(int(c+1) % 100) })
	if !errors.Is(err, ErrInvalidCheckCode) {
		t.Fatalf("got %v, want ErrInvalidCheckCode", err)
	}
}

func TestLoginExpiredRendezvous(t *testing.T) {
	f := newFakeServer(t)
	ctx := context.Background()
	l, err := Start(ctx, Config{HomeserverURL: f.srv.URL, HTTPClient: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	clear(f.sessions)
	f.mu.Unlock()
	if err = l.WaitForScan(ctx); !errors.Is(err, ErrRendezvousExpired) {
		t.Fatalf("got %v, want ErrRendezvousExpired", err)
	}
	old, _ := l.QRCode()
	if err = l.RefreshQRCode(ctx); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := l.QRCode(); string(fresh) == string(old) {
		t.Fatal("refresh kept the QR code")
	}
}

func TestStartNotSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"versions":["v1.11"]}`)
	}))
	defer srv.Close()
	if _, err := Start(context.Background(), Config{HomeserverURL: srv.URL}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v, want ErrNotSupported", err)
	}
}
