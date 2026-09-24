//go:build amd64 || arm64 || riscv64

package mcp

import (
	"syscall"
	"unsafe"
)

// waitsUnreaped reports whether waitUnreaped works on this platform.
const waitsUnreaped = true

// waitUnreaped blocks until the child pid has exited and returns its wait
// status, leaving it unreaped: wait6 with WNOWAIT, the wait the os package
// makes before it reaps. On 64-bit systems the process ID fits the one
// register the call gives it.
func waitUnreaped(pid int) (syscall.WaitStatus, error) {
	const pPID = 0
	var status int32
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAIT6, pPID, uintptr(pid), uintptr(unsafe.Pointer(&status)), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return 0, errno
		}
		return syscall.WaitStatus(status), nil
	}
}
