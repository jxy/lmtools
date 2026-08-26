package core

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"os"
)

// handleOpenAIStreamWithTools handles OpenAI streaming responses with tool support
func handleOpenAIStreamWithTools(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier) (Response, error) {
	state := NewOpenAIStreamState()
	text, toolCalls, err := RunStream(ctx, body, logFile, out, notifier, state, constants.ProviderOpenAI)
	return Response{Text: text, ToolCalls: toolCalls, Blocks: responseBlocksFromParts(text, toolCalls, ""), Usage: state.Usage()}, err
}
