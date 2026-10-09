package mcp

import (
	"lmtools/internal/processwait"
	"syscall"
)

const waitsUnreaped = processwait.Supported

func waitUnreaped(pid int) (syscall.WaitStatus, error) {
	return processwait.WaitUnreaped(pid)
}
