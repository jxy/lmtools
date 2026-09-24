//go:build !linux && !darwin && !(freebsd && (amd64 || arm64 || riscv64))

package mcp

import (
	"errors"
	"syscall"
)

// waitsUnreaped reports whether waitUnreaped works on this platform.
const waitsUnreaped = false

// waitUnreaped is not implemented here, so a stdio server is reaped when it
// exits, and shutdown signals the server alone.
func waitUnreaped(int) (syscall.WaitStatus, error) {
	return 0, errors.ErrUnsupported
}
