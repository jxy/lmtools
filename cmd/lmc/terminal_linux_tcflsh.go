//go:build linux && (386 || amd64 || arm)

package main

// terminalFlushRequest is TCFLSH, which the syscall package defines on the
// other Linux architectures and omits on these.
const terminalFlushRequest = 0x540b
