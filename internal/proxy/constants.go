package proxy

import (
	"bufio"
	"io"
)

// SSE (Server-Sent Events) configuration constants
const (
	// SSEMaxLineBytes is the maximum size of a single SSE line (1MB)
	// This limit was chosen to accommodate large model responses that may include
	// substantial amounts of generated text or JSON data in a single SSE event.
	// Most SSE lines are much smaller, but some providers (especially when streaming
	// tool calls or complex structured outputs) can produce large individual events.
	SSEMaxLineBytes = 1024 * 1024

	// SSEBufferSize is the initial buffer size for SSE scanning (64KB)
	// This provides a good balance between memory usage and performance.
	// The buffer will automatically grow up to SSEMaxLineBytes if needed.
	// 64KB is sufficient for most SSE events while avoiding excessive allocations.
	SSEBufferSize = 64 * 1024
)

// NewSSEScanner creates a new scanner configured for SSE parsing
// with appropriate buffer sizes to handle large SSE lines.
//
// IMPORTANT: This scanner should be used everywhere SSE streams are parsed
// to ensure consistent behavior and proper handling of large events.
// The scanner is configured with:
//   - Initial buffer of SSEBufferSize (64KB)
//   - Maximum line size of SSEMaxLineBytes (1MB)
//
// Usage example:
//
//	scanner := NewSSEScanner(response.Body)
//	for scanner.Scan() {
//	    line := scanner.Text()
//	    // Process SSE line
//	}
func NewSSEScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	// Increase buffer to handle large SSE lines
	buf := make([]byte, SSEBufferSize)
	scanner.Buffer(buf, SSEMaxLineBytes)
	return scanner
}
