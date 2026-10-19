package connector

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/skip2/go-qrcode"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
	"github.com/bipe-chat/bipe-beeper/pkg/qrlogin"
)

const LoginFlowPhoneQR = "phone-qr"

const (
	stepQR        = "chat.bipe.beeper.qr"
	stepCheckCode = "chat.bipe.beeper.check_code"
	stepUserCode  = "chat.bipe.beeper.user_code"

	fieldCheckCode = "check_code"

	// maxQRRefreshes limits how often the QR code is renewed before the
	// login gives up. Synapse keeps a rendezvous session for 60 seconds.
	maxQRRefreshes = 5
	// secretsTimeout is how long the bridge waits for m.login.secrets after
	// the login. The phone sends them after it sees the new device.
	secretsTimeout = 45 * time.Second
)

func (c *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return append([]bridgev2.LoginFlow{{
		Name:        bipe.LoginFlowName,
		Description: bipe.LoginFlowDescription,
		ID:          LoginFlowPhoneQR,
	}}, c.devLoginFlows()...)
}

func (c *Connector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID == LoginFlowToken && c.Config.DevTokenLogin {
		return &TokenLogin{main: c, user: user}, nil
	}
	if flowID != LoginFlowPhoneQR {
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
	life, endLife := context.WithCancel(context.Background())
	return &PhoneQRLogin{c: c, user: user, life: life, endLife: endLife}, nil
}

// PhoneQRLogin signs the bridge in as a new device by MSC4108 (ADR 0128):
//
//  1. display_and_wait: the QR code, renewed when the rendezvous expires;
//  2. user_input: the check code that the phone shows;
//  3. display_and_wait: the MAS user code, while the phone approves;
//  4. complete.
type PhoneQRLogin struct {
	c    *Connector
	user *bridgev2.User

	// life ends when the login process is cancelled. Every step runs under
	// it, so Cancel never waits behind a long poll.
	life    context.Context
	endLife context.CancelFunc

	mu        sync.Mutex
	login     *qrlogin.Login
	scanned   bool
	refreshes int
	authed    bool
}

var (
	_ bridgev2.LoginProcessDisplayAndWait = (*PhoneQRLogin)(nil)
	_ bridgev2.LoginProcessUserInput      = (*PhoneQRLogin)(nil)
)

func loginError(msg string, cause error) error {
	return bridgev2.RespError{ErrCode: "CHAT.BIPE.LOGIN_FAILED", Err: msg, StatusCode: http.StatusBadRequest}.
		WithInternalError(errors.New(errorKind(cause)))
}

// errorKind is a short, token-free description of a login error.
func errorKind(err error) string {
	var failure *qrlogin.LoginFailureError
	var oauthErr *qrlogin.OAuthError
	switch {
	case errors.As(err, &failure):
		return string(failure.Reason)
	case errors.As(err, &oauthErr):
		return oauthErr.Code
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}

// bind cancels ctx when the login process ends.
func (l *PhoneQRLogin) bind(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.life, cancel)
	return ctx, func() { stop(); cancel() }
}

func (l *PhoneQRLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	ctx, done := l.bind(ctx)
	defer done()
	login, err := qrlogin.Start(ctx, qrlogin.Config{
		HomeserverURL: l.c.Config.HomeserverURL,
		Client:        bipe.OAuthClient(),
	})
	if errors.Is(err, qrlogin.ErrNotSupported) {
		return nil, loginError(bipe.LoginNotSupported, err)
	} else if err != nil {
		return nil, fmt.Errorf("failed to start the QR login: %w", err)
	}
	l.mu.Lock()
	l.login = login
	l.mu.Unlock()
	return l.qrStep()
}

func (l *PhoneQRLogin) qrStep() (*bridgev2.LoginStep, error) {
	data, err := l.login.QRCode()
	if err != nil {
		return nil, err
	}
	png, err := qrcode.Encode(string(data), qrcode.Low, 512)
	if err != nil {
		return nil, fmt.Errorf("failed to render the QR code: %w", err)
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       stepQR,
		Instructions: bipe.LoginScanQR,
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeQR,
			// The MSC4108 payload is binary. The bridgev2 command handler
			// renders Data as is; API clients should use the image.
			Data:     string(data),
			ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		},
	}, nil
}

