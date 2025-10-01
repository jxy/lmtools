package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSEWriter handles Server-Sent Events writing
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
}

// NewSSEWriter creates a new SSE writer
func NewSSEWriter(w http.ResponseWriter, ctx context.Context) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.From(ctx).Debugf("ResponseWriter type: %T does not implement http.Flusher", w)
		return nil, fmt.Errorf("streaming not supported (ResponseWriter type: %T)", w)
	}

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	return &SSEWriter{w: w, flusher: flusher, ctx: ctx}, nil
}

// WriteEvent writes an SSE event
func (s *SSEWriter) WriteEvent(eventType, data string) error {
	// Log the event being sent to client
	if eventType != "" {
		logger.From(s.ctx).Debugf("→ CLIENT: event: %s | data: %s", eventType, data)
	} else {
		logger.From(s.ctx).Debugf("→ CLIENT: data: %s", data)
	}

	if eventType != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", eventType); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// WriteJSON writes a JSON object as an SSE event
func (s *SSEWriter) WriteJSON(eventType string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.WriteEvent(eventType, string(jsonData))
}

// TrackEvent tracks an event that was sent to the client (if handler is provided)
func (s *SSEWriter) TrackEvent(handler *AnthropicStreamHandler, eventType string) {
	if handler != nil && handler.state != nil {
		if eventType != "" {
			// Note: This should be called within the handler's mutex lock
			// to avoid race conditions on the EventsSent slice
			handler.state.EventsSent = append(handler.state.EventsSent, eventType)
		}
	}
}

// StreamingState tracks the state of a streaming response
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
	ToolCalls         []AnthropicContentBlock // Track tool calls for final response
	ClosedBlocks      map[int]bool            // Track which blocks have been closed
	EventsSent        []string                // Track all events sent to client
}

// AnthropicStreamHandler handles streaming for Anthropic format
type AnthropicStreamHandler struct {
	// mu protects all fields below AND serializes access to the underlying
	// http.ResponseWriter/Flusher (which are not thread-safe)
	mu                 sync.Mutex
	sse                *SSEWriter
	state              *StreamingState
	originalModel      string
	simulatedStreaming bool // If true, don't track tool calls in state
	ctx                context.Context
}

// NewAnthropicStreamHandler creates a new Anthropic stream handler
func NewAnthropicStreamHandler(w http.ResponseWriter, originalModel string, ctx context.Context) (*AnthropicStreamHandler, error) {
	sse, err := NewSSEWriter(w, ctx)
	if err != nil {
		return nil, err
	}

	return &AnthropicStreamHandler{
		sse:           sse,
		originalModel: originalModel,
		ctx:           ctx,
		state: &StreamingState{
			MessageID:    fmt.Sprintf("msg_%x", time.Now().UnixNano()),
			ClosedBlocks: make(map[int]bool),
		},
	}, nil
}

// SendMessageStart sends the initial message_start event
func (h *AnthropicStreamHandler) SendMessageStart() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	messageData := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            h.state.MessageID,
			"type":          "message",
			"role":          "assistant",
			"model":         h.originalModel,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":                h.state.InputTokens,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
				"output_tokens":               0,
			},
		},
	}
	return h.sse.WriteJSON("message_start", messageData)
}

// SendContentBlockStart sends a content_block_start event
func (h *AnthropicStreamHandler) SendContentBlockStart(index int, blockType string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	contentBlock := map[string]interface{}{
		"type": blockType,
	}

	if blockType == "text" {
		contentBlock["text"] = ""
	}

	blockData := map[string]interface{}{
		"type":          "content_block_start",
		"index":         index,
		"content_block": contentBlock,
	}
	return h.sse.WriteJSON("content_block_start", blockData)
}

// SendTextDelta sends a text delta
func (h *AnthropicStreamHandler) SendTextDelta(text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.state.TextBlockClosed {
		logger.From(h.ctx).Debugf("SendTextDelta called but text block is closed, ignoring %d chars", len(text))
		return nil
	}

	h.state.TextSent = true
	h.state.AccumulatedText += text

	deltaData := map[string]interface{}{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": text,
		},
	}
	if err := h.sse.WriteJSON("content_block_delta", deltaData); err != nil {
		logger.From(h.ctx).Errorf("Failed to write text delta: %v", err)
		return err
	}
	return nil
}

