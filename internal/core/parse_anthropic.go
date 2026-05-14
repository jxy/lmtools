package core

import (
	"encoding/json"
	"fmt"
)

// parseAnthropicResponseWithTools parses Anthropic responses that may contain tool calls
func parseAnthropicResponseWithTools(data []byte, isEmbed bool) (string, []ToolCall, error) {
	text, toolCalls, _, err := parseAnthropicResponseDetailed(data, isEmbed)
	return text, toolCalls, err
}

func parseAnthropicResponseDetailed(data []byte, isEmbed bool) (string, []ToolCall, []Block, error) {
	if isEmbed {
		// Anthropic doesn't support embeddings
		return "", nil, nil, fmt.Errorf("anthropic provider does not support embedding mode")
	}

	var resp struct {
		Content []json.RawMessage `json:"content"`
		Error   *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return "", nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if resp.Error != nil {
		return "", nil, nil, fmt.Errorf("API error: %s (type: %s)", resp.Error.Message, resp.Error.Type)
	}

	var text string
	var toolCalls []ToolCall
	var blocks []Block

	for _, rawContent := range resp.Content {
		var content struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(rawContent, &content); err != nil {
			return "", nil, nil, err
		}
		switch content.Type {
		case "text":
			text += content.Text
			blocks = append(blocks, TextBlock{Text: content.Text})
		case "thinking", "redacted_thinking":
			blocks = append(blocks, ReasoningBlock{
				Provider:  "anthropic",
				Type:      content.Type,
				Text:      content.Thinking,
				Signature: content.Signature,
				Raw:       append(json.RawMessage(nil), rawContent...),
			})
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:   content.ID,
				Type: "function",
				Name: content.Name,
				Args: content.Input,
			})
			blocks = append(blocks, ToolUseBlock{
				ID:    content.ID,
				Type:  "function",
				Name:  content.Name,
				Input: content.Input,
			})
		}
	}

	return text, toolCalls, blocks, nil
}
