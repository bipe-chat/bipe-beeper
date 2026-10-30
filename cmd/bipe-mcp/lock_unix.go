//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive lock on the config dir, so only one bipe-mcp
// refreshes the tokens of the session. The lock ends with the process.
func lockDir(dir string) (bool, error) {
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false, nil
	}
	// Keep f open for the life of the process.
	lockFile = f
	return true, nil
}

var lockFile *os.File
