//go:build !darwin && !freebsd && !linux

package main

import (
	"errors"
	"os"
)

// openTerminalReader is not implemented on this platform, which cannot name
// the terminal on standard input to open it again.
func openTerminalReader(*os.File) (terminalInput, error) {
	return nil, errors.New("naming the terminal on standard input is not implemented on this platform")
}
