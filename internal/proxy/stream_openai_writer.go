package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
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
	stopper      *stopTextEnforcer
	state        openAIStreamState
}

type openAIStreamState uint8

const (
	openAIStreamOpen openAIStreamState = iota
	openAIStreamAwaitingUsage
	openAIStreamFinished
	openAIStreamDone
)

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

// writeChunkLocked writes one chunk. The caller holds w.mu and has already
// decided that the chunk is valid for the current stream state.
func (w *OpenAIStreamWriter) writeChunkLocked(chunk *OpenAIStreamChunk) error {
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != openAIStreamOpen {
		return nil
	}
	return w.writeChunkLocked(chunk)
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != openAIStreamOpen {
		return nil
	}
	return w.writeChunkLocked(chunk)
}

// WriteDelta writes a delta update.
func (w *OpenAIStreamWriter) WriteDelta(content string, role *string, finishReason *string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != openAIStreamOpen {
		return nil
	}
	return w.writeDeltaLocked(content, role, finishReason)
}

func (w *OpenAIStreamWriter) writeDeltaLocked(content string, role *string, finishReason *string) error {
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

	return w.writeChunkLocked(chunk)
}

// WriteContent writes a content chunk.
func (w *OpenAIStreamWriter) WriteContent(text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.state != openAIStreamOpen {
		return nil
	}
	if w.stopper == nil {
		return w.writeDeltaLocked(text, nil, nil)
	}
	filtered, matched := w.stopper.Push(text)
	if filtered != "" {
		if err := w.writeDeltaLocked(filtered, nil, nil); err != nil {
			return err
		}
	}
	if matched {
		finishReason := "stop"
		if err := w.writeDeltaLocked("", nil, &finishReason); err != nil {
			return err
		}
		if w.includeUsage {
			w.state = openAIStreamAwaitingUsage
			return nil
		}
		w.state = openAIStreamFinished
		return w.writeDoneLocked()
	}
	return nil
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

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != openAIStreamOpen {
		return nil
	}
	return w.writeChunkLocked(chunk)
}

func (w *OpenAIStreamWriter) writeUsageLocked(usage *OpenAIUsage) error {
	chunk := &OpenAIStreamChunk{
		Usage:   usage,
		Choices: []OpenAIStreamDelta{},
	}

	return w.writeChunkLocked(chunk)
}

// WriteFinish writes the final chunk with finish_reason and optionally usage, then [DONE].
func (w *OpenAIStreamWriter) WriteFinish(finishReason string, usage *OpenAIUsage) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch w.state {
	case openAIStreamDone:
		return nil
	case openAIStreamFinished:
		return w.writeDoneLocked()
	case openAIStreamAwaitingUsage:
		if w.includeUsage && usage != nil {
			if err := w.writeUsageLocked(usage); err != nil {
				return err
			}
		}
		w.state = openAIStreamFinished
		return w.writeDoneLocked()
	}

	if w.stopper != nil {
		if w.stopper.Stopped() {
			finishReason = "stop"
		} else if tail := w.stopper.Flush(); tail != "" {
			if err := w.writeDeltaLocked(tail, nil, nil); err != nil {
				return err
			}
		}
	}
	if err := w.writeDeltaLocked("", nil, &finishReason); err != nil {
		return err
	}
	w.state = openAIStreamFinished

	if w.includeUsage && usage != nil {
		if err := w.writeUsageLocked(usage); err != nil {
			return err
		}
	}

	return w.writeDoneLocked()
}

// flushHeldStopText releases text the stop enforcer is still holding as a
// possible stop-sequence prefix. A stream that ends properly flushes in
// WriteFinish; one that is cut short has to flush here, or the generated text
// disappears behind the terminal error. A stream that already finished has
// nothing held: either WriteFinish flushed it, or a local stop matched and the
// enforcer is deliberately withholding the stop sequence itself.
//
// Callers run this ahead of whatever ends the stream, never after. It is
// idempotent, so the two of them overlapping costs nothing.
func (w *OpenAIStreamWriter) flushHeldStopText() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopper == nil || w.state != openAIStreamOpen || w.stopper.Stopped() {
		return nil
	}
	tail := w.stopper.Flush()
	if tail == "" {
		return nil
	}
	return w.writeDeltaLocked(tail, nil, nil)
}

func (w *OpenAIStreamWriter) writeDoneLocked() error {
	if w.state == openAIStreamDone {
		return nil
	}
	if err := w.sse.WriteEvent("", OpenAIDoneMarker); err != nil {
		return err
	}
	w.state = openAIStreamDone
	return nil
}

// WriteError flushes held stop-enforcement text, emits at most one error, and
// closes the stream with [DONE]. If a finish_reason already ended the turn, the
// late error is dropped but any outstanding [DONE] is still written; this
// preserves the completed answer and releases a stream waiting for optional
// usage that can no longer arrive.
func (w *OpenAIStreamWriter) WriteError(errType, message string) error {
	if err := w.flushHeldStopText(); err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeErrorLocked(errType, message)
}

// writeErrorLocked emits one terminal error, or only the outstanding [DONE]
// marker when the answer already finished. The caller holds w.mu.
func (w *OpenAIStreamWriter) writeErrorLocked(errType, message string) error {
	if w.state != openAIStreamOpen {
		logger.From(w.ctx).Warnf("Dropping %s error on a stream that already ended: %s", errType, message)
		return w.writeDoneLocked()
	}

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

	if err := w.sse.WriteEvent("", string(data)); err != nil {
		return err
	}
	w.state = openAIStreamFinished
	return w.writeDoneLocked()
}

// SendStreamError sends an error event to the client.
func (w *OpenAIStreamWriter) SendStreamError(message string) error {
	return w.WriteError("server_error", message)
}

// EnsureTerminated writes the [DONE] sentinel for a provider stream that was cut
// short. A truncated upstream otherwise leaves the client waiting on a marker
// that never arrives, which reads as a dropped connection instead of a failed
// turn. A stream that never produced even its first convertible event still
// gets an error chunk and [DONE]; returning an empty 200 there is the least
// informative form of the same truncation.
func (w *OpenAIStreamWriter) EnsureTerminated(streamErr error) error {
	if !downstreamStreamIsLive(w.ctx) {
		return nil
	}

	// Text held back as a possible stop-sequence prefix is real output that the
	// enforcer was still deciding about, and only a stream that reaches
	// WriteFinish or WriteError releases it — a stream that dies before either
	// never gets there. Flush it before reading terminal state, and before
	// WriteError closes the writer to later deltas.
	if err := w.flushHeldStopText(); err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	switch w.state {
	case openAIStreamDone:
		return nil
	case openAIStreamAwaitingUsage, openAIStreamFinished:
		return w.writeDoneLocked()
	}

	message := "upstream stream ended before the terminal chunk"
	if streamErr != nil {
		message = fmt.Sprintf("upstream stream ended before the terminal chunk: %v", streamErr)
	}
	return w.writeErrorLocked("server_error", message)
}
