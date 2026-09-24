//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const terminalReadTermios = syscall.TCGETS

// terminalPendingRequest counts the bytes a terminal holds for reading;
// in canonical mode, the bytes of complete lines alone.
const terminalPendingRequest = syscall.TIOCINQ

// terminalName reads the path of the terminal on fd from /proc. Without
// /proc the terminal cannot be named.
func terminalName(fd int) (string, error) {
	name, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("/proc names it %q, which is not a path", name)
	}
	return name, nil
}

// flushInput discards what the terminal holds for reading: TCFLSH with
// TCIFLUSH, which Linux takes by value.
func flushInput(fd int) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), terminalFlushRequest, syscall.TCIFLUSH); errno != 0 {
		return errno
	}
	return nil
}

// selectReadable waits in select for a descriptor of set to be readable.
func selectReadable(nfd int, set *syscall.FdSet) error {
	_, err := syscall.Select(nfd, set, nil, nil, nil)
	return err
}
