//go:build linux || darwin

package mcp

import "syscall"

// si_code values of a child's siginfo_t, the same on Linux and Darwin.
const (
	cldExited = 1
	cldKilled = 2
	cldDumped = 3
)

// childStatus converts the si_code and si_status that waitid reports for a
// child to a wait status, and reports whether the child exited; the other
// codes are a stop or a continue.
func childStatus(code, status int32) (syscall.WaitStatus, bool) {
	switch code {
	case cldExited:
		return syscall.WaitStatus((status & 0xff) << 8), true
	case cldKilled:
		return syscall.WaitStatus(status & 0x7f), true
	case cldDumped:
		return syscall.WaitStatus(status&0x7f | 0x80), true
	}
	return 0, false
}
