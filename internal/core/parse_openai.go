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
	response, err := parseOpenAIResponseDetailed(data, isEmbed)
	return response.Text, response.ToolCalls, err
}

// parseOpenAIResponseDetailed keeps Chat reasoning outside the assistant answer
// and retains the provider's reason for ending an unfinished response.
func parseOpenAIResponseDetailed(data []byte, isEmbed bool) (Response, error) {
	if isEmbed {
		// Parse embedding response
		var embedResp struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			return Response{}, fmt.Errorf("failed to unmarshal OpenAI embed response: %w", err)
		}
		if len(embedResp.Data) == 0 {
			return Response{}, fmt.Errorf("empty embedding response")
		}
		if len(embedResp.Data[0].Embedding) == 0 {
			return Response{Text: "[]", Blocks: []Block{TextBlock{Text: "[]"}}}, nil
		}
		embeddingJSON, err := json.Marshal(embedResp.Data[0].Embedding)
		if err != nil {
			return Response{}, fmt.Errorf("failed to marshal embedding: %w", err)
		}
		text := string(embeddingJSON)
		return Response{Text: text, Blocks: []Block{TextBlock{Text: text}}}, nil
	}

	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
					Custom struct {
						Name  string `json:"name"`
						Input string `json:"input"`
					} `json:"custom"`
					ExtraContent *OpenAIToolCallExtraContent `json:"extra_content"`
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
		return Response{}, fmt.Errorf("failed to decode response: %w", err)
	}

	if resp.Error != nil {
		return Response{}, fmt.Errorf("API error: %s (type: %s, code: %s)",
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("no choices in response")
	}

	msg := resp.Choices[0].Message
	var toolCalls []ToolCall

	for _, tc := range msg.ToolCalls {
		if tc.Type == "custom" {
			toolCalls = append(toolCalls, ToolCall{
				ID:    tc.ID,
				Type:  "custom",
				Name:  tc.Custom.Name,
				Args:  jsonStringRawMessage(tc.Custom.Input),
				Input: tc.Custom.Input,
			})
			continue
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:               tc.ID,
			Type:             "function",
			Name:             tc.Function.Name,
			Args:             normalizeOpenAIToolArguments(tc.Function.Arguments),
			ThoughtSignature: openAIToolCallThoughtSignature(tc.ExtraContent),
		})
	}

	var blocks []Block
	if reasoning, ok := openAIChatReasoning(msg.ReasoningContent, msg.Reasoning); ok {
		blocks = append(blocks, reasoning)
	}
	content, err := parseOpenAIChatContent(msg.Content)
	if err != nil {
		return Response{}, fmt.Errorf("failed to decode response content: %w", err)
	}
	blocks = append(blocks, content...)
	blocks = append(blocks, responseBlocksFromParts("", toolCalls, "")...)
	return Response{
		Text:             openAIChatContentText(content),
		ToolCalls:        toolCalls,
		Blocks:           blocks,
		IncompleteReason: openAIChatIncompleteReason(resp.Choices[0].FinishReason),
	}, nil
}

// Chat Completions backends return reasoning in a field beside content:
// reasoning_content, or reasoning, which newer vLLM sends. Each block keeps the
// field it came in as its type, and a replay writes the same field. Responses
// reasoning items already use the type "reasoning".
const (
	openAIChatReasoningType      = "reasoning_content"
	openAIChatReasoningFieldType = "chat_reasoning"
)

func openAIChatReasoningBlock(text string) ReasoningBlock {
	return ReasoningBlock{Provider: "openai", Type: openAIChatReasoningType, Text: text}
}

// openAIChatReasoning reads the two reasoning fields of a Chat message or
// delta. A backend that fills both sends the same text twice, so
// reasoning_content wins.
func openAIChatReasoning(reasoningContent, reasoning string) (ReasoningBlock, bool) {
	switch {
	case reasoningContent != "":
		return openAIChatReasoningBlock(reasoningContent), true
	case reasoning != "":
		return ReasoningBlock{Provider: "openai", Type: openAIChatReasoningFieldType, Text: reasoning}, true
	default:
		return ReasoningBlock{}, false
	}
}

func isOpenAIChatReasoning(block ReasoningBlock) bool {
	return block.Provider == "openai" && (block.Type == openAIChatReasoningType || block.Type == openAIChatReasoningFieldType)
}

func openAIChatIncompleteReason(finishReason string) string {
	switch finishReason {
	case "length":
		return "max_output_tokens"
	case "content_filter":
		return "content_filter"
	default:
		return ""
	}
}
