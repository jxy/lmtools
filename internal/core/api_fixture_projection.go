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
	case "openai-responses":
		return parseOpenAIResponsesFixtureProjection(data)
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

func parseOpenAIResponsesFixtureProjection(data []byte) (map[string]interface{}, error) {
	var envelope struct {
		Status            string          `json:"status"`
		Error             json.RawMessage `json:"error"`
		IncompleteDetails json.RawMessage `json:"incomplete_details"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}

	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		projected := projectParsedResponse("openai-responses", "", nil)
		if envelope.Status != "" {
			projected["status"] = envelope.Status
		}
		projected["error"] = rawFixtureJSON(envelope.Error)
		if len(envelope.IncompleteDetails) > 0 && string(envelope.IncompleteDetails) != "null" {
			projected["incomplete_details"] = rawFixtureJSON(envelope.IncompleteDetails)
		}
		return projected, nil
	}

	text, toolCalls, err := parseOpenAIResponsesWithTools(data)
	if err != nil {
		return nil, err
	}
	projected := projectParsedResponse("openai-responses", text, toolCalls)
	if envelope.Status != "" && envelope.Status != "completed" {
		projected["status"] = envelope.Status
	}
	if len(envelope.IncompleteDetails) > 0 && string(envelope.IncompleteDetails) != "null" {
		projected["incomplete_details"] = rawFixtureJSON(envelope.IncompleteDetails)
	}
	return projected, nil
}

func rawFixtureJSON(raw json.RawMessage) interface{} {
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err == nil {
		return decoded
	}
	return string(raw)
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
