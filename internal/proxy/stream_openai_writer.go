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
	mu                sync.Mutex
	sse               *SSEWriter
	ctx               context.Context
	streamID          string
	model             string
	created           int64
	includeUsage      bool
	stopper           *stopTextEnforcer
	finished          bool
	localStopFinished bool
	doneWritten       bool
}

// OpenAIStreamOption is a functional option for configuring OpenAIStreamWriter.
type OpenAIStreamOption func(*OpenAIStreamWriter)

// WithIncludeUsage sets whether usage information should be included in the stream.
func WithIncludeUsage(include bool) OpenAIStreamOption {
	return func(w *OpenAIStreamWriter) {
		w.includeUsage = include
	}
}

// WithStopSequences sets stop sequences enforced locally on text deltas.
func WithStopSequences(stops []string) OpenAIStreamOption {
	return func(w *OpenAIStreamWriter) {
		w.stopper = newStopTextEnforcer(stops)
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
		streamID: generateUUID("chatcmpl-"),
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
	return w.writeChunk(chunk, false)
}

func (w *OpenAIStreamWriter) writeChunk(chunk *OpenAIStreamChunk, allowFinished bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.finished && !allowFinished {
		return nil
	}

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
	return w.writeDelta(content, role, finishReason, false)
}

func (w *OpenAIStreamWriter) writeDelta(content string, role *string, finishReason *string, allowFinished bool) error {
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

	return w.writeChunk(chunk, allowFinished)
}

// WriteContent writes a content chunk.
func (w *OpenAIStreamWriter) WriteContent(text string) error {
	if w.finished {
		return nil
	}
	if w.stopper == nil {
		return w.WriteDelta(text, nil, nil)
	}
	filtered, matched := w.stopper.Push(text)
	if filtered != "" {
		if err := w.WriteDelta(filtered, nil, nil); err != nil {
			return err
		}
	}
	if matched {
		if err := w.writeLocalStopFinish(); err != nil {
			return err
		}
		if !w.includeUsage {
			return w.writeDone(true)
		}
	}
	return nil
}

func (w *OpenAIStreamWriter) writeLocalStopFinish() error {
	w.finished = true
	w.localStopFinished = true
	finishReason := "stop"
	return w.writeDelta("", nil, &finishReason, true)
}

// WriteToolCallDelta writes a tool call delta.
func (w *OpenAIStreamWriter) WriteToolCallDelta(index int, toolCall *ToolCallDelta, role *string, finishReason *string) error {
	if w.finished {
		return nil
	}
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
	return w.writeUsage(usage, false)
}

func (w *OpenAIStreamWriter) writeUsage(usage *OpenAIUsage, allowFinished bool) error {
	chunk := &OpenAIStreamChunk{
		Usage:   usage,
		Choices: []OpenAIStreamDelta{},
	}

	return w.writeChunk(chunk, allowFinished)
}

// WriteFinish writes the final chunk with finish_reason and optionally usage, then [DONE].
func (w *OpenAIStreamWriter) WriteFinish(finishReason string, usage *OpenAIUsage) error {
	if w.doneWritten {
		return nil
	}
	if w.finished && !w.localStopFinished {
		return nil
	}
	if w.localStopFinished {
		if w.includeUsage && usage != nil {
			if err := w.writeUsage(usage, true); err != nil {
				return err
			}
		}
		return w.writeDone(true)
	}
	if w.stopper != nil {
		if w.stopper.Stopped() {
			finishReason = "stop"
		} else if tail := w.stopper.Flush(); tail != "" {
			if err := w.WriteDelta(tail, nil, nil); err != nil {
				return err
			}
		}
	}
	w.finished = true
	if err := w.writeDelta("", nil, &finishReason, true); err != nil {
		return err
	}

	if w.includeUsage && usage != nil {
		if err := w.writeUsage(usage, true); err != nil {
			return err
		}
	}

	return w.writeDone(true)
}

// WriteDone writes the OpenAI stream termination marker "[DONE]".
func (w *OpenAIStreamWriter) WriteDone() error {
	return w.writeDone(false)
}

func (w *OpenAIStreamWriter) writeDone(allowFinished bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.doneWritten || (w.finished && !allowFinished) {
		return nil
	}
	w.doneWritten = true
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

// SendStreamError sends an error event to the client.
func (w *OpenAIStreamWriter) SendStreamError(message string) error {
	return w.WriteError("server_error", message)
}