// SendToolUseStart sends a tool_use block start
func (h *AnthropicStreamHandler) SendToolUseStart(index int, toolID, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Track the tool call (only for real streaming, not simulated)
	if !h.simulatedStreaming {
		h.state.ToolCalls = append(h.state.ToolCalls, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    toolID,
			Name:  name,
			Input: make(map[string]interface{}),
		})
	}

	blockData := map[string]interface{}{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]interface{}{
			"type":  "tool_use",
			"id":    toolID,
			"name":  name,
			"input": map[string]interface{}{},
		},
	}
	if err := h.sse.WriteJSON("content_block_start", blockData); err != nil {
		logger.From(h.ctx).Errorf("Failed to write tool_use start for %s: %v", name, err)
		return err
	}
	return nil
}

// SendToolInputDelta sends tool input delta
func (h *AnthropicStreamHandler) SendToolInputDelta(index int, partialJSON string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// For simulated streaming, we accumulate the partialJSON to update tool calls
	// Note: partialJSON may be incomplete during streaming
	if !h.simulatedStreaming && len(h.state.ToolCalls) > 0 {
		// The last tool call is the current one
		lastToolIndex := len(h.state.ToolCalls) - 1
		if lastToolIndex >= 0 {
			// For real streaming, we'd need to accumulate partialJSON
			// and parse when complete. For now, we skip partial updates.
			logger.From(h.ctx).Debugf("%s", "  Real streaming: would accumulate partial JSON")
		}
	}

	deltaData := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": partialJSON,
		},
	}
	if err := h.sse.WriteJSON("content_block_delta", deltaData); err != nil {
		logger.From(h.ctx).Errorf("Failed to write input_json_delta for index %d: %v", index, err)
		return err
	}
	return nil
}

// SendContentBlockStop sends a content_block_stop event
func (h *AnthropicStreamHandler) SendContentBlockStop(index int) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Check if already closed
	if h.state.ClosedBlocks[index] {
		return nil
	}

	stopData := map[string]interface{}{
		"type":  "content_block_stop",
		"index": index,
	}
	if err := h.sse.WriteJSON("content_block_stop", stopData); err != nil {
		logger.From(h.ctx).Errorf("Failed to write content_block_stop for index %d: %v", index, err)
		return err
	}

	// Mark as closed
	h.state.ClosedBlocks[index] = true
	return nil
}

// SendPing sends a ping event
func (h *AnthropicStreamHandler) SendPing() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.sse.WriteJSON("ping", map[string]string{"type": "ping"})
}

// SendMessageDelta sends a message_delta event
func (h *AnthropicStreamHandler) SendMessageDelta(stopReason string, outputTokens int) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// The message_delta should include both input and output tokens in the usage field
	deltaData := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"input_tokens":  h.state.InputTokens,
			"output_tokens": outputTokens,
		},
	}
	return h.sse.WriteJSON("message_delta", deltaData)
}

// SendMessageStop sends a message_stop event
func (h *AnthropicStreamHandler) SendMessageStop() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.sse.WriteJSON("message_stop", map[string]string{"type": "message_stop"})
}

// SendDone sends the final [DONE] marker
func (h *AnthropicStreamHandler) SendDone() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.sse.WriteEvent("", "[DONE]")
}

// Complete sends the completion sequence
func (h *AnthropicStreamHandler) Complete(stopReason string) error {
	// Note: This method calls other methods that acquire the mutex,
	// so we cannot hold the mutex for the entire method to avoid deadlock.
	// We only lock when directly accessing state.

	h.mu.Lock()
	needToCloseText := !h.state.TextBlockClosed && (h.state.TextSent || h.state.AccumulatedText != "")
	accumulatedText := h.state.AccumulatedText
	textSent := h.state.TextSent
	toolIndex := h.state.ToolIndex
	lastToolIndex := h.state.LastToolIndex
	simulatedStreaming := h.simulatedStreaming
	toolCallsLen := len(h.state.ToolCalls)
	h.mu.Unlock()

	// Close any open blocks
	if needToCloseText {
		if accumulatedText != "" && !textSent {
			// Send accumulated text
			if err := h.SendTextDelta(accumulatedText); err != nil {
				logger.From(h.ctx).Errorf("Failed to send accumulated text: %v", err)
				return err
			}
		}
		if err := h.SendContentBlockStop(0); err != nil {
			logger.From(h.ctx).Errorf("Failed to close text block: %v", err)
			return err
		}
		h.mu.Lock()
		h.state.TextBlockClosed = true
		h.mu.Unlock()
	}

	// Close tool blocks
	if toolIndex != nil {
		for i := 1; i <= lastToolIndex; i++ {
			if err := h.SendContentBlockStop(i); err != nil {
				logger.From(h.ctx).Errorf("Failed to close tool block %d: %v", i, err)
				return err
			}
		}
	}

	// Log what we've accumulated (this is NOT sent to client, just for debugging)
	if simulatedStreaming {
		h.mu.Lock()
		accTextLen := len(h.state.AccumulatedText)
		h.mu.Unlock()
		logger.From(h.ctx).Debugf("Stream complete: stop_reason=%s, text=%d chars, tools=%d", stopReason, accTextLen, toolCallsLen)
	}

	// Send completion events
	h.mu.Lock()
	outputTokens := h.state.OutputTokens
	h.mu.Unlock()

	if err := h.SendMessageDelta(stopReason, outputTokens); err != nil {
		logger.From(h.ctx).Errorf("Failed to send message_delta: %v", err)
		return err
	}

	if err := h.SendMessageStop(); err != nil {
		logger.From(h.ctx).Errorf("Failed to send message_stop: %v", err)
		return err
	}

	if err := h.SendDone(); err != nil {
		logger.From(h.ctx).Errorf("Failed to send [DONE]: %v", err)
		return err
	}

	return nil
}

