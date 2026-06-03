package proxy

import (
	"context"
	"lmtools/internal/logger"
	"net/http"
	"sync"
)

// StreamingState tracks the state of a streaming response.
type StreamingState struct {
	MessageID        string
	TextSent         bool
	TextBlockClosed  bool
	ToolIndex        *int
	LastToolIndex    int
	AccumulatedText  string
	InputTokens      int
	OutputTokens     int
	ClosedBlocks     map[int]bool
	ParsedToolBlocks map[openAIStreamToolKey]int
}

func newStreamingState(messageID string) *StreamingState {
	return &StreamingState{
		MessageID:        messageID,
		ClosedBlocks:     make(map[int]bool),
		ParsedToolBlocks: make(map[openAIStreamToolKey]int),
	}
}

// AnthropicStreamHandler handles streaming for Anthropic format.
type AnthropicStreamHandler struct {
	mu            sync.Mutex
	sse           *SSEWriter
	state         *StreamingState
	originalModel string
	ctx           context.Context
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
	return h.SendEvent(EventContentBlockStart, NewToolUseStart(index, toolID, name))
}

// SendToolInputDelta sends tool input delta.
func (h *AnthropicStreamHandler) SendToolInputDelta(index int, partialJSON string) error {
	return h.SendEvent(EventContentBlockDelta, NewToolInputDelta(index, partialJSON))
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

// FinishStream sends the standard completion sequence for a stream.
func (h *AnthropicStreamHandler) FinishStream(stopReason string, usage *AnthropicUsage) error {
	if usage != nil {
		h.SetUsage(usage.InputTokens, usage.OutputTokens)
	}

	h.mu.Lock()
	outputTokens := h.state.OutputTokens
	h.mu.Unlock()

	if err := h.SendMessageDelta(stopReason, outputTokens); err != nil {
		return handleStreamError(h.ctx, h, "AnthropicFinish", err)
	}
	if err := h.SendMessageStop(); err != nil {
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
	hasParsedToolBlocks := len(h.state.ParsedToolBlocks) > 0
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

	if toolIndex != nil || hasParsedToolBlocks {
		for i := 1; i <= lastToolIndex; i++ {
			if err := h.SendContentBlockStop(i); err != nil {
				return handleStreamError(h.ctx, h, "AnthropicComplete", err)
			}
		}
	}

	return h.FinishStream(stopReason, nil)
}

// SendStreamError sends an error event to the client.
func (h *AnthropicStreamHandler) SendStreamError(message string) error {
	return h.SendEvent(EventError, NewError(message))
}

// UpdateModel updates the model in the handler state.
func (h *AnthropicStreamHandler) UpdateModel(model string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.originalModel = model
}

// SetUsage sets the token usage in the handler state.
func (h *AnthropicStreamHandler) SetUsage(inputTokens, outputTokens int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.InputTokens = inputTokens
	h.state.OutputTokens = outputTokens
}

func (h *AnthropicStreamHandler) SetParsedUsage(inputTokens, outputTokens *int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if inputTokens != nil {
		h.state.InputTokens = *inputTokens
	}
	if outputTokens != nil {
		h.state.OutputTokens = *outputTokens
	}
}

func (h *AnthropicStreamHandler) CloseParsedTextBlockIfNeeded() error {
	h.mu.Lock()
	if h.state.TextBlockClosed {
		h.mu.Unlock()
		return nil
	}
	needTextDelta := h.state.AccumulatedText != "" && !h.state.TextSent
	accumulatedText := h.state.AccumulatedText
	h.mu.Unlock()

	if needTextDelta {
		if err := h.SendTextDelta(accumulatedText); err != nil {
			return err
		}
	}
	if err := h.SendContentBlockStop(0); err != nil {
		return err
	}

	h.mu.Lock()
	h.state.TextBlockClosed = true
	h.mu.Unlock()
	return nil
}

func (h *AnthropicStreamHandler) BeginParsedToolUseBlock(streamIndex *int, toolID, name string) (int, error) {
	if streamIndex == nil {
		return h.beginParsedToolUseBlock(nil, toolID, name)
	}
	key := openAIStreamToolKey{ToolIndex: *streamIndex}
	return h.BeginParsedToolUseBlockForOpenAIKey(key, toolID, name)
}

func (h *AnthropicStreamHandler) BeginParsedToolUseBlockForOpenAIKey(key openAIStreamToolKey, toolID, name string) (int, error) {
	return h.beginParsedToolUseBlock(&key, toolID, name)
}

func (h *AnthropicStreamHandler) beginParsedToolUseBlock(key *openAIStreamToolKey, toolID, name string) (int, error) {
	h.mu.Lock()
	if key != nil {
		if blockIndex, ok := h.state.ParsedToolBlocks[*key]; ok {
			h.mu.Unlock()
			return blockIndex, nil
		}
	}
	h.mu.Unlock()

	if err := h.CloseParsedTextBlockIfNeeded(); err != nil {
		return 0, err
	}

	h.mu.Lock()
	if key != nil {
		index := key.ToolIndex
		h.state.ToolIndex = &index
	}
	h.state.LastToolIndex++
	blockIndex := h.state.LastToolIndex
	if key != nil {
		h.state.ParsedToolBlocks[*key] = blockIndex
	}
	h.mu.Unlock()

	if err := h.SendToolUseStart(blockIndex, toolID, name); err != nil {
		return 0, err
	}
	return blockIndex, nil
}

// SendEvent sends a generic event with JSON data.
func (h *AnthropicStreamHandler) SendEvent(eventType string, data interface{}) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sse.WriteJSON(eventType, data)
}
