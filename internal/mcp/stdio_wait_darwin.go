package mcp

import (
	"syscall"
	"time"
	"unsafe"
)

// waitsUnreaped reports whether waitUnreaped works on this platform.
const waitsUnreaped = true

// stoppedPollInterval is how often waitUnreaped asks again about a child
// that stopped.
const stoppedPollInterval = 50 * time.Millisecond

// waitUnreaped blocks until the child pid has exited and returns its wait
// status, leaving it unreaped: waitid with WNOWAIT. Darwin's waitid also
// returns for a child that stopped, which the os package gives as its reason
// for not using it (Go issue 19314), so a stop is waited out.
func waitUnreaped(pid int) (syscall.WaitStatus, error) {
	const pPID = 1
	// siginfo_t begins with si_signo, si_errno, si_code, si_pid, si_uid,
	// and si_status, and is 104 bytes on 64-bit systems.
	var info [32]int32
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return 0, errno
		}
		if status, exited := childStatus(info[2], info[5]); exited {
			return status, nil
		}
		time.Sleep(stoppedPollInterval)
	}
}
