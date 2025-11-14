package proxy

import (
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
	if err := handler.SendMessageStart(); err != nil {
		return err
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
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
	if err := handler.SendMessageStart(); err != nil {
		return err
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
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
	if err := handler.SendMessageStart(); err != nil {
		return err
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		return err
	}

	// Ensure content_block_start is sent before deltas for each block
	// Event order: message_start → content_block_start → deltas → content_block_stop → message_stop
	parser := NewArgoStreamParser(handler)
	// The parser handles sending all closing events (message_stop) when EOF is reached
	return parser.ParseWithPingInterval(streamBody, pingInterval)
}

// streamFromArgoWithPings handles streaming simulation when tools are present
// Since Argo doesn't support streaming with tools, we call the non-streaming endpoint
// and simulate streaming while sending pings to keep the connection alive
func (s *Server) streamFromArgoWithPings(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	log := logger.From(ctx)

	// Send message_start before sending any pings while waiting for Argo
	if err := handler.SendMessageStart(); err != nil {
		return err
	}

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

	// Stream the content (simulate streaming by chunking the response)
	if err := s.streamArgoResponseContent(ctx, anthResp, handler); err != nil {
		return err
	}

	// Use the new FinishStream helper for consistent completion
	return handler.FinishStream(anthResp.StopReason, anthResp.Usage)
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
	scanner := NewSSEScanner(body)
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
			case EventMessageStart:
				var evt MessageStartEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					handler.UpdateModel(evt.Message.Model)
					// Forward the event to the client
					if err := handler.SendEvent(EventMessageStart, evt); err != nil {
						return err
					}
				}

			case EventContentBlockStart:
				var evt ContentBlockStartEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Log the content block start event
					log.Debugf("Content block start: type=%s", evt.ContentBlock.Type)
					// Forward the event to the client
					if err := handler.SendEvent(EventContentBlockStart, evt); err != nil {
						return err
					}
				}

			case EventContentBlockDelta:
				var evt ContentBlockDeltaEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// If it's a text_delta, accumulate the text
					if evt.Delta.Type == "text_delta" {
						handler.state.AccumulatedText += evt.Delta.Text
					}
					// Forward the event to the client
					if err := handler.SendEvent(EventContentBlockDelta, evt); err != nil {
						return err
					}
				}

			case EventContentBlockStop:
				// Block completed
				var evt ContentBlockStopEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Forward the event to the client
					if err := handler.SendEvent(EventContentBlockStop, evt); err != nil {
						return err
					}
				}

			case EventMessageDelta:
				var evt MessageDeltaEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					if evt.Delta.StopReason != "" {
						handler.SetStopReason(evt.Delta.StopReason)
					}
					if evt.Usage != nil {
						handler.SetUsage(evt.Usage.InputTokens, evt.Usage.OutputTokens)
					}
					// Forward the event to the client
					if err := handler.SendEvent(EventMessageDelta, evt); err != nil {
						return err
					}
				}

			case EventMessageStop:
				// Stream completed; ensure we sent a message_delta earlier
				var evt MessageStopEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Forward the event to the client
					if err := handler.SendEvent(EventMessageStop, evt); err != nil {
						return err
					}
				}
				return nil

			case EventError:
				var evt ErrorEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					// Forward the error event to the client
					if err := handler.SendEvent(EventError, evt); err != nil {
						return err
					}
					return fmt.Errorf("%s: %s", evt.Error.Type, evt.Error.Message)
				}

			default:
				// Log unknown event types for debugging
				log.Debugf("Unknown SSE event type: %s, data: %s", currentEvent, data)
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
	if err := handler.SendContentBlockStart(blockIndex, "text"); err != nil {
		return err
	}

	// Determine chunk size based on content length
	chunkSize := constants.DefaultTextChunkSize
	if len(content) > 1000 {
		chunkSize = constants.DefaultTextChunkSize * 2 // Larger chunks for longer content
	}

	// Use ContentSplitter for UTF-8 aware text chunking
	splitter := NewContentSplitter(handler.ctx, TextMode, chunkSize)
	chunks := splitter.Split(content)

	// Stream each chunk
	for _, chunk := range chunks {
		// Send as content_block_delta event
		if err := handler.SendTextDelta(chunk); err != nil {
			return err
		}
	}

	// Send content block stop event
	if err := handler.SendContentBlockStop(blockIndex); err != nil {
		return err
	}

	return nil
}

