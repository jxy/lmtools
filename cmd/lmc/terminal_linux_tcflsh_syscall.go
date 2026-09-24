//go:build linux && !(386 || amd64 || arm)

package main

import "syscall"

// terminalFlushRequest is TCFLSH, whose value differs by architecture.
const terminalFlushRequest = syscall.TCFLSH
