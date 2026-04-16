package core

import (
	"encoding/json"
	"fmt"
)

// ParseResponseProjection parses a provider response and returns the canonical
// parsed fixture projection used by the shared API fixture tests.
func ParseResponseProjection(provider string, data []byte) (map[string]interface{}, error) {
	var (
		text      string
		toolCalls []ToolCall
		err       error
	)

	switch provider {
	case "openai":
		text, toolCalls, err = parseOpenAIResponseWithTools(data, false)
	case "anthropic":
		text, toolCalls, err = parseAnthropicResponseWithTools(data, false)
	case "google":
		text, toolCalls, err = parseGoogleResponseWithTools(data, false)
	case "argo":
		text, toolCalls, err = parseArgoResponseWithTools(data, false)
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
	if err != nil {
		return nil, err
	}

	return projectParsedResponse(provider, text, toolCalls), nil
}

func projectParsedResponse(provider, text string, toolCalls []ToolCall) map[string]interface{} {
	projected := map[string]interface{}{
		"text": text,
	}

	if len(toolCalls) == 0 {
		projected["tool_calls"] = []interface{}{}
		return projected
	}

	items := make([]map[string]interface{}, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		item := map[string]interface{}{
			"name": toolCall.Name,
		}
		if provider != "google" && toolCall.ID != "" {
			item["id"] = toolCall.ID
		}
		if toolCall.AssistantContent != "" {
			item["assistant_content"] = toolCall.AssistantContent
		}
		if len(toolCall.Args) > 0 {
			var decoded interface{}
			if err := json.Unmarshal(toolCall.Args, &decoded); err == nil {
				item["args"] = decoded
			} else {
				item["args"] = string(toolCall.Args)
			}
		}
		items = append(items, item)
	}
	projected["tool_calls"] = items
	return projected
}