// streamToolBlock streams a tool use content block
func (s *Server) streamToolBlock(ctx context.Context, block AnthropicContentBlock, blockIndex int, handler *AnthropicStreamHandler) error {
	log := logger.From(ctx)

	// Send tool use start event with empty input field
	if err := handler.SendToolUseStart(blockIndex, block.ID, block.Name); err != nil {
		return err
	}

	// Send initial empty partial_json delta (Anthropic format requirement)
	if err := handler.SendToolInputDelta(blockIndex, ""); err != nil {
		return err
	}

	// Stream the input JSON in chunks
	inputJSON, err := json.Marshal(block.Input)
	if err != nil {
		return err
	}
	inputStr := string(inputJSON)
	chunkSize := constants.DefaultJSONChunkSize

	// Use ContentSplitter for JSON-aware chunking to respect UTF-8 and escape boundaries
	splitter := NewContentSplitter(ctx, JSONMode, chunkSize)
	chunks := splitter.Split(inputStr)

	// Stream each chunk
	for _, chunk := range chunks {
		if err := handler.SendToolInputDelta(blockIndex, chunk); err != nil {
			return err
		}
	}

	// Send tool use stop event
	if err := handler.SendContentBlockStop(blockIndex); err != nil {
		return err
	}

	// Log the full tool use block details
	if log.IsDebugEnabled() {
		toolJSON, _ := json.Marshal(block)
		log.Debugf("Streamed tool use block: %s", string(toolJSON))
	}
	return nil
}

