package bipe

import (
	"os"
	"runtime"
	"strings"

	"maunium.net/go/mautrix/id"
)

// BridgeDeviceInfo is the device entry of the bridge (bipe ADR 0291,
// bipe-chat/bipe#1485): kind bridge, app "Beeper bridge <version>", the
// system, and the host name of the machine that runs the bridge. The bridge
// has no attestation, so attested is false.
func BridgeDeviceInfo(deviceID id.DeviceID, version string, linkedAt int64) DeviceInfo {
	return DeviceInfo{
		DeviceID: deviceID,
		Kind:     DeviceKindBridge,
		App:      strings.TrimSpace("Beeper bridge " + version),
		OS:       systemName(),
		Model:    machineName(),
		LinkedAt: linkedAt,
		Attested: false,
	}
}

// systemName is the operating system, with its version when the system
// gives it.
func systemName() string {
	name := map[string]string{"darwin": "macOS", "windows": "Windows", "linux": "Linux"}[runtime.GOOS]
	if name == "" {
		name = runtime.GOOS
	}
	if v := osVersion(); v != "" {
		name += " " + v
	}
	return name
}

// machineName is the host name without the ".local" suffix of macOS.
func machineName() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(host, ".local")
}
