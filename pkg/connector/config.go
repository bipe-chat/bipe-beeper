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
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "homeserver_url")
}

func (c *Connector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &c.Config, &up.StructUpgrader{
		SimpleUpgrader: upgradeConfig,
		Base:           ExampleConfig,
	}
}
