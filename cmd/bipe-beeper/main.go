// bipe-beeper bridges a Matrix homeserver (Bipe by default) into Beeper.
package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/bipe-chat/bipe-beeper/pkg/connector"
)

// Set at build time with -ldflags -X (see build.sh).
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	m := mxmain.BridgeMain{
		Name:        "bipe-beeper",
		Description: "A Matrix-to-Matrix bridge that puts Bipe chats in Beeper.",
		URL:         "https://github.com/bipe-chat/bipe-beeper",
		Version:     "0.1.0",
		Connector:   &connector.Connector{},
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
