package connector

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

// LoginFlowToken is the development login. It takes an access token of a new device.
// It exists only when network.dev_token_login is true, until the QR sign-in (bipe#422) is built.
const LoginFlowToken = "token"

func (c *Connector) devLoginFlows() []bridgev2.LoginFlow {
	if !c.Config.DevTokenLogin {
		return nil
	}
	return []bridgev2.LoginFlow{{
		Name:        "Token de acesso (desenvolvimento)",
		Description: "Cole o token de acesso de um aparelho novo. Só para desenvolvimento.",
		ID:          LoginFlowToken,
	}}
}

// TokenLogin signs in with a pasted access token.
type TokenLogin struct {
	main *Connector
	user *bridgev2.User
}

var _ bridgev2.LoginProcessUserInput = (*TokenLogin)(nil)

func (l *TokenLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	homeserver := l.main.Config.HomeserverURL
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "chat.bipe.beeper.token",
		Instructions: "Cole o token de acesso de um aparelho novo da conta.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{{
				Type:         bridgev2.LoginInputFieldTypeURL,
				ID:           "homeserver_url",
				Name:         "Servidor",
				DefaultValue: homeserver,
			}, {
				Type: bridgev2.LoginInputFieldTypeToken,
				ID:   "access_token",
				Name: "Token de acesso",
			}},
		},
	}, nil
}

func (l *TokenLogin) Cancel() {}

func (l *TokenLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	homeserver := strings.TrimSpace(input["homeserver_url"])
	if homeserver == "" {
		homeserver = l.main.Config.HomeserverURL
	}
	token := strings.TrimSpace(input["access_token"])
	if token == "" {
		return nil, fmt.Errorf("access token is empty")
	}
	cli, err := mautrix.NewClient(homeserver, "", token)
	if err != nil {
		return nil, err
	}
	whoami, err := cli.Whoami(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check the access token: %w", err)
	} else if whoami.DeviceID == "" {
		return nil, fmt.Errorf("the access token has no device")
	}
	pickleKey := make([]byte, 32)
	if _, err = rand.Read(pickleKey); err != nil {
		return nil, err
	}
	return completeLogin(ctx, l.user, whoami.UserID, &UserLoginMetadata{
		HomeserverURL: homeserver,
		DeviceID:      whoami.DeviceID.String(),
		AccessToken:   token,
		PickleKey:     base64.RawStdEncoding.EncodeToString(pickleKey),
	})
}

// completeLogin saves a login and connects it. Every login flow ends here.
func completeLogin(ctx context.Context, user *bridgev2.User, userID id.UserID, meta *UserLoginMetadata) (*bridgev2.LoginStep, error) {
	login, err := user.NewLogin(ctx, &database.UserLogin{
		ID:         networkid.UserLoginID(userID),
		RemoteName: userID.String(),
		Metadata:   meta,
	}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, fmt.Errorf("failed to save the login: %w", err)
	}
	go login.Client.Connect(login.Log.WithContext(context.Background()))
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "chat.bipe.beeper.complete",
		Instructions: fmt.Sprintf("Conectado como %s.", userID),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: login.ID,
			UserLogin:   login,
		},
	}, nil
}
