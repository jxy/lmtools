package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"net/http"
	"sync"
	"time"
)

// OpenAIStreamWriter handles Server-Sent Events writing for OpenAI format
type OpenAIStreamWriter struct {
	mu       sync.Mutex
	sse      *SSEWriter
	streamID string
	model    string
	created  int64
}

// NewOpenAIStreamWriter creates a new OpenAI SSE stream writer
func NewOpenAIStreamWriter(w http.ResponseWriter, model string, ctx context.Context) (*OpenAIStreamWriter, error) {
	sse, err := NewSSEWriter(w, ctx)
	if err != nil {
		return nil, err
	}

	return &OpenAIStreamWriter{
		sse:      sse,
		streamID: generateUUID("chatcmpl-"),
		model:    model,
		created:  time.Now().Unix(),
	}, nil
}

// WriteChunk writes a complete OpenAI streaming chunk
func (w *OpenAIStreamWriter) WriteChunk(chunk *OpenAIStreamChunk) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Ensure chunk has required fields
	if chunk.ID == "" {
		chunk.ID = w.streamID
	}
	if chunk.Object == "" {
		chunk.Object = "chat.completion.chunk"
	}
	if chunk.Created == 0 {
		chunk.Created = w.created
	}
	if chunk.Model == "" {
		chunk.Model = w.model
	}

	// Marshal and send the chunk
	data, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("failed to marshal chunk: %w", err)
	}

	return w.sse.WriteEvent("", string(data))
}

// WriteDelta writes a delta update (simplified helper)
func (w *OpenAIStreamWriter) WriteDelta(content string, role *string, finishReason *string) error {
	delta := OpenAIDelta{}

	if role != nil {
		r := core.Role(*role)
		delta.Role = &r
	}

	if content != "" {
		delta.Content = &content
	}

	chunk := &OpenAIStreamChunk{
		Choices: []OpenAIStreamDelta{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
	}

	return w.WriteChunk(chunk)
}

// WriteToolCallDelta writes a tool call delta
func (w *OpenAIStreamWriter) WriteToolCallDelta(index int, toolCall *ToolCallDelta, finishReason *string) error {
	chunk := &OpenAIStreamChunk{
		Choices: []OpenAIStreamDelta{
			{
				Index: 0,
				Delta: OpenAIDelta{
					ToolCalls: []ToolCallDelta{*toolCall},
				},
				FinishReason: finishReason,
			},
		},
	}

	return w.WriteChunk(chunk)
}

// WriteUsage writes usage information (some models send this)
func (w *OpenAIStreamWriter) WriteUsage(usage *OpenAIUsage) error {
	chunk := &OpenAIStreamChunk{
		Usage: usage,
		Choices: []OpenAIStreamDelta{
			{
				Index:        0,
				Delta:        OpenAIDelta{},
				FinishReason: nil,
			},
		},
	}

	return w.WriteChunk(chunk)
}

// WriteDone writes the final [DONE] marker
func (w *OpenAIStreamWriter) WriteDone() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.sse.WriteEvent("", "[DONE]")
}

// WriteError writes an error in OpenAI streaming format
func (w *OpenAIStreamWriter) WriteError(errType, message string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	errorData := map[string]interface{}{
		"error": map[string]interface{}{
			"type":    errType,
			"message": message,
		},
	}

	data, err := json.Marshal(errorData)
	if err != nil {
		return fmt.Errorf("failed to marshal error: %w", err)
	}

	return w.sse.WriteEvent("", string(data))
}

// Close ensures the stream is properly closed
func (w *OpenAIStreamWriter) Close() error {
	// No-op for now as the underlying ResponseWriter is managed by the HTTP server
	return nil
}

// OpenAIStreamConverter converts from various provider formats to OpenAI streaming format
type OpenAIStreamConverter struct {
	writer           *OpenAIStreamWriter
	currentToolIndex int
	toolCallsByIndex map[int]*ToolCallDelta
	accumulatedText  string
	hasStarted       bool
	ctx              context.Context
}

// NewOpenAIStreamConverter creates a new converter
func NewOpenAIStreamConverter(writer *OpenAIStreamWriter, ctx context.Context) *OpenAIStreamConverter {
	return &OpenAIStreamConverter{
		writer:           writer,
		toolCallsByIndex: make(map[int]*ToolCallDelta),
		ctx:              ctx,
	}
}

