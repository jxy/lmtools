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

// OpenAIStreamWriter handles Server-Sent Events writing for OpenAI format.
type OpenAIStreamWriter struct {
	mu           sync.Mutex
	sse          *SSEWriter
	ctx          context.Context
	streamID     string
	model        string
	created      int64
	includeUsage bool
}

// OpenAIStreamOption is a functional option for configuring OpenAIStreamWriter.
type OpenAIStreamOption func(*OpenAIStreamWriter)

// WithIncludeUsage sets whether usage information should be included in the stream.
func WithIncludeUsage(include bool) OpenAIStreamOption {
	return func(w *OpenAIStreamWriter) {
		w.includeUsage = include
	}
}

// NewOpenAIStreamWriter creates a new OpenAI SSE stream writer.
func NewOpenAIStreamWriter(w http.ResponseWriter, model string, ctx context.Context, opts ...OpenAIStreamOption) (*OpenAIStreamWriter, error) {
	sse, err := NewSSEWriter(w, ctx)
	if err != nil {
		return nil, err
	}

	writer := &OpenAIStreamWriter{
		sse:      sse,
		ctx:      ctx,
		streamID: newOpenAIStreamID(),
		model:    model,
		created:  time.Now().Unix(),
	}

	for _, opt := range opts {
		opt(writer)
	}

	return writer, nil
}

// WriteChunk writes a complete OpenAI streaming chunk.
func (w *OpenAIStreamWriter) WriteChunk(chunk *OpenAIStreamChunk) error {
	w.mu.Lock()
	defer w.mu.Unlock()

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

	data, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("failed to marshal chunk: %w", err)
	}

	return w.sse.WriteEvent("", string(data))
}

// WriteInitialAssistantDelta writes the initial assistant role delta.
func (w *OpenAIStreamWriter) WriteInitialAssistantDelta() error {
	return w.WriteInitialAssistantTextDelta()
}

// WriteInitialAssistantTextDelta writes the initial assistant role delta for a text stream.
func (w *OpenAIStreamWriter) WriteInitialAssistantTextDelta() error {
	role := core.Role("assistant")
	empty := ""
	delta := OpenAIDelta{
		Role:    &role,
		Content: &empty,
	}
	chunk := &OpenAIStreamChunk{
		Choices: []OpenAIStreamDelta{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: nil,
			},
		},
	}
	return w.WriteChunk(chunk)
}

// WriteInitialAssistantToolCallDelta writes the initial assistant delta for a tool-call stream.
func (w *OpenAIStreamWriter) WriteInitialAssistantToolCallDelta(index int, id, name string) error {
	role := core.Role("assistant")
	delta := OpenAIDelta{
		Role:        &role,
		ContentNull: true,
		ToolCalls: []ToolCallDelta{
			{
				Index: index,
				ID:    id,
				Type:  "function",
				Function: &FunctionCallDelta{
					Name:      name,
					Arguments: "",
				},
			},
		},
	}
	chunk := &OpenAIStreamChunk{
		Choices: []OpenAIStreamDelta{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: nil,
			},
		},
	}
	return w.WriteChunk(chunk)
}

// WriteDelta writes a delta update.
func (w *OpenAIStreamWriter) WriteDelta(content string, role *string, finishReason *string) error {
	delta := OpenAIDelta{}

	if role != nil {
		r := core.Role(*role)
		delta.Role = &r
	}
	if role != nil && content == "" {
		delta.ContentNull = true
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

// WriteContent writes a content chunk.
func (w *OpenAIStreamWriter) WriteContent(text string) error {
	return w.WriteDelta(text, nil, nil)
}

// WriteToolCallIntro writes the initial tool call with ID, name, and empty arguments.
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

// WriteToolArguments writes tool argument chunks.
func (w *OpenAIStreamWriter) WriteToolArguments(index int, argsChunk string) error {
	toolCall := &ToolCallDelta{
		Index: index,
		Function: &FunctionCallDelta{
			Arguments: argsChunk,
		},
	}
	return w.WriteToolCallDelta(index, toolCall, nil, nil)
}

// WriteToolCallDelta writes a tool call delta.
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

// WriteUsage writes usage information.
func (w *OpenAIStreamWriter) WriteUsage(usage *OpenAIUsage) error {
	chunk := &OpenAIStreamChunk{
		Usage:   usage,
		Choices: []OpenAIStreamDelta{},
	}

	return w.WriteChunk(chunk)
}

// WriteFinish writes the final chunk with finish_reason and optionally usage, then [DONE].
func (w *OpenAIStreamWriter) WriteFinish(finishReason string, usage *OpenAIUsage) error {
	if err := w.WriteDelta("", nil, &finishReason); err != nil {
		return err
	}

	if w.includeUsage && usage != nil {
		if err := w.WriteUsage(usage); err != nil {
			return err
		}
	}

	return w.WriteDone()
}

// WriteDone writes the OpenAI stream termination marker "[DONE]".
func (w *OpenAIStreamWriter) WriteDone() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.sse.WriteEvent("", OpenAIDoneMarker)
}

// WriteError writes an error in OpenAI streaming format.
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

// Close ensures the stream is properly closed.
func (w *OpenAIStreamWriter) Close() error {
	return nil
}

// SendStreamError sends an error event to the client.
func (w *OpenAIStreamWriter) SendStreamError(message string) error {
	return w.WriteError("server_error", message)
}
