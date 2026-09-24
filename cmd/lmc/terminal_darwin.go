package main

import (
	"bytes"
	"errors"
	"syscall"
	"unsafe"
)

// terminalName asks fcntl for the path of the terminal on fd.
func terminalName(fd int) (string, error) {
	var path [1024]byte // MAXPATHLEN
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETPATH, uintptr(unsafe.Pointer(&path[0]))); errno != 0 {
		return "", errno
	}
	end := bytes.IndexByte(path[:], 0)
	if end <= 0 {
		return "", errors.New("fcntl returned no path")
	}
	return string(path[:end]), nil
}
