package connector

import (
	"context"
	"errors"

	"maunium.net/go/mautrix/bridgev2"
)

const LoginFlowPhoneQR = "phone-qr"

// ErrLoginNotBuilt is returned until the QR sign-in exists (bipe#422).
var ErrLoginNotBuilt = errors.New("Entrar com o celular ainda não está pronto.")

func (c *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "Entrar com o celular",
		Description: "Escaneie um código com o app do Bipe no celular.",
		ID:          LoginFlowPhoneQR,
	}}
}

func (c *Connector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowPhoneQR {
		return nil, bridgev2.ErrInvalidLoginFlowID
	}
	return &PhoneQRLogin{user: user}, nil
}

// PhoneQRLogin signs the bridge in as a new device by MSC4108 (ADR 0128).
type PhoneQRLogin struct {
	user *bridgev2.User
}

var _ bridgev2.LoginProcess = (*PhoneQRLogin)(nil)

func (l *PhoneQRLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return nil, ErrLoginNotBuilt
}

func (l *PhoneQRLogin) Cancel() {}