// simulateStreamingFromArgoWithInterval simulates streaming with a specific interval
func (s *Server) simulateStreamingFromArgoWithInterval(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	log := logger.From(ctx)

	// Send message_start up front so clients receive it before any pings
	if err := handler.SendMessageStart(); err != nil {
		return err
	}

	// If ping interval specified, wait with pings
	var argoResp *ArgoChatResponse
	var err error

	if pingInterval > 0 {
		log.Debugf("Waiting for Argo response with pings every %v", pingInterval)
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

	// Stream the content
	if err := s.streamArgoResponseContent(ctx, anthResp, handler); err != nil {
		return err
	}

	// Use the new FinishStream helper for consistent completion
	return handler.FinishStream(anthResp.StopReason, anthResp.Usage)
}

// streamOpenAIFromAnthropic handles streaming from Anthropic API and converts to OpenAI format
func (s *Server) streamOpenAIFromAnthropic(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error {
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

	// Create converter
	converter := NewOpenAIStreamConverter(writer, ctx)

	// Parse Anthropic SSE stream
	scanner := NewSSEScanner(resp.Body)
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

			// Convert to OpenAI format
			if err := converter.HandleAnthropicEvent(currentEvent, json.RawMessage(data)); err != nil {
				logger.From(ctx).Errorf("Failed to handle Anthropic event: %v", err)
				return err
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return errors.WrapError("read Anthropic stream", err)
	}

	return nil
}

// streamOpenAIFromGoogle handles streaming from Google AI API and converts to OpenAI format
func (s *Server) streamOpenAIFromGoogle(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error {
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

	// Create converter
	converter := NewOpenAIStreamConverter(writer, ctx)

	// Send initial assistant delta
	if err := writer.WriteInitialAssistantDelta(); err != nil {
		return err
	}

	// Parse Google stream
	decoder := json.NewDecoder(resp.Body)
	for {
		var chunk map[string]interface{}
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Convert to OpenAI format
		if err := converter.HandleGoogleChunk(chunk); err != nil {
			logger.From(ctx).Errorf("Failed to handle Google chunk: %v", err)
			return err
		}
	}

	return nil
}

// streamOpenAIFromArgo handles streaming from Argo API and converts to OpenAI format
func (s *Server) streamOpenAIFromArgo(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error {
	log := logger.From(ctx)

	// Check if tools are present
	hasTools := len(anthReq.Tools) > 0

	if hasTools {
		// Argo doesn't support streaming with tools, simulate it
		log.Infof("Tools present - using simulated streaming for OpenAI format")
		return s.simulateOpenAIStreamFromArgo(ctx, anthReq, writer)
	}

	// No tools - use real streaming endpoint
	log.Infof("No tools - using real Argo streaming endpoint")

	// Get streaming response from Argo
	streamBody, err := s.forwardToArgoStream(ctx, anthReq)
	if err != nil {
		return err
	}
	defer streamBody.Close()

	// Create converter
	converter := NewOpenAIStreamConverter(writer, ctx)

	// Send initial assistant delta
	if err := writer.WriteInitialAssistantDelta(); err != nil {
		return err
	}

	// Read the entire stream into memory first
	// This is necessary because ContentSplitter works on complete strings
	data, err := io.ReadAll(streamBody)
	if err != nil {
		return err
	}

	// Use ContentSplitter for proper UTF-8 aware chunking
	splitter := NewContentSplitter(ctx, TextMode, 1024)
	chunks := splitter.Split(string(data))

	// Stream each chunk
	for _, chunk := range chunks {
		if err := converter.HandleArgoText(chunk); err != nil {
			logger.From(ctx).Errorf("Failed to handle Argo text chunk: %v", err)
			return err
		}
	}

	// Complete the stream
	return converter.Complete("stop")
}

// simulateOpenAIStreamFromArgo simulates OpenAI streaming when Argo has tools
func (s *Server) simulateOpenAIStreamFromArgo(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error {
	log := logger.From(ctx)

	// Get non-streaming response from Argo
	argoResp, err := s.forwardToArgo(ctx, anthReq)
	if err != nil {
		return err
	}

	// Convert to Anthropic format
	anthResp := s.converter.ConvertArgoToAnthropicWithRequest(argoResp, anthReq.Model, anthReq)

	// Log tool calls from response if present
	for _, block := range anthResp.Content {
		if block.Type == "tool_use" {
			if inputJSON, err := json.Marshal(block.Input); err == nil {
				log.Debugf("Tool call from response: %s: %s", block.Name, string(inputJSON))
			}
		}
	}

	// Send initial assistant delta
	if err := writer.WriteInitialAssistantDelta(); err != nil {
		return err
	}

	// Stream each content block
	for i, block := range anthResp.Content {
		switch block.Type {
		case "text":
			// Determine chunk size based on content length (same logic as streamTextBlock)
			chunkSize := constants.DefaultTextChunkSize
			if len(block.Text) > 1000 {
				chunkSize = constants.DefaultTextChunkSize * 2 // Larger chunks for longer content
			}

			// Use ContentSplitter for UTF-8 aware chunking to prevent character splitting
			splitter := NewContentSplitter(ctx, TextMode, chunkSize)
			chunks := splitter.Split(block.Text)
			for _, chunk := range chunks {
				if err := writer.WriteContent(chunk); err != nil {
					return err
				}
			}

		case "tool_use":
			// Send initial tool call with ID, name, and empty arguments
			if err := writer.WriteToolCallIntro(i, block.ID, block.Name); err != nil {
				return err
			}

			// Stream arguments in incremental deltas per OpenAI spec
			args, _ := json.Marshal(block.Input)
			argStr := string(args)
			splitter := NewContentSplitter(ctx, JSONMode, 64)
			for _, part := range splitter.Split(argStr) {
				if err := writer.WriteToolArguments(i, part); err != nil {
					return err
				}
			}
		}
	}

	// Usage Streaming Implementation:
	//
	// OpenAI format requires special handling for usage information:
	// 1. Check if constants.IncludeUsageKey is true (via Anthropic metadata)
	// 2. If false (default): No usage chunk is sent
	// 3. If true: Send usage as a separate chunk after finish_reason
	//
	// The metadata key constants.IncludeUsageKey is used to pass this
	// OpenAI-specific option through the Anthropic request format.
	//
	// Note: Intermediate chunks have explicit "usage: null" due to the
	// OpenAIStreamChunk.Usage field being a pointer without omitempty tag.
	// This matches OpenAI's behavior where usage is always present in the schema.
	// The include_usage flag is now set when creating the writer in handlers_openai.go

	// Map stop reason to finish reason
	finishReason := MapStopReasonToOpenAIFinishReason(anthResp.StopReason)

	// Prepare usage if available
	var usage *OpenAIUsage
	if anthResp.Usage != nil {
		usage = &OpenAIUsage{
			PromptTokens:     anthResp.Usage.InputTokens,
			CompletionTokens: anthResp.Usage.OutputTokens,
			TotalTokens:      anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens,
		}
	}

	// Write finish sequence: final chunk with finish_reason, optional usage, then [DONE]
	return writer.WriteFinish(finishReason, usage)
}
