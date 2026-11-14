package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/rs/zerolog"

	"github.com/bipe-chat/bipe-beeper/pkg/bipe"
	"github.com/bipe-chat/bipe-beeper/pkg/bipe/mcpserver"
)

// The device entry of bipe-mcp (bipe ADR 0291, bipe-chat/bipe#1484). The
// phone lists bipe-mcp under Aparelhos e assistentes › Assistentes from
// this entry. bipe-mcp writes it on the first start after a link, and on
// each later start when a field or the drafts room changed. It knows the
// MCP client name only after the client sends initialized.

func devicePath(dir string) string { return filepath.Join(dir, "device-info.json") }

// deviceRecord is device-info.json: the last entry that bipe-mcp wrote, and
// the room it went to.
type deviceRecord struct {
	RoomID string          `json:"room_id"`
	Info   bipe.DeviceInfo `json:"info"`
}

// deviceInfo is the entry of this device: kind assistant, the app with the
// MCP client name, the system and the machine name. bipe-mcp has no
// attestation.
func deviceInfo(sess *storedSession, client string) bipe.DeviceInfo {
	info := bipe.DeviceInfo{
		DeviceID: sess.DeviceID,
		Source:   bipe.DeviceSourceSelf,
		Kind:     bipe.DeviceKindAssistant,
		Model:    machineName(),
		OS:       systemName(),
		App:      "bipe-mcp " + Version + " · " + bipe.ClientName(client),
		Attested: false,
	}
	if !sess.LinkedAt.IsZero() {
		info.LinkedAt = sess.LinkedAt.UnixMilli()
	}
	return info.Clean()
}

// machineName is the host name without the .local suffix of macOS.
func machineName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(name), ".local")
}

// systemName is the system with its version where bipe-mcp can read it,
// for example "macOS 26.1".
func systemName() string {
	switch runtime.GOOS {
	case "darwin":
		if v := macOSVersion(); v != "" {
			return "macOS " + v
		}
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}

func loadDeviceRecord(dir string) *deviceRecord {
	data, err := os.ReadFile(devicePath(dir))
	if err != nil {
		return nil
	}
	var rec deviceRecord
	if json.Unmarshal(data, &rec) != nil {
		return nil
	}
	return &rec
}

func saveDeviceRecord(dir string, rec deviceRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(devicePath(dir), data, 0o600)
}

// writeDeviceInfo writes the entry when it differs from the last one that
// bipe-mcp wrote, or when the drafts room changed. A failure only goes to
// the log: the tools work without the entry, and the next start tries again.
func writeDeviceInfo(ctx context.Context, dir string, sess *storedSession, svc *mcpserver.Service, client string, log zerolog.Logger) {
	info := deviceInfo(sess, client)
	if rec := loadDeviceRecord(dir); rec != nil && rec.Info == info {
		current, err := svc.DraftsRoomID(ctx)
		if err == nil && current != "" && string(current) == rec.RoomID {
			return
		}
	}
	roomID, err := svc.WriteDeviceInfo(ctx, info)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Warn().Str("error", errorKind(err)).Msg("Failed to write the device entry")
		}
		return
	}
	if err = saveDeviceRecord(dir, deviceRecord{RoomID: string(roomID), Info: info}); err != nil {
		log.Warn().Err(err).Msg("Failed to save the device entry record")
	}
	log.Info().Msg("Wrote the device entry")
}
