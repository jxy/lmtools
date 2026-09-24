package mcp

import (
	"syscall"
	"time"
)

// SetShutdownTimingForTest shortens the shutdown sequence of the stdio
// transports started from now on and returns a function that restores it.
func SetShutdownTimingForTest(grace, deadline time.Duration) func() {
	oldGrace, oldDeadline := shutdownGrace, shutdownDeadline
	shutdownGrace, shutdownDeadline = grace, deadline
	return func() { shutdownGrace, shutdownDeadline = oldGrace, oldDeadline }
}

// SetSignalGroupForTest replaces how the stdio transports started from now
// on signal a process group and returns a function that restores it.
func SetSignalGroupForTest(signal func(pgid int, sig syscall.Signal) error) func() {
	old := signalGroup
	signalGroup = signal
	return func() { signalGroup = old }
}

// OnWriteSlotForTest makes each stdio transport started from now on call fn
// when a write takes the write slot, before the write checks its context,
// and returns a function that stops it.
func OnWriteSlotForTest(fn func()) func() {
	afterSlotForTest = fn
	return func() { afterSlotForTest = nil }
}

// SignalWritesForTest makes each stdio transport started from now on send on
// the returned channel when a write takes the write slot, and returns a
// function that stops it.
func SignalWritesForTest() (<-chan struct{}, func()) {
	writes := make(chan struct{}, 64)
	stop := OnWriteSlotForTest(func() {
		select {
		case writes <- struct{}{}:
		default:
		}
	})
	return writes, stop
}

// HoldReapingForTest makes each stdio transport started from now on wait
// before it reaps its server, until the returned function is called.
func HoldReapingForTest() func() {
	release := make(chan struct{})
	beforeReapForTest = func() { <-release }
	return func() {
		beforeReapForTest = nil
		close(release)
	}
}
