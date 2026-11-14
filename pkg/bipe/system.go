package bipe

import (
	"os"
	"runtime"
	"strings"
)

// MachineName is the host name without the .local suffix of macOS. Device
// entries name the machine with it.
func MachineName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(name), ".local")
}

// SystemName is the system with its version where it can be read, for
// example "macOS 26.1".
func SystemName() string {
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