// SendError sends an error event
func (h *AnthropicStreamHandler) SendError(message string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.sse.WriteJSON("error", map[string]string{
		"type":    "error",
		"message": message,
	})
}

// Close ensures the stream is properly closed
func (h *AnthropicStreamHandler) Close() error {
	// No-op for now as the underlying ResponseWriter is managed by the HTTP server
	// This method exists to satisfy the interface and for future extensions
	return nil
}

// UpdateModel updates the model in the handler state
func (h *AnthropicStreamHandler) UpdateModel(model string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.originalModel = model
}

// SetStopReason sets the stop reason in the handler state
func (h *AnthropicStreamHandler) SetStopReason(stopReason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Store for later use in Complete
	h.state.HasSentStopReason = true
}

// SetUsage sets the token usage in the handler state
func (h *AnthropicStreamHandler) SetUsage(inputTokens, outputTokens int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.InputTokens = inputTokens
	h.state.OutputTokens = outputTokens
}

// SendMessage sends a complete message (for simulated streaming)
func (h *AnthropicStreamHandler) SendMessage(message string) error {
	// Send message start
	if err := h.SendMessageStart(); err != nil {
		return err
	}

	// Send content block start
	if err := h.SendContentBlockStart(0, "text"); err != nil {
		return err
	}

	// Send the message as text delta
	if err := h.SendTextDelta(message); err != nil {
		return err
	}

	// Send content block stop
	if err := h.SendContentBlockStop(0); err != nil {
		return err
	}

	// Complete the stream
	return h.Complete("end_turn")
}

// SendEvent sends a generic event with JSON data
func (h *AnthropicStreamHandler) SendEvent(eventType string, data interface{}) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.sse.WriteJSON(eventType, data)
}

// OpenAIStreamParser parses OpenAI streaming responses
// Note: This parser doesn't need an explicit context parameter because
// context cancellation is handled via the response body reader. When the
// HTTP request context is cancelled, the body is closed, causing Parse()
// to exit cleanly when scanner.Scan() returns false.
type OpenAIStreamParser struct {
	handler *AnthropicStreamHandler
}

// NewOpenAIStreamParser creates a new OpenAI stream parser
func NewOpenAIStreamParser(handler *AnthropicStreamHandler) *OpenAIStreamParser {
	return &OpenAIStreamParser{handler: handler}
}

// Parse parses an OpenAI streaming response
func (p *OpenAIStreamParser) Parse(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse SSE data
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			// Check for end marker
			if data == "[DONE]" {
				return p.handler.Complete("end_turn")
			}

			// Parse JSON chunk
			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				logger.From(p.handler.ctx).Errorf("Failed to parse OpenAI stream chunk: %v", err)
				continue // Skip invalid JSON but log the error
			}

			// Log the received chunk
			if logger.From(p.handler.ctx).IsDebugEnabled() {
				logger.DebugJSON(logger.From(p.handler.ctx), "OpenAI Stream Chunk", chunk)
			}

			// Process the chunk
			if err := p.processChunk(chunk); err != nil {
				logger.From(p.handler.ctx).Errorf("Failed to process OpenAI stream chunk: %v", err)
				// Send error event to client
				if err := p.handler.sse.WriteJSON("error", map[string]string{
					"type":    "error",
					"message": fmt.Sprintf("Stream processing error: %v", err),
				}); err != nil {
					logger.From(p.handler.ctx).Errorf("Failed to write error event: %v", err)
				}
				return err
			}
		}
	}

	return scanner.Err()
}

