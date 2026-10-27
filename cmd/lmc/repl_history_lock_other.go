//go:build !darwin && !freebsd && !linux

package main

import "os"

// lockHistory has no lock to take here, so REPLs that write at the same time
// can interleave their lines.
func lockHistory(*os.File) (func(), error) {
	return func() {}, nil
}
