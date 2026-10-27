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

// windowSize reads the rows and columns of the terminal fd refers to, with
// defaults of 24 and 80 for a size the terminal does not report.
func windowSize(fd uintptr) (rows, columns int) {
	var size struct{ Rows, Cols, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	rows, columns = 24, 80
	if errno == 0 && size.Rows > 0 {
		rows = int(size.Rows)
	}
	if errno == 0 && size.Cols > 0 {
		columns = int(size.Cols)
	}
	return rows, columns
}

// fileColumns returns the width of the terminal file writes to, read at each
// call so a resize between lines is followed, or nil when it is not one.
func fileColumns(file *os.File) func() int {
	if !isTerminalFile(file) {
		return nil
	}
	fd := file.Fd()
	return func() int {
		_, columns := windowSize(fd)
		return columns
	}
}
