package core

import (
	"strings"
)

// buildContentAndToolsFromEmbedded extracts content string and tool calls from embedded sequences.
// Returns the reconstructed content (prefixes + suffix) and extracted tool calls.
func buildContentAndToolsFromEmbedded(seq []EmbeddedSequence, suffix string) (string, []ToolCall) {
	var content strings.Builder
	var toolCalls []ToolCall

	// Reconstruct assistant visible content by concatenating prefixes and suffix
	for _, part := range seq {
		if part.Prefix != "" {
			content.WriteString(part.Prefix)
		}
	}
	if suffix != "" {
		content.WriteString(suffix)
	}

	// Build tool calls from embedded calls
	for _, part := range seq {
		if part.Call != nil {
			toolCall := ToolCall{
				ID:               part.Call.ID,
				Name:             part.Call.Name,
				Args:             part.Call.ArgsJSON,
				AssistantContent: part.Prefix, // Store the prefix as context for this tool call
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return strings.TrimSpace(content.String()), toolCalls
}
