// Package connector is the bridgev2 network connector. The remote network is a
// Matrix homeserver, so the bridge is a Matrix client on that side.
package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
)

type Connector struct {
	br     *bridgev2.Bridge
	Config Config
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
	login.Client = &Client{login: login}
	return nil
}

// UserLoginMetadata is stored for each login. The QR sign-in (bipe#422) fills it.
type UserLoginMetadata struct {
	HomeserverURL string `json:"homeserver_url"`
	DeviceID      string `json:"device_id"`
}
