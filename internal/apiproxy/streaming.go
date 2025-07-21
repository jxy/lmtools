package apiproxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SSEWriter handles Server-Sent Events writing
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter creates a new SSE writer
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	LogDebug("Creating SSE writer")

	flusher, ok := w.(http.Flusher)
	if !ok {
		LogDebug(fmt.Sprintf("ResponseWriter type: %T does not implement http.Flusher", w))
		return nil, fmt.Errorf("streaming not supported (ResponseWriter type: %T)", w)
	}

	LogDebug("SSE writer created successfully with Flusher support")

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	return &SSEWriter{w: w, flusher: flusher}, nil
}

// WriteEvent writes an SSE event
func (s *SSEWriter) WriteEvent(eventType, data string) error {
	// Log the event being sent to client
	if eventType != "" {
		LogDebug(fmt.Sprintf("→ CLIENT: event: %s | data: %s", eventType, data))
	} else {
		LogDebug(fmt.Sprintf("→ CLIENT: data: %s", data))
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
	sse                *SSEWriter
	state              *StreamingState
	originalModel      string
	simulatedStreaming bool // If true, don't track tool calls in state
}

// NewAnthropicStreamHandler creates a new Anthropic stream handler
func NewAnthropicStreamHandler(w http.ResponseWriter, originalModel string) (*AnthropicStreamHandler, error) {
	sse, err := NewSSEWriter(w)
	if err != nil {
		return nil, err
	}

	return &AnthropicStreamHandler{
		sse:           sse,
		originalModel: originalModel,
		state: &StreamingState{
			MessageID:    fmt.Sprintf("msg_%x", time.Now().UnixNano()),
			ClosedBlocks: make(map[int]bool),
		},
	}, nil
}

// SendMessageStart sends the initial message_start event
func (h *AnthropicStreamHandler) SendMessageStart() error {
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
				"input_tokens":                0,
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
	if h.state.TextBlockClosed {
		LogDebug(fmt.Sprintf("SendTextDelta called but text block is closed, ignoring %d chars", len(text)))
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
		LogError("Failed to write text delta", err)
		return err
	}
	return nil
}

// SendToolUseStart sends a tool_use block start
func (h *AnthropicStreamHandler) SendToolUseStart(index int, toolID, name string) error {
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
		LogError(fmt.Sprintf("Failed to write tool_use start for %s", name), err)
		return err
	}
	return nil
}

// SendToolInputDelta sends tool input delta
func (h *AnthropicStreamHandler) SendToolInputDelta(index int, partialJSON string) error {
	// For simulated streaming, we accumulate the partialJSON to update tool calls
	// Note: partialJSON may be incomplete during streaming
	if !h.simulatedStreaming && len(h.state.ToolCalls) > 0 {
		// The last tool call is the current one
		lastToolIndex := len(h.state.ToolCalls) - 1
		if lastToolIndex >= 0 {
			// For real streaming, we'd need to accumulate partialJSON
			// and parse when complete. For now, we skip partial updates.
			LogDebug("  Real streaming: would accumulate partial JSON")
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
		LogError(fmt.Sprintf("Failed to write input_json_delta for index %d", index), err)
		return err
	}
	return nil
}

// SendContentBlockStop sends a content_block_stop event
func (h *AnthropicStreamHandler) SendContentBlockStop(index int) error {
	// Check if already closed
	if h.state.ClosedBlocks[index] {
		return nil
	}

	stopData := map[string]interface{}{
		"type":  "content_block_stop",
		"index": index,
	}
	if err := h.sse.WriteJSON("content_block_stop", stopData); err != nil {
		LogError(fmt.Sprintf("Failed to write content_block_stop for index %d", index), err)
		return err
	}

	// Mark as closed
	h.state.ClosedBlocks[index] = true
	return nil
}

// SendPing sends a ping event
func (h *AnthropicStreamHandler) SendPing() error {
	return h.sse.WriteJSON("ping", map[string]string{"type": "ping"})
}

// SendMessageDelta sends a message_delta event
func (h *AnthropicStreamHandler) SendMessageDelta(stopReason string, outputTokens int) error {
	deltaData := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": outputTokens,
		},
	}
	return h.sse.WriteJSON("message_delta", deltaData)
}

// SendMessageStop sends a message_stop event
func (h *AnthropicStreamHandler) SendMessageStop() error {
	return h.sse.WriteJSON("message_stop", map[string]string{"type": "message_stop"})
}

// SendDone sends the final [DONE] marker
func (h *AnthropicStreamHandler) SendDone() error {
	return h.sse.WriteEvent("", "[DONE]")
}

