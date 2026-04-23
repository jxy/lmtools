package core

import "encoding/json"

// ParsedStreamUsage captures token counts when providers include them in stream chunks.
type ParsedStreamUsage struct {
	InputTokens  *int
	OutputTokens *int
}

// ParsedOpenAIStreamToolCall captures a single streamed OpenAI tool-call delta.
type ParsedOpenAIStreamToolCall struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// ParsedOpenAIStreamChunk is the normalized shape of an OpenAI stream chunk.
type ParsedOpenAIStreamChunk struct {
	Usage        ParsedStreamUsage
	FinishReason string
	Content      string
	ToolCalls    []ParsedOpenAIStreamToolCall
}

// ParseOpenAIStreamChunk decodes the common fields used by both core and proxy stream parsers.
func ParseOpenAIStreamChunk(data []byte) (ParsedOpenAIStreamChunk, error) {
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
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
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
		parsed.ToolCalls = append(parsed.ToolCalls, ParsedOpenAIStreamToolCall{
			Index:     toolCall.Index,
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
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