// HandleAnthropicEvent processes an Anthropic streaming event and converts to OpenAI format
func (c *OpenAIStreamConverter) HandleAnthropicEvent(eventType string, data json.RawMessage) error {
	switch eventType {
	case "message_start":
		// Send initial role delta
		if !c.hasStarted {
			role := "assistant"
			if err := c.writer.WriteDelta("", &role, nil); err != nil {
				return err
			}
			c.hasStarted = true
		}

	case "content_block_start":
		var event ContentBlockStartEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		if event.ContentBlock.Type == "tool_use" {
			// Initialize tool call
			toolCall := &ToolCallDelta{
				Index: event.Index,
				ID:    event.ContentBlock.ID,
				Type:  "function",
				Function: &FunctionCallDelta{
					Name:      event.ContentBlock.Name,
					Arguments: "",
				},
			}
			c.toolCallsByIndex[event.Index] = toolCall

			// Send initial tool call with ID and name
			if err := c.writer.WriteToolCallDelta(event.Index, toolCall, nil); err != nil {
				return err
			}
		}

	case "content_block_delta":
		var event ContentBlockDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		switch event.Delta.Type {
		case "text_delta":
			// Send text delta
			if err := c.writer.WriteDelta(event.Delta.Text, nil, nil); err != nil {
				return err
			}
			c.accumulatedText += event.Delta.Text

		case "input_json_delta":
			// Send tool arguments delta
			if toolCall, ok := c.toolCallsByIndex[event.Index]; ok {
				// Create a new tool call with just the arguments delta
				deltaCall := &ToolCallDelta{
					Index: event.Index,
					Function: &FunctionCallDelta{
						Arguments: event.Delta.PartialJSON,
					},
				}
				if err := c.writer.WriteToolCallDelta(event.Index, deltaCall, nil); err != nil {
					return err
				}
				// Accumulate arguments
				toolCall.Function.Arguments += event.Delta.PartialJSON
			}
		}

	case "message_delta":
		var event MessageDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		// Map stop reason to finish reason
		if event.Delta.StopReason != "" {
			finishReason := MapStopReasonToOpenAIFinishReason(event.Delta.StopReason)
			if err := c.writer.WriteDelta("", nil, &finishReason); err != nil {
				return err
			}
		}

		// Send usage if present
		if event.Usage != nil {
			usage := &OpenAIUsage{
				PromptTokens:     event.Usage.InputTokens,
				CompletionTokens: event.Usage.OutputTokens,
				TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
			}
			if err := c.writer.WriteUsage(usage); err != nil {
				return err
			}
		}

	case "message_stop":
		// Send [DONE]
		return c.writer.WriteDone()

	case "error":
		var event ErrorEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return c.writer.WriteError(event.Error.Type, event.Error.Message)

	case "ping":
		// OpenAI format doesn't have explicit ping events, but we can send an empty delta
		// to keep the connection alive
		if err := c.writer.WriteDelta("", nil, nil); err != nil {
			return err
		}
	}

	return nil
}

// HandleGoogleChunk processes a Google streaming chunk and converts to OpenAI format
func (c *OpenAIStreamConverter) HandleGoogleChunk(chunk map[string]interface{}) error {
	// Extract candidates
	candidates, ok := chunk["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}

	candidate := candidates[0].(map[string]interface{})

	// Check finish reason
	if finishReason, ok := candidate["finishReason"].(string); ok && finishReason != "" {
		mapped := mapGoogleFinishReason(finishReason)
		if err := c.writer.WriteDelta("", nil, &mapped); err != nil {
			return err
		}
		return c.writer.WriteDone()
	}

	// Process content
	if content, ok := candidate["content"].(map[string]interface{}); ok {
		if parts, ok := content["parts"].([]interface{}); ok {
			for _, part := range parts {
				partMap := part.(map[string]interface{})

				// Handle text
				if text, ok := partMap["text"].(string); ok && text != "" {
					if err := c.writer.WriteDelta(text, nil, nil); err != nil {
						return err
					}
				}

				// Handle function calls
				if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
					name := functionCall["name"].(string)
					args, _ := json.Marshal(functionCall["args"])

					toolCall := &ToolCallDelta{
						Index: c.currentToolIndex,
						ID:    fmt.Sprintf("call_%x", time.Now().UnixNano()),
						Type:  "function",
						Function: &FunctionCallDelta{
							Name:      name,
							Arguments: string(args),
						},
					}

					if err := c.writer.WriteToolCallDelta(c.currentToolIndex, toolCall, nil); err != nil {
						return err
					}
					c.currentToolIndex++
				}
			}
		}
	}

	// Handle usage metadata
	if usage, ok := chunk["usageMetadata"].(map[string]interface{}); ok {
		openAIUsage := &OpenAIUsage{
			PromptTokens:     int(usage["promptTokenCount"].(float64)),
			CompletionTokens: int(usage["candidatesTokenCount"].(float64)),
			TotalTokens:      int(usage["totalTokenCount"].(float64)),
		}
		if err := c.writer.WriteUsage(openAIUsage); err != nil {
			return err
		}
	}

	return nil
}

// HandleArgoText processes plain text from Argo and converts to OpenAI format
func (c *OpenAIStreamConverter) HandleArgoText(text string) error {
	// Send initial role if not started
	if !c.hasStarted {
		role := "assistant"
		if err := c.writer.WriteDelta("", &role, nil); err != nil {
			return err
		}
		c.hasStarted = true
	}

	// Send text delta
	return c.writer.WriteDelta(text, nil, nil)
}

// Complete sends the completion sequence for OpenAI format
func (c *OpenAIStreamConverter) Complete(finishReason string) error {
	// Send finish reason
	if err := c.writer.WriteDelta("", nil, &finishReason); err != nil {
		return err
	}

	// Send [DONE]
	return c.writer.WriteDone()
}
