package core

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"os"
)

// handleAnthropicStreamWithTools handles Anthropic's SSE format with tool support
func handleAnthropicStreamWithTools(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier) (Response, error) {
	state := &AnthropicStreamState{}
	text, toolCalls, err := RunStream(ctx, body, logFile, out, notifier, state, constants.ProviderAnthropic)
	blocks := state.Blocks()
	if len(blocks) == 0 {
		blocks = responseBlocksFromParts(text, toolCalls, "")
	}
	return Response{Text: text, ToolCalls: toolCalls, Blocks: blocks}, err
}
