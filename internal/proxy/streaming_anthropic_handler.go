package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/logger"
	"net/http"
	"sync"
	"time"
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
	lastEventAt   time.Time

	// Terminal-event tracking. Anthropic clients treat a stream that ends
	// without message_stop or error as a dropped connection, so the handler
	// remembers what it has already sent and can close the stream out itself.
	terminated bool
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
		state:         newStreamingState(generateUUID("msg_")),
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

// StartIdleHeartbeat sends Anthropic ping events after periods with no
// downstream SSE writes. The returned function stops the heartbeat and waits
// for its goroutine to exit.
func (h *AnthropicStreamHandler) StartIdleHeartbeat(interval time.Duration) func() {
	if interval <= 0 {
		return func() {}
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once

	h.mu.Lock()
	h.lastEventAt = time.Now()
	h.mu.Unlock()

	go func() {
		defer close(done)

		timer := time.NewTimer(interval)
		defer timer.Stop()

		for {
			select {
			case <-h.ctx.Done():
				return
			case <-stop:
				return
			case <-timer.C:
				wait, err := h.sendIdlePingIfDue(interval)
				if err != nil {
					if handleErr := handleStreamError(h.ctx, nil, "AnthropicIdleHeartbeat",
						fmt.Errorf("send ping: %w", err)); handleErr != nil {
						return
					}
					return
				}
				if wait <= 0 {
					wait = interval
				}
				timer.Reset(wait)
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}
}

func (h *AnthropicStreamHandler) sendIdlePingIfDue(interval time.Duration) (time.Duration, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	wait := interval - time.Since(h.lastEventAt)
	if wait <= 0 {
		wrote, err := h.sendEventLocked(EventPing, NewPing())
		if err != nil {
			return 0, err
		}
		if !wrote {
			return interval, nil
		}
	}
	return wait, nil
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

// SendStreamError sends an error event to the client, unless the stream already
// ended. Both terminal events close the door, and they close it against
// whoever knocks: message_stop said the turn finished, and an Anthropic error
// event is itself terminal, so one failure gets one error.
//
// A turn the client was told had finished does not un-finish because the
// upstream ran into trouble behind it — an error there has the client throw
// away, or pay to regenerate, an answer it already holds complete. In the other
// direction, a failure the upstream has already reported in its own words and
// with its own type is the account the client can act on; the proxy's generic
// "stream processing error" behind it only competes with it. Both are ordinary
// rather than exotic: an upstream error event reaches the parser and then the
// handler a second time on the way out, and stop sequences are stripped from
// the forwarded request, so the upstream keeps generating past the point the
// client was told the answer ended.
//
// The drop is logged. The client has its ending either way, but an operator
// watching for upstream trouble still wants to see it.
func (h *AnthropicStreamHandler) SendStreamError(message string) error {
	h.mu.Lock()
	wrote, err := h.sendEventLocked(EventError, NewError(message))
	h.mu.Unlock()

	if err != nil {
		return err
	}
	if !wrote {
		logger.From(h.ctx).Warnf("Dropping error on an Anthropic stream that already ended: %s", message)
	}
	return nil
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
	_, err := h.sendEventLocked(eventType, data)
	return err
}

// sendEventLocked is the terminal-event boundary for Anthropic streams. The
// caller must hold h.mu. Once message_stop or error is written, every later
// event is suppressed, including heartbeat pings.
func (h *AnthropicStreamHandler) sendEventLocked(eventType string, data interface{}) (bool, error) {
	if h.terminated {
		return false, nil
	}
	if err := h.sse.WriteJSON(eventType, data); err != nil {
		return false, err
	}
	h.lastEventAt = time.Now()
	if eventType == EventMessageStop || eventType == EventError {
		h.terminated = true
	}
	return true, nil
}

// EnsureTerminated closes out a provider stream that never reached a terminal
// event. An upstream that is truncated, killed by a gateway, or cut off by a
// size limit otherwise leaves the client waiting on a message_stop that never
// arrives, which clients report as a disconnect rather than a failure. An error
// event is already terminal under the Anthropic streaming contract, so a stream
// that emitted one is left alone. Zero events is also a truncation here: once an
// accepted provider stream reaches this owner, returning nothing would leave an
// empty 200 rather than an ordinary HTTP error or a terminal SSE failure.
//
// SendStreamError refuses a late error too, so the terminal checks here are not
// what makes this safe. They are what keeps an ordinary completed stream from
// logging a dropped error on the way out.
func (h *AnthropicStreamHandler) EnsureTerminated(streamErr error) error {
	if !downstreamStreamIsLive(h.ctx) {
		return nil
	}

	h.mu.Lock()
	terminated := h.terminated
	h.mu.Unlock()

	if terminated {
		return nil
	}

	message := "upstream stream ended before message_stop"
	if streamErr != nil {
		message = fmt.Sprintf("upstream stream ended before message_stop: %v", streamErr)
	}
	logger.From(h.ctx).Warnf("Anthropic stream had no terminal event; sending error event: %s", message)
	return h.SendStreamError(message)
}
