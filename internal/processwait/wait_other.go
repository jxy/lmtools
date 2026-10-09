//go:build !linux && !darwin && !(freebsd && (amd64 || arm64 || riscv64))

package processwait

import (
	"errors"
	"syscall"
)

// Supported reports whether this platform can observe an exit without reaping.
const Supported = false

// WaitUnreaped is unavailable on this platform and returns errors.ErrUnsupported.
func WaitUnreaped(int) (syscall.WaitStatus, error) {
	return 0, errors.ErrUnsupported
}
