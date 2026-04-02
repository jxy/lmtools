package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/logger"
	"net/http"
	"sync"
)

// StreamingState tracks the state of a streaming response.
type StreamingState struct {
	MessageID         string
	TextSent          bool
	TextBlockClosed   bool
	ToolIndex         *int
	LastToolIndex     int
	AccumulatedText   string
	HasSentStopReason bool
	InputTokens       int
	OutputTokens      int
	ToolCalls         []AnthropicContentBlock
	ClosedBlocks      map[int]bool
	EventsSent        []string
}

func newStreamingState(messageID string) *StreamingState {
	return &StreamingState{
		MessageID:    messageID,
		ClosedBlocks: make(map[int]bool),
	}
}

// AnthropicStreamHandler handles streaming for Anthropic format.
type AnthropicStreamHandler struct {
	mu                 sync.Mutex
	sse                *SSEWriter
	state              *StreamingState
	originalModel      string
	simulatedStreaming bool
	ctx                context.Context
}

// NewAnthropicStreamHandler creates a new Anthropic stream handler.
func NewAnthropicStreamHandler(w http.ResponseWriter, originalModel string, ctx context.Context) (*AnthropicStreamHandler, error) {
	sse, err := NewSSEWriter(w, ctx)
	if err != nil {
		return nil, err
	}

	return &AnthropicStreamHandler{
		sse:           sse,
		originalModel: originalModel,
		ctx:           ctx,
		state:         newStreamingState(newAnthropicStreamID()),
	}, nil
}

// SendMessageStart sends the initial message_start event.
func (h *AnthropicStreamHandler) SendMessageStart() error {
	evt := NewMessageStart(h.state.MessageID, h.originalModel, h.state.InputTokens, h.state.OutputTokens)
	return h.SendEvent(EventMessageStart, evt)
}

// SendContentBlockStart sends a content_block_start event.
func (h *AnthropicStreamHandler) SendContentBlockStart(index int, blockType string) error {
	return h.SendEvent(EventContentBlockStart, NewContentBlockStart(index, blockType))
}

// SendTextDelta sends a text delta.
func (h *AnthropicStreamHandler) SendTextDelta(text string) error {
	h.mu.Lock()
	if h.state.TextBlockClosed {
		h.mu.Unlock()
		logger.From(h.ctx).Debugf("SendTextDelta called but text block is closed, ignoring %d chars", len(text))
		return nil
	}

	h.state.TextSent = true
	h.state.AccumulatedText += text
	h.mu.Unlock()

	return h.SendEvent(EventContentBlockDelta, NewTextDelta(0, text))
}

// SendToolUseStart sends a tool_use block start.
func (h *AnthropicStreamHandler) SendToolUseStart(index int, toolID, name string) error {
	h.mu.Lock()
	if !h.simulatedStreaming {
		h.state.ToolCalls = append(h.state.ToolCalls, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    toolID,
			Name:  name,
			Input: make(map[string]interface{}),
		})
	}
	h.mu.Unlock()

	return h.SendEvent(EventContentBlockStart, NewToolUseStart(index, toolID, name))
}

// SendToolInputDelta sends tool input delta.
func (h *AnthropicStreamHandler) SendToolInputDelta(index int, partialJSON string) error {
	h.mu.Lock()
	if !h.simulatedStreaming && len(h.state.ToolCalls) > 0 {
		logger.From(h.ctx).Debugf("%s", "  Real streaming: would accumulate partial JSON")
	}
	h.mu.Unlock()

	return h.SendEvent(EventContentBlockDelta, NewToolInputDelta(index, partialJSON))
}

// SendContentBlockDelta sends a content_block_delta event with any delta type.
func (h *AnthropicStreamHandler) SendContentBlockDelta(index int, delta interface{}) error {
	var deltaData ContentBlockDeltaEvent

	if evt, ok := delta.(ContentBlockDeltaEvent); ok {
		deltaData = evt
	} else {
		switch d := delta.(type) {
		case DeltaContent:
			switch d.Type {
			case "text_delta":
				deltaData = NewTextDelta(index, d.Text)
			case "input_json_delta":
				partialJSON := ""
				if d.PartialJSON != nil {
					partialJSON = *d.PartialJSON
				}
				deltaData = NewToolInputDelta(index, partialJSON)
			default:
				deltaData = ContentBlockDeltaEvent{
					Type:  EventContentBlockDelta,
					Index: index,
					Delta: d,
				}
			}
		case string:
			deltaData = NewTextDelta(index, d)
		default:
			deltaData = NewTextDelta(index, fmt.Sprintf("%v", delta))
		}
	}

	return h.SendEvent(EventContentBlockDelta, deltaData)
}

// SendContentBlockStop sends a content_block_stop event.
func (h *AnthropicStreamHandler) SendContentBlockStop(index int) error {
	h.mu.Lock()
	if h.state.ClosedBlocks[index] {
		h.mu.Unlock()
		return nil
	}
	h.state.ClosedBlocks[index] = true
	h.mu.Unlock()

	return h.SendEvent(EventContentBlockStop, NewContentBlockStop(index))
}

