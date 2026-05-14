package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ParsedStreamUsage captures token counts when providers include them in stream chunks.
type ParsedStreamUsage struct {
	InputTokens  *int
	OutputTokens *int
}

// ParsedOpenAIStreamToolCall captures a single streamed OpenAI tool-call delta.
type ParsedOpenAIStreamToolCall struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
	Input     string
}

// ParsedOpenAIStreamChunk is the normalized shape of an OpenAI stream chunk.
type ParsedOpenAIStreamChunk struct {
	Usage        ParsedStreamUsage
	FinishReason string
	Content      string
	ToolCalls    []ParsedOpenAIStreamToolCall
}

// ParseOpenAIStreamErrorChunk detects OpenAI-compatible SSE error payloads.
func ParseOpenAIStreamErrorChunk(data []byte) error {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil
	}
	if len(envelope.Error) == 0 || bytes.Equal(envelope.Error, []byte("null")) {
		return nil
	}
	return newFatalStreamError(fmt.Errorf("upstream stream error: %s", formatOpenAIStreamError(envelope.Error)))
}

func formatOpenAIStreamError(raw json.RawMessage) string {
	var details struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(raw, &details); err == nil {
		message := strings.TrimSpace(details.Message)
		var fields []string
		if details.Type != "" {
			fields = append(fields, "type: "+details.Type)
		}
		if details.Code != "" {
			fields = append(fields, "code: "+details.Code)
		}
		if message != "" && len(fields) > 0 {
			return message + " (" + strings.Join(fields, ", ") + ")"
		}
		if message != "" {
			return message
		}
		if len(fields) > 0 {
			return strings.Join(fields, ", ")
		}
	}
	var message string
	if err := json.Unmarshal(raw, &message); err == nil && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, raw); err == nil && compacted.Len() > 0 {
		return compacted.String()
	}
	return "unknown upstream error"
}

// ParseOpenAIStreamChunk decodes the common fields used by both core and proxy stream parsers.
func ParseOpenAIStreamChunk(data []byte) (ParsedOpenAIStreamChunk, error) {
	if err := ParseOpenAIStreamErrorChunk(data); err != nil {
		return ParsedOpenAIStreamChunk{}, err
	}

	var raw struct {
		Usage struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
		} `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Delta        struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function *struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
					Custom *struct {
						Name  string `json:"name"`
						Input string `json:"input"`
					} `json:"custom"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ParsedOpenAIStreamChunk{}, err
	}

	parsed := ParsedOpenAIStreamChunk{
		Usage: ParsedStreamUsage{
			InputTokens:  raw.Usage.PromptTokens,
			OutputTokens: raw.Usage.CompletionTokens,
		},
	}
	if len(raw.Choices) == 0 {
		return parsed, nil
	}

	choice := raw.Choices[0]
	parsed.FinishReason = choice.FinishReason
	parsed.Content = choice.Delta.Content
	if len(choice.Delta.ToolCalls) == 0 {
		return parsed, nil
	}

	parsed.ToolCalls = make([]ParsedOpenAIStreamToolCall, 0, len(choice.Delta.ToolCalls))
	for _, toolCall := range choice.Delta.ToolCalls {
		toolType := toolCall.Type
		if toolType == "" {
			switch {
			case toolCall.Custom != nil:
				toolType = "custom"
			case toolCall.Function != nil:
				toolType = "function"
			}
		}
		name := ""
		arguments := ""
		if toolCall.Function != nil {
			name = toolCall.Function.Name
			arguments = toolCall.Function.Arguments
		}
		input := ""
		if toolType == "custom" {
			if toolCall.Custom != nil {
				name = toolCall.Custom.Name
				input = toolCall.Custom.Input
			}
		}
		parsed.ToolCalls = append(parsed.ToolCalls, ParsedOpenAIStreamToolCall{
			Index:     toolCall.Index,
			ID:        toolCall.ID,
			Type:      toolType,
			Name:      name,
			Arguments: arguments,
			Input:     input,
		})
	}

	return parsed, nil
}

// ParsedGoogleFunctionCall captures a normalized Google streamed function call.
type ParsedGoogleFunctionCall struct {
	ID               string
	Name             string
	Args             json.RawMessage
	ThoughtSignature string
}

// ParsedGoogleStreamChunk is the normalized shape of a Google stream chunk.
type ParsedGoogleStreamChunk struct {
	Usage                    ParsedStreamUsage
	FinishReason             string
	TextParts                []string
	LastTextThoughtSignature string
	FunctionCalls            []ParsedGoogleFunctionCall
}

// ParseGoogleStreamChunk decodes the common fields used by both core and proxy stream parsers.
func ParseGoogleStreamChunk(data []byte) (ParsedGoogleStreamChunk, error) {
	var raw struct {
		UsageMetadata struct {
			PromptTokenCount     *int `json:"promptTokenCount"`
			CandidatesTokenCount *int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text             string `json:"text"`
					ThoughtSignature string `json:"thoughtSignature"`
					FunctionCall     *struct {
						ID   string          `json:"id,omitempty"`
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ParsedGoogleStreamChunk{}, err
	}

	parsed := ParsedGoogleStreamChunk{
		Usage: ParsedStreamUsage{
			InputTokens:  raw.UsageMetadata.PromptTokenCount,
			OutputTokens: raw.UsageMetadata.CandidatesTokenCount,
		},
	}
	if len(raw.Candidates) == 0 {
		return parsed, nil
	}

	candidate := raw.Candidates[0]
	parsed.FinishReason = candidate.FinishReason
	if len(candidate.Content.Parts) == 0 {
		return parsed, nil
	}

	parsed.TextParts = make([]string, 0, len(candidate.Content.Parts))
	parsed.FunctionCalls = make([]ParsedGoogleFunctionCall, 0, len(candidate.Content.Parts))
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			parsed.TextParts = append(parsed.TextParts, part.Text)
		}
		if part.ThoughtSignature != "" && part.FunctionCall == nil {
			parsed.LastTextThoughtSignature = part.ThoughtSignature
		}
		if part.FunctionCall != nil {
			parsed.FunctionCalls = append(parsed.FunctionCalls, ParsedGoogleFunctionCall{
				ID:               part.FunctionCall.ID,
				Name:             part.FunctionCall.Name,
				Args:             part.FunctionCall.Args,
				ThoughtSignature: part.ThoughtSignature,
			})
		}
	}

	return parsed, nil
}
