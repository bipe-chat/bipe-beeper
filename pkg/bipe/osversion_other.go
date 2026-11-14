//go:build !darwin

package bipe

// osVersion is empty on systems other than macOS: the entry names only the
// system.
func osVersion() string { return "" }
