//go:build !unix

package main

import "os"

// lockDir is a no-op where flock does not exist. Run one bipe-mcp per
// account there.
func lockDir(string) (bool, error) { return true, nil }

var lockFile *os.File
