package bipe

import "strings"

// BridgeDeviceInfo is the device entry of the Beeper bridge (bipe ADR 0291,
// bipe-chat/bipe#1485): kind bridge, app "Beeper bridge <version>", the
// system and the machine that run the bridge. The bridge has no
// attestation, so attested is false. linkedAt is in milliseconds; 0 leaves
// it out.
func BridgeDeviceInfo(deviceID, version string, linkedAt int64) DeviceInfo {
	return DeviceInfo{
		DeviceID: deviceID,
		Source:   DeviceSourceSelf,
		Kind:     DeviceKindBridge,
		App:      strings.TrimSpace("Beeper bridge " + version),
		OS:       SystemName(),
		Model:    MachineName(),
		LinkedAt: linkedAt,
		Attested: false,
	}.Clean()
}
