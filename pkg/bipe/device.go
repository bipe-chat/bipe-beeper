package bipe

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// Device entries (bipe ADR 0291, bipe-chat/bipe#1479). device-kinds.json is
// a copy of bipe-shared registries/device-kinds.json; TestCopiesMatchBipeShared
// checks it. Spec: bipe-shared docs/drafts-room.md, "Device entries". The
// reader rule is deviceInfoFromEvent in bipe-shared tools/lib/registries.mjs.

//go:embed device-kinds.json
var deviceKindsJSON []byte

// DeviceKinds is the part of registries/device-kinds.json that a writer
// needs.
type DeviceKinds struct {
	Version        int    `json:"version"`
	Field          string `json:"field"`
	Msgtype        string `json:"msgtype"`
	FieldMaxLength int    `json:"fieldMaxLength"`
}

// Device kinds and sources that bipe-beeper writes.
const (
	DeviceKindAssistant = "assistant"
	DeviceKindBridge    = "bridge"
	DeviceSourceSelf    = "self"
)

var deviceKinds DeviceKinds

func init() {
	if err := json.Unmarshal(deviceKindsJSON, &deviceKinds); err != nil {
		panic(fmt.Errorf("bipe: invalid device-kinds.json: %w", err))
	}
	if deviceKinds.Field == "" || deviceKinds.Msgtype == "" || deviceKinds.FieldMaxLength <= 0 {
		panic("bipe: device-kinds.json has no field, msgtype or fieldMaxLength")
	}
}

// DeviceKindsRegistry returns the device kinds registry.
func DeviceKindsRegistry() DeviceKinds { return deviceKinds }

// DeviceText trims s and cuts it to fieldMaxLength UTF-16 code units, so the
// reader rule accepts it. A blank s gives "": leave that field out.
func DeviceText(s string) string { return cutUTF16(s, deviceKinds.FieldMaxLength) }

// DeviceInfo is one device entry. Blank text fields and a zero LinkedAt are
// left out of the event.
type DeviceInfo struct {
	DeviceID string `json:"device_id"`
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Model    string `json:"model,omitempty"`
	OS       string `json:"os,omitempty"`
	App      string `json:"app,omitempty"`
	LinkedAt int64  `json:"linked_at,omitempty"`
	LinkedBy string `json:"linked_by,omitempty"`
	Attested bool   `json:"attested"`
}

// Clean returns d with every text field passed through DeviceText.
func (d DeviceInfo) Clean() DeviceInfo {
	d.Model = DeviceText(d.Model)
	d.OS = DeviceText(d.OS)
	d.App = DeviceText(d.App)
	d.LinkedBy = DeviceText(d.LinkedBy)
	if d.LinkedAt < 0 {
		d.LinkedAt = 0
	}
	return d
}

// DeviceInfoContent makes the m.room.message content of a device entry: an
// m.notice whose body other Matrix apps show, and the device_info field.
func DeviceInfoContent(d DeviceInfo) map[string]any {
	d = d.Clean()
	field := map[string]any{
		"device_id": d.DeviceID,
		"source":    d.Source,
		"kind":      d.Kind,
		"attested":  d.Attested,
	}
	for k, v := range map[string]string{"model": d.Model, "os": d.OS, "app": d.App, "linked_by": d.LinkedBy} {
		if v != "" {
			field[k] = v
		}
	}
	if d.LinkedAt > 0 {
		field["linked_at"] = d.LinkedAt
	}
	var parts []string
	for _, v := range []string{d.App, d.Model, d.OS} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	body := strings.Join(parts, " · ")
	if body == "" {
		body = d.Kind
	}
	return map[string]any{
		"msgtype":         deviceKinds.Msgtype,
		"body":            body,
		deviceKinds.Field: field,
	}
}
