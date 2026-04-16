package core

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"os"
)

// handleAnthropicStreamWithTools handles Anthropic's SSE format with tool support
func handleAnthropicStreamWithTools(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier) (Response, error) {
	text, toolCalls, err := RunStream(ctx, body, logFile, out, notifier, &AnthropicStreamState{}, constants.ProviderAnthropic)
	return Response{Text: text, ToolCalls: toolCalls}, err
}
