//go:build darwin

package bipe

import "golang.org/x/sys/unix"

// macOSVersion is the product version, for example "26.1", or "".
func macOSVersion() string {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return v
}
