// Package connector is the bridgev2 network connector. The remote network is a
// Matrix homeserver, so the bridge is a Matrix client on that side.
package connector

import (
	"context"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

type Connector struct {
	br       *bridgev2.Bridge
	Config   Config
	sessions sessionRegistry
}

var _ bridgev2.NetworkConnector = (*Connector)(nil)

func (c *Connector) Init(bridge *bridgev2.Bridge) {
	c.br = bridge
}

func (c *Connector) Start(ctx context.Context) error {
	return nil
}

func (c *Connector) GetName() bridgev2.BridgeName {
	return bipe.Name()
}

func (c *Connector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		UserLogin: func() any {
			return &UserLoginMetadata{}
		},
	}
}

func (c *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

func (c *Connector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 1
}

func (c *Connector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	login.Client = NewClient(c, login)
	c.sessions.start(c, login)
	return nil
}

// UserLoginMetadata is stored for each login in the bridge database on the
// user's machine. A login flow fills it. The device must be a new device of
// the remote account: the bridge uploads its own device keys. Read and write the
// token fields only through the login's oauthSession.
type UserLoginMetadata struct {
	HomeserverURL string `json:"homeserver_url"`
	DeviceID      string `json:"device_id"`
	// PickleKey encrypts the Olm account in the crypto store.
	PickleKey string `json:"pickle_key"`

	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	AccessTokenExpiry time.Time `json:"access_token_expiry,omitzero"`

	OAuthClientID      string `json:"oauth_client_id,omitempty"`
	TokenEndpoint      string `json:"oauth_token_endpoint,omitempty"`
	RevocationEndpoint string `json:"oauth_revocation_endpoint,omitempty"`
}