// Complete sends the completion sequence
func (h *AnthropicStreamHandler) Complete(stopReason string) error {
	// Close any open blocks
	if !h.state.TextBlockClosed && (h.state.TextSent || h.state.AccumulatedText != "") {
		if h.state.AccumulatedText != "" && !h.state.TextSent {
			// Send accumulated text
			if err := h.SendTextDelta(h.state.AccumulatedText); err != nil {
				LogError("Failed to send accumulated text", err)
				return err
			}
		}
		if err := h.SendContentBlockStop(0); err != nil {
			LogError("Failed to close text block", err)
			return err
		}
		h.state.TextBlockClosed = true
	}

	// Close tool blocks
	if h.state.ToolIndex != nil {
		for i := 1; i <= h.state.LastToolIndex; i++ {
			if err := h.SendContentBlockStop(i); err != nil {
				LogError(fmt.Sprintf("Failed to close tool block %d", i), err)
				return err
			}
		}
	}

	// Log what we've accumulated (this is NOT sent to client, just for debugging)
	if h.simulatedStreaming {
		LogDebug(fmt.Sprintf("Stream complete: stop_reason=%s, text=%d chars, tools=%d", stopReason, len(h.state.AccumulatedText), len(h.state.ToolCalls)))
	}

	// Send completion events
	if err := h.SendMessageDelta(stopReason, h.state.OutputTokens); err != nil {
		LogError("Failed to send message_delta", err)
		return err
	}

	if err := h.SendMessageStop(); err != nil {
		LogError("Failed to send message_stop", err)
		return err
	}

	if err := h.SendDone(); err != nil {
		LogError("Failed to send [DONE]", err)
		return err
	}

	return nil
}

// OpenAIStreamParser parses OpenAI streaming responses
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
				LogError("Failed to parse OpenAI stream chunk", err)
				continue // Skip invalid JSON but log the error
			}

			// Log the received chunk
			LogJSON("OpenAI Stream Chunk", chunk)

			// Process the chunk
			if err := p.processChunk(chunk); err != nil {
				LogError("Failed to process OpenAI stream chunk", err)
				// Send error event to client
				if err := p.handler.sse.WriteJSON("error", map[string]string{
					"type":    "error",
					"message": fmt.Sprintf("Stream processing error: %v", err),
				}); err != nil {
					LogError("Failed to write error event", err)
				}
				return err
			}
		}
	}

	return scanner.Err()
}

// processChunk processes a single streaming chunk
func (p *OpenAIStreamParser) processChunk(chunk map[string]interface{}) error {
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
		p.handler.state.OutputTokens += len(content) / 4 // Rough estimate
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

// GeminiStreamParser parses Gemini streaming responses
type GeminiStreamParser struct {
	handler *AnthropicStreamHandler
}

// NewGeminiStreamParser creates a new Gemini stream parser
func NewGeminiStreamParser(handler *AnthropicStreamHandler) *GeminiStreamParser {
	return &GeminiStreamParser{handler: handler}
}

// Parse parses a Gemini streaming response
func (p *GeminiStreamParser) Parse(reader io.Reader) error {
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
		LogJSON("Gemini Stream Chunk", chunk)

		// Process the chunk
		if err := p.processChunk(chunk); err != nil {
			LogError("Failed to process Gemini stream chunk", err)
			// Send error event to client
			if err := p.handler.sse.WriteJSON("error", map[string]string{
				"type":    "error",
				"message": fmt.Sprintf("Stream processing error: %v", err),
			}); err != nil {
				LogError("Failed to write error event", err)
			}
			return err
		}
	}

	return p.handler.Complete("end_turn")
}

// processChunk processes a single Gemini streaming chunk
func (p *GeminiStreamParser) processChunk(chunk map[string]interface{}) error {
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
			p.handler.state.OutputTokens += len(text) / 4
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
	// Argo streams plain text, so we just forward it
	buffer := make([]byte, 1024)

	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			text := string(buffer[:n])
			// Log the received text chunk
			LogDebug(fmt.Sprintf("Argo Stream Chunk: %q", text))
			if err := p.handler.SendTextDelta(text); err != nil {
				LogError("Failed to send Argo text delta", err)
				// Send error event to client
				if err := p.handler.sse.WriteJSON("error", map[string]string{
					"type":    "error",
					"message": fmt.Sprintf("Stream processing error: %v", err),
				}); err != nil {
					LogError("Failed to write error event", err)
				}
				return err
			}
			p.handler.state.OutputTokens += len(text) / 4
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	return p.handler.Complete("end_turn")
}