// processChunk processes a single streaming chunk
func (p *OpenAIStreamParser) processChunk(chunk map[string]interface{}) error {
	// Check for usage data in the chunk (OpenAI sometimes sends this)
	if usage, ok := chunk["usage"].(map[string]interface{}); ok {
		if promptTokens, ok := usage["prompt_tokens"].(float64); ok {
			p.handler.state.InputTokens = int(promptTokens)
		}
		if completionTokens, ok := usage["completion_tokens"].(float64); ok {
			p.handler.state.OutputTokens = int(completionTokens)
		}
	}

	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return nil
	}

	choice := choices[0].(map[string]interface{})

	// Check for finish reason
	if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
		stopReason := "end_turn"
		switch finishReason {
		case "length":
			stopReason = "max_tokens"
		case "tool_calls", "function_call":
			stopReason = "tool_use"
		}
		return p.handler.Complete(stopReason)
	}

	// Process delta
	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Handle text content
	if content, ok := delta["content"].(string); ok && content != "" {
		if err := p.handler.SendTextDelta(content); err != nil {
			return err
		}
		// Note: OpenAI provides actual token counts in the usage field,
		// so we don't need to estimate tokens here
	}

	// Handle tool calls
	if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
		for _, tc := range toolCalls {
			toolCall := tc.(map[string]interface{})

			// Get tool call index
			index := 0
			if idx, ok := toolCall["index"].(float64); ok {
				index = int(idx)
			}

			// Check if this is a new tool
			if p.handler.state.ToolIndex == nil || index != *p.handler.state.ToolIndex {
				// Close text block if needed
				if !p.handler.state.TextBlockClosed {
					if p.handler.state.AccumulatedText != "" && !p.handler.state.TextSent {
						_ = p.handler.SendTextDelta(p.handler.state.AccumulatedText)
					}
					_ = p.handler.SendContentBlockStop(0)
					p.handler.state.TextBlockClosed = true
				}

				// Start new tool block
				p.handler.state.ToolIndex = &index
				p.handler.state.LastToolIndex++

				function := toolCall["function"].(map[string]interface{})
				name := function["name"].(string)
				toolID := fmt.Sprintf("toolu_%x", time.Now().UnixNano())
				if id, ok := toolCall["id"].(string); ok {
					toolID = id
				}

				_ = p.handler.SendToolUseStart(p.handler.state.LastToolIndex, toolID, name)
			}

			// Handle arguments
			if function, ok := toolCall["function"].(map[string]interface{}); ok {
				if args, ok := function["arguments"].(string); ok && args != "" {
					_ = p.handler.SendToolInputDelta(p.handler.state.LastToolIndex, args)
				}
			}
		}
	}

	return nil
}

// GoogleStreamParser parses Google AI streaming responses
type GoogleStreamParser struct {
	handler *AnthropicStreamHandler
}

// NewGoogleStreamParser creates a new Google AI stream parser
func NewGoogleStreamParser(handler *AnthropicStreamHandler) *GoogleStreamParser {
	return &GoogleStreamParser{handler: handler}
}

// Parse parses a Google AI streaming response
func (p *GoogleStreamParser) Parse(reader io.Reader) error {
	decoder := json.NewDecoder(reader)

	for {
		var chunk map[string]interface{}
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Log the received chunk
		if logger.From(p.handler.ctx).IsDebugEnabled() {
			logger.DebugJSON(logger.From(p.handler.ctx), "Google Stream Chunk", chunk)
		}

		// Process the chunk
		if err := p.processChunk(chunk); err != nil {
			logger.From(p.handler.ctx).Errorf("%s: %v", "Failed to process Google stream chunk", err)
			// Send error event to client
			if err := p.handler.sse.WriteJSON("error", map[string]string{
				"type":    "error",
				"message": fmt.Sprintf("Stream processing error: %v", err),
			}); err != nil {
				logger.From(p.handler.ctx).Errorf("%s: %v", "Failed to write error event", err)
			}
			return err
		}
	}

	return p.handler.Complete("end_turn")
}

