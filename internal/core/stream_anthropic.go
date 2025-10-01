package core

import (
	"context"
	"io"
	"os"
)

// handleAnthropicStreamWithTools handles Anthropic's SSE format with tool support
func handleAnthropicStreamWithTools(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier) (string, []ToolCall, error) {
	return RunStream(ctx, body, logFile, out, notifier, &AnthropicStreamState{}, "anthropic")
}
