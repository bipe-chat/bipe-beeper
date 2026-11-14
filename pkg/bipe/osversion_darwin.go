package bipe

import "syscall"

// osVersion is the macOS product version, for example "15.1".
func osVersion() string {
	v, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return v
}
