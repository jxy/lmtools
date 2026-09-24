//go:build darwin || freebsd

package main

import (
	"syscall"
	"unsafe"
)

// terminalPendingRequest is FIONREAD, _IOR('f', 127, int), which the
// syscall package omits. It counts the bytes a terminal holds for reading;
// in canonical mode, the bytes of complete lines alone.
const terminalPendingRequest = 0x4004667f

// flushInput discards what the terminal holds for reading: TIOCFLUSH with
// FREAD, passed by pointer.
func flushInput(fd int) error {
	which := int32(1) // FREAD
	return terminalIoctl(fd, syscall.TIOCFLUSH, unsafe.Pointer(&which))
}

// selectReadable waits in select for a descriptor of set to be readable.
// The syscall package returns the error alone here.
func selectReadable(nfd int, set *syscall.FdSet) error {
	return syscall.Select(nfd, set, nil, nil, nil)
}
