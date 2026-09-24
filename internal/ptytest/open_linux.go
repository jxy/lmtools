package ptytest

import (
	"fmt"
	"strconv"
	"syscall"
	"unsafe"
)

// openMaster opens /dev/ptmx, unlocks the slave, and names it from its
// number.
func openMaster() (int, string, error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", fmt.Errorf("open /dev/ptmx: %w", err)
	}
	var unlock int32
	if err := ioctl(fd, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		_ = syscall.Close(fd)
		return -1, "", fmt.Errorf("unlock the pseudo terminal: %w", err)
	}
	var unit uint32
	if err := ioctl(fd, syscall.TIOCGPTN, unsafe.Pointer(&unit)); err != nil {
		_ = syscall.Close(fd)
		return -1, "", fmt.Errorf("number the pseudo terminal: %w", err)
	}
	return fd, "/dev/pts/" + strconv.FormatUint(uint64(unit), 10), nil
}
