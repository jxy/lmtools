package core

import (
	"encoding/json"
	"fmt"
)

func normalizeOpenAIToolArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		if json.Valid([]byte(encoded)) {
			return json.RawMessage(encoded)
		}
	}

	return raw
}

// parseOpenAIResponseWithTools parses OpenAI responses that may contain tool calls
func parseOpenAIResponseWithTools(data []byte, isEmbed bool) (string, []ToolCall, error) {
	if isEmbed {
		// Parse embedding response
		var embedResp struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			return "", nil, fmt.Errorf("failed to unmarshal OpenAI embed response: %w", err)
		}
		if len(embedResp.Data) == 0 {
			return "", nil, fmt.Errorf("empty embedding response")
		}
		if len(embedResp.Data[0].Embedding) == 0 {
			return "[]", nil, nil
		}
		embeddingJSON, err := json.Marshal(embedResp.Data[0].Embedding)
		if err != nil {
			return "", nil, fmt.Errorf("failed to marshal embedding: %w", err)
		}
		return string(embeddingJSON), nil, nil
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return "", nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if resp.Error != nil {
		return "", nil, fmt.Errorf("API error: %s (type: %s, code: %s)",
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return "", nil, fmt.Errorf("no choices in response")
	}

	msg := resp.Choices[0].Message
	var toolCalls []ToolCall

	for _, tc := range msg.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: normalizeOpenAIToolArguments(tc.Function.Arguments),
		})
	}

	return msg.Content, toolCalls, nil
}
