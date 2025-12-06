package core

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"os"
)

// NOTE: Tool support for Google provider:
// - Direct Google provider (using Google API directly): SUPPORTS tools (including streaming)
// - Google models via Argo provider: DOES NOT support tools (current limitation)
// This file implements streaming tool support for direct Google provider usage.

// handleGoogleStreamWithTools handles Google's SSE format with tool support
func handleGoogleStreamWithTools(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier) (string, []ToolCall, error) {
	return RunStream(ctx, body, logFile, out, notifier, &GoogleStreamState{}, constants.ProviderGoogle)
}
