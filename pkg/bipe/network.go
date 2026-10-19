// Package bipe holds everything that is specific to Bipe. The connector core
// bridges any Matrix homeserver and takes its defaults from here.
package bipe

import "maunium.net/go/mautrix/bridgev2"

// DefaultHomeserverURL is the client-server API of the Bipe homeserver.
const DefaultHomeserverURL = "https://matrix.bipe.net.br"

// Name is the network metadata that Beeper shows for the bridge.
// NetworkIcon stays empty until the Bipe icon is uploaded as an mxc:// URI.
func Name() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "Bipe",
		NetworkURL:           "https://bipe.chat",
		NetworkID:            "bipe",
		BeeperBridgeType:     "github.com/bipe-chat/bipe-beeper",
		DefaultPort:          29350,
		DefaultCommandPrefix: "!bipe",
	}
}
