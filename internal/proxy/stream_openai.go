// Package proxy provides OpenAI streaming functionality.
//
// Usage Streaming Rules for OpenAI Format:
//
// The OpenAI streaming format handles usage information as follows:
//
// 1. Usage is ONLY included when constants.IncludeUsageKey is true in the request
// 2. Usage appears in a separate chunk AFTER the finish_reason chunk
// 3. Intermediate chunks have explicit "usage: null" (included in JSON)
// 4. The usage chunk includes prompt_tokens, completion_tokens, and total_tokens
// 5. The usage chunk appears before the [DONE] marker
//
// Example stream sequence WITH include_usage:
//
//	→ {"choices":[{"delta":{"role":"assistant","content":null}}]} // Initial
//	→ {"choices":[{"delta":{"content":"Hello"}}]}                  // Content
//	→ {"choices":[{"delta":{},"finish_reason":"stop"}]}           // Finish
//	→ {"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}} // Usage
//	→ [DONE]                                                       // Termination
//
// Example stream sequence WITHOUT include_usage:
//
//	→ {"choices":[{"delta":{"role":"assistant","content":null}}]} // Initial
//	→ {"choices":[{"delta":{"content":"Hello"}}]}                  // Content
//	→ {"choices":[{"delta":{},"finish_reason":"stop"}]}           // Finish
//	→ [DONE]                                                       // Termination (no usage)
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
	mu           sync.Mutex
	sse          *SSEWriter
	streamID     string
	model        string
	created      int64
	includeUsage bool // Whether to include usage in the stream
}

// OpenAIStreamOption is a functional option for configuring OpenAIStreamWriter
type OpenAIStreamOption func(*OpenAIStreamWriter)

// WithIncludeUsage sets whether usage information should be included in the stream
func WithIncludeUsage(include bool) OpenAIStreamOption {
	return func(w *OpenAIStreamWriter) {
		w.includeUsage = include
	}
}

