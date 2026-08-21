package core

import "time"

const (
	// Tool execution limits
	DefaultMaxOutputSize   = 1024 * 1024      // 1MB per tool output
	DefaultMaxToolRounds   = 64               // Tool-call rounds per block before confirmation
	DefaultToolTimeout     = 60 * time.Second // Default timeout for one command
	DefaultMaxToolParallel = 8                // Default maximum parallel command executions
	DefaultMaxToolCalls    = 64               // Maximum tool calls accepted in a single round

	// MaxToolOutputBytes is the ceiling on -tool-max-output-bytes.
	// GetToolMaxOutputBytes clamps to it and flag validation rejects above it;
	// both sides read this constant so the limit enforced is the limit reported.
	MaxToolOutputBytes = 100 * 1024 * 1024

	// MaxCommandTimeoutSeconds bounds the timeout a model may ask for. It is a
	// count of seconds rather than a Duration because the bound has to apply
	// ahead of the multiply that turns one into the other: time.Duration is an
	// int64 of nanoseconds, so a larger number wraps and the deadline lands in
	// the past. A day is far past any plausible tool call and well short of
	// where the conversion stops working.
	MaxCommandTimeoutSeconds = 24 * 60 * 60

	// CommandWaitDelay bounds how long Cmd.Wait keeps reading the captured
	// output pipe after the process has exited or the deadline has passed. A
	// descendant that inherited the pipe holds it open indefinitely otherwise,
	// past both the command timeout and the worker slot it occupies.
	CommandWaitDelay = 2 * time.Second

	// Session limits
	MaxRetries         = 10              // Max retries for session operations
	SessionLockTimeout = 5 * time.Second // Timeout for session lock acquisition
)