func (l *PhoneQRLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	ctx, done := l.bind(ctx)
	defer done()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.login == nil {
		return nil, errors.New("login not started")
	}
	if l.authed {
		return l.waitForApproval(ctx)
	}
	err := l.login.WaitForScan(ctx)
	if errors.Is(err, qrlogin.ErrRendezvousExpired) {
		l.refreshes++
		if l.refreshes > maxQRRefreshes {
			return nil, loginError(bipe.LoginQRExpired, err)
		}
		if err = l.login.RefreshQRCode(ctx); err != nil {
			return nil, err
		}
		return l.qrStep()
	} else if err != nil {
		if errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), bridgev2.ErrLoginStepCancelled) {
			return nil, bridgev2.ErrLoginStepCancelled
		}
		return nil, fmt.Errorf("failed to open the secure channel: %w", err)
	}
	l.scanned = true
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       stepCheckCode,
		Instructions: bipe.LoginTypeCheckCode,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:        bridgev2.LoginInputFieldType2FACode,
				ID:          fieldCheckCode,
				Name:        bipe.LoginCheckCodeField,
				Description: bipe.LoginCheckCodeHelp,
				Pattern:     `^[0-9]{1,2}$`,
				MinLength:   1,
				MaxLength:   2,
			}},
		},
	}, nil
}

func (l *PhoneQRLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	ctx, done := l.bind(ctx)
	defer done()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.login == nil || !l.scanned || l.authed {
		return nil, errors.New("unexpected check code")
	}
	err := l.login.ConfirmCheckCode(ctx, input[fieldCheckCode])
	if errors.Is(err, qrlogin.ErrInvalidCheckCode) {
		return nil, loginError(bipe.LoginCheckCodeWrong, err)
	} else if err != nil {
		return nil, err
	}
	auth, err := l.login.RequestAuthorization(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to request the authorization: %w", err)
	}
	l.authed = true
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       stepUserCode,
		Instructions: bipe.LoginTypeUserCode,
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeCode,
			Data: auth.UserCode,
		},
	}, nil
}

func (l *PhoneQRLogin) waitForApproval(ctx context.Context) (*bridgev2.LoginStep, error) {
	log := zerolog.Ctx(ctx)
	res, err := l.login.WaitForApproval(ctx)
	switch {
	case errors.Is(err, qrlogin.ErrDeclined):
		return nil, loginError(bipe.LoginDeclined, err)
	case errors.Is(err, qrlogin.ErrAuthorizationExpired):
		return nil, loginError(bipe.LoginApprovalExpired, err)
	case err != nil:
		return nil, fmt.Errorf("failed to get the tokens: %w", err)
	}

	secretsCtx, cancel := context.WithTimeout(ctx, secretsTimeout)
	secrets, err := l.login.ReceiveSecrets(secretsCtx)
	cancel()
	if err != nil {
		// The login is valid without secrets. The device stays unverified.
		log.Warn().Str("reason", errorKind(err)).Msg("No E2EE secrets from the phone")
		secrets = nil
	}

	l.setDeviceName(ctx, res)

	meta := &UserLoginMetadata{
		HomeserverURL:      res.HomeserverURL,
		DeviceID:           string(res.DeviceID),
		PickleKey:          newPickleKey(),
		OAuthClientID:      res.Session.ClientID,
		TokenEndpoint:      res.Session.TokenEndpoint,
		RevocationEndpoint: res.Session.RevocationEndpoint,
	}
	meta.setTokens(res.Session.Tokens)
	l.login = nil
	return completeLogin(ctx, l.user, res.UserID, meta, func(ctx context.Context, ul *bridgev2.UserLogin) {
		l.c.afterQRLogin(ctx, ul, secrets)
	})
}

// setDeviceName names the new device "Beeper", so Linked devices on the phone
// shows it. A failure does not stop the login.
func (l *PhoneQRLogin) setDeviceName(ctx context.Context, res *qrlogin.Result) {
	cli, err := mautrix.NewClient(res.HomeserverURL, res.UserID, res.Session.AccessToken)
	if err != nil {
		return
	}
	cli.Log = *zerolog.Ctx(ctx)
	err = cli.SetDeviceInfo(ctx, res.DeviceID, &mautrix.ReqDeviceInfo{DisplayName: bipe.DeviceDisplayName})
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to set the device display name")
	}
}

func (l *PhoneQRLogin) Cancel() {
	l.endLife()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.login != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		l.login.Cancel(ctx)
		l.login = nil
	}
}

func newPickleKey() string {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return base64.RawStdEncoding.EncodeToString(key)
}