// NewOpenAIStreamWriter creates a new OpenAI SSE stream writer
func NewOpenAIStreamWriter(w http.ResponseWriter, model string, ctx context.Context, opts ...OpenAIStreamOption) (*OpenAIStreamWriter, error) {
	sse, err := NewSSEWriter(w, ctx)
	if err != nil {
		return nil, err
	}

	writer := &OpenAIStreamWriter{
		sse:      sse,
		streamID: generateUUID("chatcmpl-"),
		model:    model,
		created:  time.Now().Unix(),
	}

	// Apply options
	for _, opt := range opts {
		opt(writer)
	}

	return writer, nil
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

// WriteInitialAssistantDelta writes the initial assistant role delta.
// This is required by OpenAI format before sending content or tool calls.
// It explicitly sets role="assistant" and content=null.
func (w *OpenAIStreamWriter) WriteInitialAssistantDelta() error {
	role := "assistant"
	return w.WriteDelta("", &role, nil)
}

// WriteDelta writes a delta update (simplified helper)
func (w *OpenAIStreamWriter) WriteDelta(content string, role *string, finishReason *string) error {
	delta := OpenAIDelta{}

	if role != nil {
		r := core.Role(*role)
		delta.Role = &r
	}
	// If emitting an initial assistant delta with empty content,
	// force `content: null` to match OpenAI shape.
	if role != nil && content == "" {
		delta.ContentNull = true
	}

	// OpenAI initial assistant delta should have content: null when emitting tool calls.
	// We set content only when non-empty; otherwise leave nil so it encodes as null
	// (per models.go tag), matching OpenAI shape for the first delta.
	if content != "" {
		delta.Content = &content
	}
	// Ensure intermediate chunks explicitly include finish_reason: null
	// Final chunk should set finish_reason to a concrete value (e.g., "tool_calls").
	// The finishReason pointer being nil will encode as null in JSON.

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

// WriteContent writes a content chunk (simplified helper for text streaming)
func (w *OpenAIStreamWriter) WriteContent(text string) error {
	return w.WriteDelta(text, nil, nil)
}

// WriteToolCallIntro writes the initial tool call with ID, name, and empty arguments
func (w *OpenAIStreamWriter) WriteToolCallIntro(index int, id, name string) error {
	toolCall := &ToolCallDelta{
		Index: index,
		ID:    id,
		Type:  "function",
		Function: &FunctionCallDelta{
			Name:      name,
			Arguments: "",
		},
	}
	return w.WriteToolCallDelta(index, toolCall, nil, nil)
}

// WriteToolArguments writes tool argument chunks
func (w *OpenAIStreamWriter) WriteToolArguments(index int, argsChunk string) error {
	toolCall := &ToolCallDelta{
		Index: index,
		Function: &FunctionCallDelta{
			Arguments: argsChunk,
		},
	}
	return w.WriteToolCallDelta(index, toolCall, nil, nil)
}

// WriteToolCallDelta writes a tool call delta
func (w *OpenAIStreamWriter) WriteToolCallDelta(index int, toolCall *ToolCallDelta, role *string, finishReason *string) error {
	var delta OpenAIDelta
	if role != nil {
		r := core.Role(*role)
		delta.Role = &r
	}
	delta.ToolCalls = []ToolCallDelta{*toolCall}
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

// WriteUsage writes usage information (some models send this)
func (w *OpenAIStreamWriter) WriteUsage(usage *OpenAIUsage) error {
	// OpenAI usage chunks often include an empty choices array.
	chunk := &OpenAIStreamChunk{
		Usage:   usage,
		Choices: []OpenAIStreamDelta{},
	}

	return w.WriteChunk(chunk)
}

// WriteFinish writes the final chunk with finish_reason and optionally usage, then [DONE]
func (w *OpenAIStreamWriter) WriteFinish(finishReason string, usage *OpenAIUsage) error {
	// Final chunk with finish_reason
	if err := w.WriteDelta("", nil, &finishReason); err != nil {
		return err
	}

	// Emit usage if requested and available
	if w.includeUsage && usage != nil {
		if err := w.WriteUsage(usage); err != nil {
			return err
		}
	}

	// Send [DONE] marker
	return w.WriteDone()
}

// WriteDone writes the OpenAI stream termination marker "[DONE]".
// This is specific to OpenAI's SSE format and signals the end of the stream.
// Note: Anthropic format uses message_stop event instead of [DONE].
// Google format has its own termination mechanism.
func (w *OpenAIStreamWriter) WriteDone() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.sse.WriteEvent("", "[DONE]")
}

// WriteError writes an error in OpenAI streaming format
func (w *OpenAIStreamWriter) WriteError(errType, message string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	errorResp := OpenAIError{
		Error: OpenAIErrorDetail{
			Type:    errType,
			Message: message,
		},
	}

	data, err := json.Marshal(errorResp)
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
	lastFinishReason string
	lastUsage        *OpenAIUsage // Store usage from MessageDelta for emission with finish_reason
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
	case EventMessageStart:
		// Send initial role delta
		if !c.hasStarted {
			// First delta: send initial assistant role
			if err := c.writer.WriteInitialAssistantDelta(); err != nil {
				return err
			}
			c.hasStarted = true
		}

	case EventContentBlockStart:
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
			// First tool_call delta: id, name, empty arguments (no role)
			if err := c.writer.WriteToolCallDelta(event.Index, toolCall, nil, nil); err != nil {
				return err
			}
		}

	case EventContentBlockDelta:
		var event ContentBlockDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		switch event.Delta.Type {
		case "text_delta":
			// Send text delta
			if err := c.writer.WriteContent(event.Delta.Text); err != nil {
				return err
			}
			c.accumulatedText += event.Delta.Text

		case "input_json_delta":
			// Send tool arguments delta
			if toolCall, ok := c.toolCallsByIndex[event.Index]; ok {
				// Safely dereference partial JSON
				arg := ""
				if event.Delta.PartialJSON != nil {
					arg = *event.Delta.PartialJSON
				}
				// Create a new tool call with just the arguments delta
				deltaCall := &ToolCallDelta{
					Index: event.Index,
					Function: &FunctionCallDelta{
						Arguments: arg,
					},
				}
				// Subsequent deltas contain only arguments fragments
				if err := c.writer.WriteToolCallDelta(event.Index, deltaCall, nil, nil); err != nil {
					return err
				}
				// Accumulate arguments
				toolCall.Function.Arguments += arg
			}
		}

	case EventMessageDelta:
		var event MessageDeltaEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}

		// Map stop reason to finish reason
		if event.Delta.StopReason != "" {
			// Record mapped finish reason and emit it on message_stop.
			c.lastFinishReason = MapStopReasonToOpenAIFinishReason(event.Delta.StopReason)
		}

		// Store usage for later emission with finish_reason
		if event.Usage != nil {
			c.lastUsage = &OpenAIUsage{
				PromptTokens:     event.Usage.InputTokens,
				CompletionTokens: event.Usage.OutputTokens,
				TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
			}
		}

	case EventMessageStop:
		// Final chunk: finish_reason + optional usage + [DONE]
		finish := c.lastFinishReason
		if finish == "" {
			finish = "stop"
		}
		// Use WriteFinish to handle finish_reason, usage, and [DONE] in correct order
		return c.writer.WriteFinish(finish, c.lastUsage)

	case EventError:
		var event ErrorEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return c.writer.WriteError(event.Error.Type, event.Error.Message)

	case EventPing:
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
	// Ensure initial assistant role delta is sent once
	if !c.hasStarted {
		if err := c.writer.WriteInitialAssistantDelta(); err != nil {
			return err
		}
		c.hasStarted = true
	}

	// First, check for usage metadata and store it
	if usage, ok := chunk["usageMetadata"].(map[string]interface{}); ok {
		c.lastUsage = &OpenAIUsage{
			PromptTokens:     int(usage["promptTokenCount"].(float64)),
			CompletionTokens: int(usage["candidatesTokenCount"].(float64)),
			TotalTokens:      int(usage["totalTokenCount"].(float64)),
		}
	}

	// Extract candidates
	candidates, ok := chunk["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}

	candidate := candidates[0].(map[string]interface{})

	// Check finish reason
	if finishReason, ok := candidate["finishReason"].(string); ok && finishReason != "" {
		mapped := mapGoogleFinishReason(finishReason)
		// Use WriteFinish to handle finish_reason, usage, and [DONE] in correct order
		return c.writer.WriteFinish(mapped, c.lastUsage)
	}

	// Process content
	if content, ok := candidate["content"].(map[string]interface{}); ok {
		if parts, ok := content["parts"].([]interface{}); ok {
			for _, part := range parts {
				partMap := part.(map[string]interface{})

				// Handle text
				if text, ok := partMap["text"].(string); ok && text != "" {
					if err := c.writer.WriteContent(text); err != nil {
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

					// Tool call intro without role (role was sent in first delta)
					if err := c.writer.WriteToolCallDelta(c.currentToolIndex, toolCall, nil, nil); err != nil {
						return err
					}
					c.currentToolIndex++
				}
			}
		}
	}

	return nil
}

// HandleArgoText processes plain text from Argo and converts to OpenAI format
func (c *OpenAIStreamConverter) HandleArgoText(text string) error {
	// Send initial role if not started
	if !c.hasStarted {
		// Send initial assistant delta
		if err := c.writer.WriteInitialAssistantDelta(); err != nil {
			return err
		}
		c.hasStarted = true
	}

	// Send text delta
	return c.writer.WriteContent(text)
}

// Complete sends the completion sequence for OpenAI format.
// This is a convenience method that delegates to FinishStream.
// For direct control over usage reporting, use FinishStream() directly.
func (c *OpenAIStreamConverter) Complete(finishReason string) error {
	// Delegate to FinishStream for the standard completion sequence
	return c.FinishStream(finishReason, nil)
}

// FinishStream sends the completion sequence with optional usage information
func (c *OpenAIStreamConverter) FinishStream(finishReason string, usage *OpenAIUsage) error {
	// Use the writer's WriteFinish method which handles both finish_reason and usage
	return c.writer.WriteFinish(finishReason, usage)
}