// processChunk processes a single Google AI streaming chunk
func (p *GoogleStreamParser) processChunk(chunk map[string]interface{}) error {
	// Check for usage metadata in the chunk (Google may send this)
	if usageMetadata, ok := chunk["usageMetadata"].(map[string]interface{}); ok {
		if promptTokens, ok := usageMetadata["promptTokenCount"].(float64); ok {
			p.handler.state.InputTokens = int(promptTokens)
		}
		if candidatesTokens, ok := usageMetadata["candidatesTokenCount"].(float64); ok {
			p.handler.state.OutputTokens = int(candidatesTokens)
		}
	}

	candidates, ok := chunk["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return nil
	}

	candidate := candidates[0].(map[string]interface{})

	// Check finish reason
	if finishReason, ok := candidate["finishReason"].(string); ok && finishReason != "" {
		stopReason := "end_turn"
		if finishReason == "MAX_TOKENS" {
			stopReason = "max_tokens"
		}
		return p.handler.Complete(stopReason)
	}

	// Process content
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return nil
	}

	parts, ok := content["parts"].([]interface{})
	if !ok {
		return nil
	}

	for _, part := range parts {
		partMap := part.(map[string]interface{})

		// Handle text
		if text, ok := partMap["text"].(string); ok && text != "" {
			if err := p.handler.SendTextDelta(text); err != nil {
				return err
			}
			// Note: Google provides actual token counts in the usageMetadata field,
			// so we don't need to estimate tokens here
		}

		// Handle function calls
		if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
			// Close text block if needed
			if !p.handler.state.TextBlockClosed {
				if p.handler.state.AccumulatedText != "" && !p.handler.state.TextSent {
					_ = p.handler.SendTextDelta(p.handler.state.AccumulatedText)
				}
				_ = p.handler.SendContentBlockStop(0)
				p.handler.state.TextBlockClosed = true
			}

			// Start tool block
			p.handler.state.LastToolIndex++
			name := functionCall["name"].(string)
			toolID := fmt.Sprintf("toolu_%x", time.Now().UnixNano())

			_ = p.handler.SendToolUseStart(p.handler.state.LastToolIndex, toolID, name)

			// Send arguments
			if args, ok := functionCall["args"].(map[string]interface{}); ok {
				argsJSON, _ := json.Marshal(args)
				_ = p.handler.SendToolInputDelta(p.handler.state.LastToolIndex, string(argsJSON))
			}

			// Close tool block
			_ = p.handler.SendContentBlockStop(p.handler.state.LastToolIndex)
		}
	}

	return nil
}

// ArgoStreamParser handles Argo streaming responses
type ArgoStreamParser struct {
	handler *AnthropicStreamHandler
}

// NewArgoStreamParser creates a new Argo stream parser
func NewArgoStreamParser(handler *AnthropicStreamHandler) *ArgoStreamParser {
	return &ArgoStreamParser{handler: handler}
}

// Parse parses an Argo streaming response
func (p *ArgoStreamParser) Parse(reader io.Reader) error {
	return p.ParseWithPingInterval(reader, constants.DefaultPingInterval*time.Second)
}

// ParseWithPingInterval parses an Argo streaming response with configurable ping interval
func (p *ArgoStreamParser) ParseWithPingInterval(reader io.Reader, pingInterval time.Duration) error {
	// Argo streams plain text, so we just forward it
	buffer := make([]byte, 1024)
	lastActivity := time.Now()
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	// Channel to signal when data is received
	dataChan := make(chan []byte, 1)
	errChan := make(chan error, 1)

	// Start goroutine to read data
	go func() {
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				// Copy the data to avoid race conditions
				data := make([]byte, n)
				copy(data, buffer[:n])
				select {
				case dataChan <- data:
				default:
					// If channel is full, drop old data and send new
					<-dataChan
					dataChan <- data
				}
			}
			if err != nil {
				errChan <- err
				return
			}
		}
	}()

	for {
		select {
		case data := <-dataChan:
			lastActivity = time.Now()
			text := string(data)
			// Log the received text chunk
			logger.From(p.handler.ctx).Debugf("Argo Stream Chunk: %q", text)
			if err := p.handler.SendTextDelta(text); err != nil {
				logger.From(p.handler.ctx).Errorf("%s: %v", "Failed to send Argo text delta", err)
				// Send error event to client
				if err := p.handler.sse.WriteJSON("error", map[string]string{
					"type":    "error",
					"message": fmt.Sprintf("Stream processing error: %v", err),
				}); err != nil {
					logger.From(p.handler.ctx).Errorf("%s: %v", "Failed to write error event", err)
				}
				return err
			}
			// Argo doesn't provide token counts, so we must estimate them
			p.handler.state.OutputTokens += EstimateTokenCount(text)

		case err := <-errChan:
			if err == io.EOF {
				return p.handler.Complete("end_turn")
			}
			return err

		case <-pingTicker.C:
			// Only send ping if we haven't received data recently
			if time.Since(lastActivity) >= pingInterval {
				logger.From(p.handler.ctx).Debugf("Sending ping after %v of inactivity", time.Since(lastActivity))
				if err := p.handler.SendPing(); err != nil {
					logger.From(p.handler.ctx).Errorf("%s: %v", "Failed to send ping during Argo streaming", err)
					return fmt.Errorf("client disconnected: %w", err)
				}
			}
		}
	}
}
