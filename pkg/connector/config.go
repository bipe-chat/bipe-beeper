package connector

import (
	_ "embed"

	up "go.mau.fi/util/configupgrade"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	// HomeserverURL is the client-server API of the remote Matrix homeserver.
	HomeserverURL string `yaml:"homeserver_url"`
	// DevTokenLogin enables the "token" login flow, which takes a pasted access token.
	// It is for development only, until the QR sign-in (bipe#422) exists.
	DevTokenLogin bool `yaml:"dev_token_login"`
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "homeserver_url")
	helper.Copy(up.Bool, "dev_token_login")
}

func (c *Connector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &c.Config, &up.StructUpgrader{
		SimpleUpgrader: upgradeConfig,
		Base:           ExampleConfig,
	}
}
