package core

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"os"
)

// NOTE: Tool support for Google provider:
// - Direct Google provider (using Google API directly): SUPPORTS tools (including streaming)
// - Google models via Argo provider: SUPPORT Google-format tool streaming when routed that way
// This file implements Google-format streaming tool support.

// handleGoogleStreamWithTools handles Google's SSE format with tool support
func handleGoogleStreamWithTools(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier) (Response, error) {
	state := &GoogleStreamState{}
	text, toolCalls, err := RunStream(ctx, body, logFile, out, notifier, state, constants.ProviderGoogle)
	blocks := responseBlocksFromParts(text, toolCalls, state.lastTextThoughtSignature)
	return Response{
		Text:             text,
		ToolCalls:        toolCalls,
		Blocks:           blocks,
		ThoughtSignature: state.lastTextThoughtSignature,
	}, err
}

func responseBlocksFromParts(text string, toolCalls []ToolCall, thoughtSignature string) []Block {
	blocks := make([]Block, 0, 1+len(toolCalls))
	if thoughtSignature != "" {
		blocks = append(blocks, ReasoningBlock{
			Provider:  "google",
			Type:      "thought_signature",
			Signature: thoughtSignature,
		})
	}
	if text != "" {
		blocks = append(blocks, TextBlock{Text: text})
	}
	for _, call := range toolCalls {
		if call.ThoughtSignature != "" {
			blocks = append(blocks, ReasoningBlock{
				Provider:  "google",
				Type:      "thought_signature",
				Signature: call.ThoughtSignature,
			})
		}
		blocks = append(blocks, ToolUseBlock{
			ID:           call.ID,
			Type:         call.Type,
			Namespace:    call.Namespace,
			OriginalName: call.OriginalName,
			Name:         call.Name,
			Input:        append([]byte(nil), call.Args...),
			InputString:  call.Input,
		})
	}
	return blocks
}
