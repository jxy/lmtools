package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"net/http"
	"strings"
	"time"
)

// Helper functions to reduce duplication in stream handlers

// startTextStream sends initial stream events for text content
func startTextStream(handler *AnthropicStreamHandler) error {
	if err := handler.SendMessageStart(); err != nil {
		return err
	}
	return handler.SendContentBlockStart(0, "text")
}

// streamFromOpenAI handles streaming from OpenAI API
func (s *Server) streamFromOpenAI(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	// Convert to OpenAI format with streaming enabled
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return errors.WrapError("convert to OpenAI format", err)
	}
	openAIReq.Stream = true

	// Marshal request
	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		return errors.WrapError("marshal OpenAI request", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", s.config.OpenAIURL, bytes.NewReader(reqBody))
	if err != nil {
		return errors.WrapError("create OpenAI request", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")
	if key := s.config.OpenAIAPIKey; key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	// Send request
	resp, err := s.client.Do(ctx, req, "openai")
	if err != nil {
		return errors.WrapError("send OpenAI request", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		s.logErrorResponse(ctx, "openai", resp.StatusCode, body)
		return NewResponseError(resp.StatusCode, string(body))
	}

	// Send initial events
	if err := startTextStream(handler); err != nil {
		return err
	}

	// Parse OpenAI SSE stream
	parser := NewOpenAIStreamParser(handler)
	return parser.Parse(resp.Body)
}

// streamFromGoogle handles streaming from Google Gemini API
func (s *Server) streamFromGoogle(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	// Convert to Google format
	googleReq, err := s.converter.ConvertAnthropicToGoogle(ctx, anthReq)
	if err != nil {
		return errors.WrapError("convert to Google format", err)
	}

	// Marshal request
	reqBody, err := json.Marshal(googleReq)
	if err != nil {
		return errors.WrapError("marshal Google request", err)
	}

	// Construct streaming URL with model
	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent", s.config.GoogleURL, anthReq.Model)

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return errors.WrapError("create Google request", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")

	// Apply API key
	if key := s.config.GoogleAPIKey; key != "" {
		if err := auth.ApplyGoogleAPIKey(req, key); err != nil {
			return errors.WrapError("apply Google API key", err)
		}
	}

	// Send request
	resp, err := s.client.Do(ctx, req, "google")
	if err != nil {
		return errors.WrapError("send Google request", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		s.logErrorResponse(ctx, "google", resp.StatusCode, body)
		return NewResponseError(resp.StatusCode, string(body))
	}

	// Send initial events
	if err := startTextStream(handler); err != nil {
		return err
	}

	// Parse Google stream
	parser := NewGoogleStreamParser(handler)
	return parser.Parse(resp.Body)
}

// streamFromArgo handles streaming from Argo API
func (s *Server) streamFromArgo(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	log := logger.From(ctx)

	// Check if tools are present
	hasTools := len(anthReq.Tools) > 0

	// Get ping interval from config, or use default
	pingInterval := s.config.PingInterval
	if pingInterval <= 0 {
		pingInterval = constants.DefaultPingInterval * time.Second
	}
	pingInterval = s.validatePingInterval(ctx, pingInterval)

	if hasTools {
		// Argo doesn't support streaming with tools, simulate it
		log.Infof("Tools present - using simulated streaming with pings (interval: %v)", pingInterval)
		return s.streamFromArgoWithPings(ctx, anthReq, handler, pingInterval)
	}

	// No tools - use real streaming endpoint with pings
	log.Infof("No tools - using real Argo streaming endpoint with pings (interval: %v)", pingInterval)

	// Get streaming response from Argo
	streamBody, err := s.forwardToArgoStream(ctx, anthReq)
	if err != nil {
		return err
	}
	defer streamBody.Close()

	// Send initial events
	if err := startTextStream(handler); err != nil {
		return err
	}

	// Use ArgoStreamParser with ping interval to handle the plain text stream
	parser := NewArgoStreamParser(handler)
	// The parser handles sending all closing events (message_stop, [DONE]) when EOF is reached
	return parser.ParseWithPingInterval(streamBody, pingInterval)
}

// streamFromArgoWithPings handles streaming simulation when tools are present
// Since Argo doesn't support streaming with tools, we call the non-streaming endpoint
// and simulate streaming while sending pings to keep the connection alive
func (s *Server) streamFromArgoWithPings(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	log := logger.From(ctx)

	// Log the streaming request
	logger.DebugJSON(log, "Outgoing Argo Request (simulated streaming with tools)", anthReq)

	// Forward to Argo's non-streaming endpoint while sending pings
	log.Debugf("Waiting for Argo response with pings every %v", pingInterval)
	argoResp, err := s.waitForArgoResponseWithPings(ctx, anthReq, handler, pingInterval)
	if err != nil {
		return err
	}

	// Convert to Anthropic format
	anthResp := s.converter.ConvertArgoToAnthropicWithRequest(argoResp, anthReq.Model, anthReq)

	// Update model in handler
	handler.UpdateModel(anthResp.Model)

	// Send initial stream events
	if err := startTextStream(handler); err != nil {
		return err
	}

	// Stream the content (simulate streaming by chunking the response)
	if err := s.streamArgoResponseContent(ctx, anthResp, handler); err != nil {
		return err
	}

	// Set final metadata
	handler.SetStopReason(anthResp.StopReason)
	if anthResp.Usage != nil {
		handler.SetUsage(anthResp.Usage.InputTokens, anthResp.Usage.OutputTokens)
	}

	// Send message stop
	if err := handler.SendMessageStop(); err != nil {
		return err
	}

	// Send [DONE] marker to properly close the stream
	return handler.SendDone()
}

// streamFromAnthropic handles streaming from Anthropic API
func (s *Server) streamFromAnthropic(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	// Enable streaming
	anthReq.Stream = true

	// Marshal request
	reqBody, err := json.Marshal(anthReq)
	if err != nil {
		return errors.WrapError("marshal Anthropic request", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", s.config.AnthropicURL+"/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return errors.WrapError("create Anthropic request", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	if key := s.config.AnthropicAPIKey; key != "" {
		req.Header.Set("x-api-key", key)
	}

	// Send request
	resp, err := s.client.Do(ctx, req, "anthropic")
	if err != nil {
		return errors.WrapError("send Anthropic request", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		s.logErrorResponse(ctx, "anthropic", resp.StatusCode, body)
		return NewResponseError(resp.StatusCode, string(body))
	}

	// Parse Anthropic SSE stream directly
	return s.parseAnthropicStream(resp.Body, handler)
}

// parseAnthropicStream parses Anthropic's SSE format
func (s *Server) parseAnthropicStream(body io.Reader, handler *AnthropicStreamHandler) error {
	log := logger.From(handler.ctx)
	scanner := bufio.NewScanner(body)
	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		// Handle event lines
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		// Handle data lines
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			// Parse based on event type
			switch currentEvent {
			case "message_start":
				var evt MessageStartEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					handler.UpdateModel(evt.Message.Model)
					// Forward the event to the client
					if err := handler.SendEvent("message_start", evt); err != nil {
						return err
					}
				}

			case "content_block_start":
				var evt ContentBlockStartEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Log the content block start event
					log.Debugf("Content block start: type=%s", evt.ContentBlock.Type)
					// Forward the event to the client
					if err := handler.SendEvent("content_block_start", evt); err != nil {
						return err
					}
				}

			case "content_block_delta":
				var evt ContentBlockDeltaEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// If it's a text_delta, accumulate the text
					if evt.Delta.Type == "text_delta" {
						handler.state.AccumulatedText += evt.Delta.Text
					}
					// Forward the event to the client
					if err := handler.SendEvent("content_block_delta", evt); err != nil {
						return err
					}
				}

			case "content_block_stop":
				// Block completed
				var evt ContentBlockStopEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Forward the event to the client
					if err := handler.SendEvent("content_block_stop", evt); err != nil {
						return err
					}
				}

			case "message_delta":
				var evt MessageDeltaEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					if evt.Delta.StopReason != "" {
						handler.SetStopReason(evt.Delta.StopReason)
					}
					if evt.Usage != nil {
						handler.SetUsage(evt.Usage.InputTokens, evt.Usage.OutputTokens)
					}
					// Forward the event to the client
					if err := handler.SendEvent("message_delta", evt); err != nil {
						return err
					}
				}

			case "message_stop":
				// Stream completed
				var evt MessageStopEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Forward the event to the client
					if err := handler.SendEvent("message_stop", evt); err != nil {
						return err
					}
				}
				return nil

			case "error":
				var evt ErrorEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Forward the error event to the client
					if err := handler.SendEvent("error", evt); err != nil {
						return err
					}
					return fmt.Errorf("%s: %s", evt.Error.Type, evt.Error.Message)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return errors.WrapError("read stream", err)
	}

	return nil
}

// validatePingInterval ensures the ping interval is within acceptable bounds
func (s *Server) validatePingInterval(ctx context.Context, pingInterval time.Duration) time.Duration {
	log := logger.From(ctx)

	if pingInterval < minPingInterval {
		log.Warnf("Ping interval %v is below minimum %v, using minimum", pingInterval, minPingInterval)
		return minPingInterval
	}

	if pingInterval > maxPingInterval {
		log.Warnf("Ping interval %v exceeds maximum %v, using maximum", pingInterval, maxPingInterval)
		return maxPingInterval
	}

	return pingInterval
}

// waitForArgoResponseWithPings waits for Argo response while sending pings
func (s *Server) waitForArgoResponseWithPings(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) (*ArgoChatResponse, error) {
	log := logger.From(ctx)

	// Create response channel
	type result struct {
		resp *ArgoChatResponse
		err  error
	}
	respChan := make(chan result, 1)

	// Start request in background
	go func() {
		resp, err := s.forwardToArgo(ctx, anthReq)
		respChan <- result{resp, err}
	}()

	// Create ticker for pings
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	// Send pings until response arrives
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case res := <-respChan:
			return res.resp, res.err

		case <-ticker.C:
			if err := handler.SendPing(); err != nil {
				log.Warnf("Failed to send ping: %v", err)
			}
		}
	}
}

// streamArgoResponseContent streams the content from an Argo response
func (s *Server) streamArgoResponseContent(ctx context.Context, anthResp *AnthropicResponse, handler *AnthropicStreamHandler) error {
	// Stream each content block
	for i, block := range anthResp.Content {
		switch block.Type {
		case "text":
			if err := s.streamTextBlock(block.Text, i, handler); err != nil {
				return err
			}

		case "tool_use":
			if err := s.streamToolBlock(ctx, block, i, handler); err != nil {
				return err
			}
		}
	}

	return nil
}

// streamTextBlock streams a text content block
func (s *Server) streamTextBlock(content string, blockIndex int, handler *AnthropicStreamHandler) error {
	// Send content block start event
	blockStart := ContentBlockStartEvent{
		Type:  "content_block_start",
		Index: blockIndex,
		ContentBlock: AnthropicContentBlock{
			Type: "text",
			Text: "",
		},
	}
	if err := handler.SendEvent("content_block_start", blockStart); err != nil {
		return err
	}

	// Determine chunk size based on content length
	chunkSize := constants.DefaultTextChunkSize
	if len(content) > 1000 {
		chunkSize = constants.DefaultTextChunkSize * 2 // Larger chunks for longer content
	}

	// Use splitTextForStreaming to respect UTF-8 boundaries
	chunks := splitTextForStreaming(handler.ctx, content, chunkSize)

	// Stream each chunk
	for _, chunk := range chunks {
		// Send as content_block_delta event
		delta := ContentBlockDeltaEvent{
			Type:  "content_block_delta",
			Index: blockIndex,
			Delta: DeltaContent{
				Type: "text_delta",
				Text: chunk,
			},
		}
		if err := handler.SendEvent("content_block_delta", delta); err != nil {
			return err
		}
	}

	// Send content block stop event
	blockStop := ContentBlockStopEvent{
		Type:  "content_block_stop",
		Index: blockIndex,
	}
	if err := handler.SendEvent("content_block_stop", blockStop); err != nil {
		return err
	}

	return nil
}

// streamToolBlock streams a tool use content block
func (s *Server) streamToolBlock(ctx context.Context, block AnthropicContentBlock, blockIndex int, handler *AnthropicStreamHandler) error {
	log := logger.From(ctx)

	// Send tool use start event
	toolStart := ContentBlockStartEvent{
		Type:  "content_block_start",
		Index: blockIndex,
		ContentBlock: AnthropicContentBlock{
			ID:   block.ID,
			Type: "tool_use",
			Name: block.Name,
		},
	}

	if err := handler.SendEvent("content_block_start", toolStart); err != nil {
		return err
	}

	// Stream the input JSON in chunks
	inputJSON, err := json.Marshal(block.Input)
	if err != nil {
		return err
	}
	inputStr := string(inputJSON)
	chunkSize := constants.DefaultJSONChunkSize

	// Use splitTextForStreaming to respect UTF-8 boundaries in JSON content
	chunks := splitTextForStreaming(ctx, inputStr, chunkSize)

	// Stream each chunk
	for _, chunk := range chunks {
		delta := ContentBlockDeltaEvent{
			Type:  "content_block_delta",
			Index: blockIndex,
			Delta: DeltaContent{
				Type:        "input_json_delta",
				PartialJSON: chunk,
			},
		}

		if err := handler.SendEvent("content_block_delta", delta); err != nil {
			return err
		}
	}

	// Send tool use stop event
	if err := handler.SendEvent("content_block_stop", ContentBlockStopEvent{
		Type:  "content_block_stop",
		Index: blockIndex,
	}); err != nil {
		return err
	}

	log.Debugf("Streamed tool use block: %s", block.Name)
	return nil
}

// simulateStreamingFromArgoWithInterval simulates streaming with a specific interval
func (s *Server) simulateStreamingFromArgoWithInterval(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	log := logger.From(ctx)

	// If ping interval specified, wait with pings
	var argoResp *ArgoChatResponse
	var err error

	if pingInterval > 0 {
		log.Debugf("Waiting for Argo response with pings every %v", pingInterval)
		// Log the streaming request
		logger.DebugJSON(log, "Outgoing Argo Streaming Request", anthReq)
		argoResp, err = s.waitForArgoResponseWithPings(ctx, anthReq, handler, pingInterval)
	} else {
		// Get non-streaming response from Argo
		argoResp, err = s.forwardToArgo(ctx, anthReq)
	}

	if err != nil {
		return err
	}

	// Convert to Anthropic format
	anthResp := s.converter.ConvertArgoToAnthropicWithRequest(argoResp, anthReq.Model, anthReq)

	// Update model in handler
	handler.UpdateModel(anthResp.Model)

	// Send initial stream events
	if err := startTextStream(handler); err != nil {
		return err
	}

	// Stream the content
	if err := s.streamArgoResponseContent(ctx, anthResp, handler); err != nil {
		return err
	}

	// Set final metadata
	handler.SetStopReason(anthResp.StopReason)
	if anthResp.Usage != nil {
		handler.SetUsage(anthResp.Usage.InputTokens, anthResp.Usage.OutputTokens)
	}

	// Send message stop
	if err := handler.SendMessageStop(); err != nil {
		return err
	}

	// Send [DONE] marker to properly close the stream
	return handler.SendDone()
}
