package ptytest

import (
	"fmt"
	"strconv"
	"syscall"
	"unsafe"
)

// openMaster calls posix_openpt, which the syscall package numbers but does
// not wrap, and names the slave from its unit. FreeBSD needs neither grantpt
// nor unlockpt.
func openMaster() (int, string, error) {
	r, _, errno := syscall.Syscall(syscall.SYS_POSIX_OPENPT, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0, 0)
	if errno != 0 {
		return -1, "", fmt.Errorf("posix_openpt: %w", errno)
	}
	fd := int(r)
	var unit uint32
	if err := ioctl(fd, syscall.TIOCGPTN, unsafe.Pointer(&unit)); err != nil {
		_ = syscall.Close(fd)
		return -1, "", fmt.Errorf("number the pseudo terminal: %w", err)
	}
	return fd, "/dev/pts/" + strconv.FormatUint(uint64(unit), 10), nil
}
