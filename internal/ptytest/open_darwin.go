package ptytest

import (
	"bytes"
	"fmt"
	"syscall"
	"unsafe"
)

// openMaster opens /dev/ptmx and does what grantpt, unlockpt, and ptsname
// do, through their ioctls.
func openMaster() (int, string, error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", fmt.Errorf("open /dev/ptmx: %w", err)
	}
	fail := func(step string, err error) (int, string, error) {
		_ = syscall.Close(fd)
		return -1, "", fmt.Errorf("%s: %w", step, err)
	}
	if err := ioctlValue(fd, syscall.TIOCPTYGRANT, 0); err != nil {
		return fail("grant the pseudo terminal", err)
	}
	if err := ioctlValue(fd, syscall.TIOCPTYUNLK, 0); err != nil {
		return fail("unlock the pseudo terminal", err)
	}
	// TIOCPTYGNAME fills a buffer of 128 bytes.
	var name [128]byte
	if err := ioctl(fd, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		return fail("name the pseudo terminal", err)
	}
	end := bytes.IndexByte(name[:], 0)
	if end < 0 {
		end = len(name)
	}
	return fd, string(name[:end]), nil
}

// ioctlValue calls ioctl with an argument passed by value.
func ioctlValue(fd int, request, value uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, value); errno != 0 {
		return errno
	}
	return nil
}
