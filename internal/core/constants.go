package core

import "time"

const (
	// Tool execution limits
	DefaultMaxOutputSize   = 1024 * 1024      // 1MB per tool output
	DefaultMaxToolRounds   = 32               // Increased from 10, prevents infinite tool loops
	DefaultToolTimeout     = 30 * time.Second // Default timeout for tool execution
	DefaultMaxToolParallel = 4                // Default maximum parallel tool executions
	DefaultMaxToolCalls    = 64               // Maximum tool calls accepted in a single round

	// Session limits
	MaxRetries         = 10              // Max retries for session operations
	SessionLockTimeout = 5 * time.Second // Timeout for session lock acquisition
)
