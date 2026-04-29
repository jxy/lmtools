package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// StreamState defines the interface for provider-specific stream parsers
type StreamState interface {
	// ParseLine processes a single line from the stream and returns:
	// - content: any text content to display
	// - calls: any completed tool calls
	// - done: whether the stream is complete
	// - err: any parsing error
	ParseLine(line string) (content string, calls []ToolCall, done bool, err error)
}

// AnthropicStreamState tracks event type and tool accumulation for Anthropic streams
type AnthropicStreamState struct {
	currentEvent     string
	currentBlockType string
	currentBlockID   string
	currentToolName  string
	partialInput     string
}

// ParseLine implements StreamState for Anthropic SSE format
func (s *AnthropicStreamState) ParseLine(line string) (string, []ToolCall, bool, error) {
	// Handle event lines
	if strings.HasPrefix(line, "event: ") {
		s.currentEvent = strings.TrimPrefix(line, "event: ")
		return "", nil, false, nil
	}

	// Handle data lines
	if strings.HasPrefix(line, "data: ") {
		data := strings.TrimPrefix(line, "data: ")

		switch s.currentEvent {
		case "content_block_start":
			var blockStart struct {
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &blockStart); err == nil {
				s.currentBlockType = blockStart.ContentBlock.Type
				s.currentBlockID = blockStart.ContentBlock.ID
				if blockStart.ContentBlock.Type == "tool_use" {
					s.currentToolName = blockStart.ContentBlock.Name
					s.partialInput = ""
				}
			}

		case "content_block_delta":
			switch s.currentBlockType {
			case "text":
				var delta struct {
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(data), &delta); err == nil && delta.Delta.Type == "text_delta" {
					return delta.Delta.Text, nil, false, nil
				}
			case "tool_use":
				var delta struct {
					Delta struct {
						Type        string `json:"type"`
						PartialJSON string `json:"partial_json"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(data), &delta); err == nil && delta.Delta.Type == "input_json_delta" {
					s.partialInput += delta.Delta.PartialJSON
				}
			}

		case "content_block_stop":
			if s.currentBlockType == "tool_use" && s.currentToolName != "" {
				// Finalize tool call
				toolCall := ToolCall{
					ID:   s.currentBlockID,
					Name: s.currentToolName,
					Args: json.RawMessage(s.partialInput),
				}
				s.currentToolName = ""
				s.partialInput = ""
				return "", []ToolCall{toolCall}, false, nil
			}
			s.currentBlockType = ""
			s.currentBlockID = ""

		case "message_stop":
			// Stream is complete
			return "", nil, true, nil
		}
	}

	return "", nil, false, nil
}

// OpenAIStreamState tracks streaming state with tool support for OpenAI
type OpenAIStreamState struct {
	partialToolCalls map[int]*ToolCall
}

// NewOpenAIStreamState creates a new OpenAI stream state
func NewOpenAIStreamState() *OpenAIStreamState {
	return &OpenAIStreamState{
		partialToolCalls: make(map[int]*ToolCall),
	}
}

// ParseLine implements StreamState for OpenAI SSE format
func (s *OpenAIStreamState) ParseLine(line string) (string, []ToolCall, bool, error) {
	// Handle data lines
	if strings.HasPrefix(line, "data: ") {
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// Stream is complete - finalize any partial tool calls
			var toolCalls []ToolCall
			for idx, tc := range s.partialToolCalls {
				// Validate JSON arguments before adding to final tool calls
				if len(tc.Args) > 0 {
					var v interface{}
					if err := json.Unmarshal(tc.Args, &v); err != nil {
						return "", nil, false, fmt.Errorf("invalid JSON in tool call %d arguments: %w", idx, err)
					}
					// Re-marshal to ensure clean JSON
					normalized, _ := json.Marshal(v)
					tc.Args = json.RawMessage(normalized)
				}
				toolCalls = append(toolCalls, *tc)
			}
			return "", toolCalls, true, nil
		}

		parsed, err := ParseOpenAIStreamChunk([]byte(data))
		if err != nil {
			// Return error to be logged
			return "", nil, false, err
		}

		for _, tc := range parsed.ToolCalls {
			if _, exists := s.partialToolCalls[tc.Index]; !exists {
				s.partialToolCalls[tc.Index] = &ToolCall{}
			}
			partial := s.partialToolCalls[tc.Index]
			if tc.ID != "" {
				partial.ID = tc.ID
			}
			if tc.Name != "" {
				partial.Name = tc.Name
			}
			if tc.Arguments != "" {
				currentArgs := string(partial.Args)
				partial.Args = json.RawMessage(currentArgs + tc.Arguments)
			}
		}

		return parsed.Content, nil, false, nil
	}

	return "", nil, false, nil
}

// GoogleStreamState tracks current part and tool calls for Google streaming
type GoogleStreamState struct {
	nextID                   uint64
	lastTextThoughtSignature string
}

// ParseLine implements StreamState for Google SSE format
func (s *GoogleStreamState) ParseLine(line string) (string, []ToolCall, bool, error) {
	// Handle data lines
	if strings.HasPrefix(line, "data: ") {
		data := strings.TrimPrefix(line, "data: ")

		parsed, err := ParseGoogleStreamChunk([]byte(data))
		if err != nil {
			// Return error to be logged
			return "", nil, false, err
		}

		var textContent string
		var newToolCalls []ToolCall

		for _, text := range parsed.TextParts {
			textContent += text
		}
		if parsed.LastTextThoughtSignature != "" {
			s.lastTextThoughtSignature = parsed.LastTextThoughtSignature
		}
		for _, functionCall := range parsed.FunctionCalls {
			args := functionCall.Args
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			newToolCalls = append(newToolCalls, ToolCall{
				ID:               s.generateToolCallID(), // Google doesn't provide IDs
				Name:             functionCall.Name,
				Args:             args,
				ThoughtSignature: functionCall.ThoughtSignature,
			})
		}

		return textContent, newToolCalls, false, nil
	}

	return "", nil, false, nil
}

// generateToolCallID creates a unique ID for tool calls (used by Google which doesn't provide IDs)
func (s *GoogleStreamState) generateToolCallID() string {
	s.nextID++
	return fmt.Sprintf("call_%d", s.nextID)
}

// RunStream is a unified helper for running provider-specific streaming parsers
func RunStream(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier, state StreamState, providerName string) (string, []ToolCall, error) {
	return handleGenericStream(ctx, body, logFile, out, notifier, state, providerName)
}
