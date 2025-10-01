package core

import (
	"encoding/json"
	"fmt"
)

// parseAnthropicResponseWithTools parses Anthropic responses that may contain tool calls
func parseAnthropicResponseWithTools(data []byte, isEmbed bool) (string, []ToolCall, error) {
	if isEmbed {
		// Anthropic doesn't support embeddings
		return "", nil, fmt.Errorf("anthropic provider does not support embedding mode")
	}

	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return "", nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if resp.Error != nil {
		return "", nil, fmt.Errorf("API error: %s (type: %s)", resp.Error.Message, resp.Error.Type)
	}

	var text string
	var toolCalls []ToolCall

	for _, content := range resp.Content {
		switch content.Type {
		case "text":
			text += content.Text
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:   content.ID,
				Name: content.Name,
				Args: content.Input,
			})
		}
	}

	return text, toolCalls, nil
}
