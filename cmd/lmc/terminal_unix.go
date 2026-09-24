//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func isTerminalFile(file *os.File) bool {
	if file == nil {
		return false
	}
	return isTerminalFd(file.Fd())
}

// isTerminalFd reports whether fd is a terminal: whether it answers a read
// of its settings.
func isTerminalFd(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(terminalReadTermios),
		uintptr(unsafe.Pointer(&termios)),
	)
	return errno == 0
}
