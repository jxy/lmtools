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
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		file.Fd(),
		uintptr(terminalReadTermios),
		uintptr(unsafe.Pointer(&termios)),
	)
	return errno == 0
}
