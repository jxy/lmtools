package core

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"os"
)

// handleOpenAIStreamWithTools handles OpenAI streaming responses with tool support
func handleOpenAIStreamWithTools(ctx context.Context, body io.ReadCloser, logFile *os.File, output ResponseOutput, notifier Notifier) (Response, error) {
	output = responseOutputOrDefault(output, os.Stdout)
	state := NewOpenAIStreamState()
	text, toolCalls, err := RunStream(ctx, body, logFile, output, notifier, state, constants.ProviderOpenAI)
	if err == nil {
		var finalCalls []ToolCall
		finalCalls, err = state.finishStream()
		toolCalls = append(toolCalls, finalCalls...)
	} else {
		state.closeReasoning(true)
	}
	emitPendingResponseStreamEvents(state, output)
	return Response{
		Text: text, ToolCalls: toolCalls, Blocks: state.Blocks(), Usage: state.Usage(),
		IncompleteReason: state.IncompleteReason(),
	}, err
}
