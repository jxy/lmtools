//go:build darwin || freebsd || linux

package main

import (
	"os"
	"syscall"
)

// lockHistory holds an exclusive lock on the history file until the returned
// function releases it; other REPLs wait for it.
func lockHistory(file *os.File) (func(), error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }, nil
}