// SendPing sends a ping event.
func (h *AnthropicStreamHandler) SendPing() error {
	return h.SendEvent(EventPing, NewPing())
}

// SendMessageDelta sends a message_delta event.
func (h *AnthropicStreamHandler) SendMessageDelta(stopReason string, outputTokens int) error {
	h.mu.Lock()
	inputTokens := h.state.InputTokens
	h.mu.Unlock()

	usage := &AnthropicUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	return h.SendEvent(EventMessageDelta, NewMessageDelta(stopReason, usage))
}

// SendMessageStop sends a message_stop event.
func (h *AnthropicStreamHandler) SendMessageStop() error {
	return h.SendEvent(EventMessageStop, NewMessageStop())
}

// SendDone sends the stream termination marker.
func (h *AnthropicStreamHandler) SendDone() error {
	return nil
}

// FinishStream sends the standard completion sequence for a stream.
func (h *AnthropicStreamHandler) FinishStream(stopReason string, usage *AnthropicUsage) error {
	if usage != nil {
		h.SetUsage(usage.InputTokens, usage.OutputTokens)
	}
	h.SetStopReason(stopReason)

	h.mu.Lock()
	outputTokens := h.state.OutputTokens
	h.mu.Unlock()

	if err := h.SendMessageDelta(stopReason, outputTokens); err != nil {
		return handleStreamError(h.ctx, h, "AnthropicFinish", err)
	}
	if err := h.SendMessageStop(); err != nil {
		return handleStreamError(h.ctx, h, "AnthropicFinish", err)
	}
	if err := h.SendDone(); err != nil {
		return handleStreamError(h.ctx, h, "AnthropicFinish", err)
	}
	return nil
}

// Complete completes the stream by closing open blocks and sending completion events.
func (h *AnthropicStreamHandler) Complete(stopReason string) error {
	h.mu.Lock()
	needToCloseText := !h.state.TextBlockClosed && (h.state.TextSent || h.state.AccumulatedText != "")
	accumulatedText := h.state.AccumulatedText
	textSent := h.state.TextSent
	toolIndex := h.state.ToolIndex
	lastToolIndex := h.state.LastToolIndex
	simulatedStreaming := h.simulatedStreaming
	toolCallsLen := len(h.state.ToolCalls)
	h.mu.Unlock()

	if needToCloseText {
		if accumulatedText != "" && !textSent {
			if err := h.SendTextDelta(accumulatedText); err != nil {
				return handleStreamError(h.ctx, h, "AnthropicComplete", err)
			}
		}
		if err := h.SendContentBlockStop(0); err != nil {
			return handleStreamError(h.ctx, h, "AnthropicComplete", err)
		}
		h.mu.Lock()
		h.state.TextBlockClosed = true
		h.mu.Unlock()
	}

	if toolIndex != nil {
		for i := 1; i <= lastToolIndex; i++ {
			if err := h.SendContentBlockStop(i); err != nil {
				return handleStreamError(h.ctx, h, "AnthropicComplete", err)
			}
		}
	}

	if simulatedStreaming {
		h.mu.Lock()
		accTextLen := len(h.state.AccumulatedText)
		h.mu.Unlock()
		logger.From(h.ctx).Debugf("Stream complete: stop_reason=%s, text=%d chars, tools=%d", stopReason, accTextLen, toolCallsLen)
	}

	return h.FinishStream(stopReason, nil)
}

// SendStreamError sends an error event to the client.
func (h *AnthropicStreamHandler) SendStreamError(message string) error {
	return h.SendEvent(EventError, NewError(message))
}

// Close ensures the stream is properly closed.
func (h *AnthropicStreamHandler) Close() error {
	return nil
}

// UpdateModel updates the model in the handler state.
func (h *AnthropicStreamHandler) UpdateModel(model string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.originalModel = model
}

// SetStopReason sets the stop reason in the handler state.
func (h *AnthropicStreamHandler) SetStopReason(string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.HasSentStopReason = true
}

// SetUsage sets the token usage in the handler state.
func (h *AnthropicStreamHandler) SetUsage(inputTokens, outputTokens int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.InputTokens = inputTokens
	h.state.OutputTokens = outputTokens
}

// SendMessage sends a complete message for simulated streaming.
func (h *AnthropicStreamHandler) SendMessage(message string) error {
	if err := h.SendMessageStart(); err != nil {
		return err
	}
	if err := h.SendContentBlockStart(0, "text"); err != nil {
		return err
	}
	if err := h.SendTextDelta(message); err != nil {
		return err
	}
	if err := h.SendContentBlockStop(0); err != nil {
		return err
	}
	return h.Complete("end_turn")
}

// SendEvent sends a generic event with JSON data.
func (h *AnthropicStreamHandler) SendEvent(eventType string, data interface{}) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sse.WriteJSON(eventType, data)
}
