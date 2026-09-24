package mcp

import (
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// waitsUnreaped reports whether waitUnreaped works on this platform.
const waitsUnreaped = true

// waitUnreaped blocks until the child pid has exited and returns its wait
// status, leaving it unreaped: waitid with WNOWAIT, the wait the os package
// makes before it reaps.
func waitUnreaped(pid int) (syscall.WaitStatus, error) {
	const pPID = 1
	// siginfo_t is 128 bytes: si_signo, si_errno, and si_code, which MIPS
	// puts before si_errno, and then for a child, after padding to eight
	// bytes on 64-bit systems, si_pid, si_uid, and si_status.
	var info [32]int32
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return 0, errno
		}
		break
	}
	code := info[2]
	if strings.HasPrefix(runtime.GOARCH, "mips") {
		code = info[1]
	}
	status, _ := childStatus(code, info[5+unsafe.Sizeof(uintptr(0))/8])
	return status, nil
}
