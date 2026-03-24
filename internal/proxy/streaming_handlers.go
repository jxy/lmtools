package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"strings"
	"time"
)

// Helper functions to reduce duplication in stream handlers

// googleStreamingRequest builds and sends an HTTP request for Google streaming.
// Returns the response for streaming, caller is responsible for closing the body.
func (s *Server) googleStreamingRequest(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	// Convert to Google format
	googleReq, err := s.converter.ConvertAnthropicToGoogle(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Google format: %w", err)
	}

	// Marshal request
	reqBody, err := json.Marshal(googleReq)
	if err != nil {
		return nil, fmt.Errorf("marshal Google request: %w", err)
	}

	// Construct streaming URL with model
	url, err := buildGoogleModelURL(s.endpoints.Google, anthReq.Model, "streamGenerateContent")
	if err != nil {
		return nil, fmt.Errorf("build Google streaming URL: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create Google request: %w", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")

	// Apply API key
	if key := s.config.GoogleAPIKey; key != "" {
		if err := auth.ApplyGoogleAPIKey(req, key); err != nil {
			return nil, fmt.Errorf("apply Google API key: %w", err)
		}
	}

	// Send request
	resp, err := s.client.Do(ctx, req, constants.ProviderGoogle)
	if err != nil {
		return nil, fmt.Errorf("send Google request: %w", err)
	}

	return resp, nil
}

// anthropicStreamingRequest builds and sends an HTTP request for Anthropic streaming.
// Returns the response for streaming, caller is responsible for closing the body.
func (s *Server) anthropicStreamingRequest(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	// Enable streaming
	anthReq.Stream = true

	// Marshal request
	reqBody, err := json.Marshal(anthReq)
	if err != nil {
		return nil, fmt.Errorf("marshal Anthropic request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoints.Anthropic, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create Anthropic request: %w", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	if key := s.config.AnthropicAPIKey; key != "" {
		req.Header.Set("x-api-key", key)
	}

	// Send request
	resp, err := s.client.Do(ctx, req, constants.ProviderAnthropic)
	if err != nil {
		return nil, fmt.Errorf("send Anthropic request: %w", err)
	}

	return resp, nil
}

// openAIStreamingRequest builds and sends an HTTP request for OpenAI streaming.
// Returns the response for streaming, caller is responsible for closing the body.
func (s *Server) openAIStreamingRequest(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	// Convert to OpenAI format
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to OpenAI format: %w", err)
	}
	openAIReq.Stream = true

	// Marshal request
	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoints.OpenAI, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI request: %w", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")
	if key := s.config.OpenAIAPIKey; key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	// Send request
	resp, err := s.client.Do(ctx, req, constants.ProviderOpenAI)
	if err != nil {
		return nil, fmt.Errorf("send OpenAI request: %w", err)
	}

	return resp, nil
}

// streamFromOpenAI handles streaming from OpenAI API
func (s *Server) streamFromOpenAI(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	resp, err := s.openAIStreamingRequest(ctx, anthReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		return s.HandleStreamingError(ctx, constants.ProviderOpenAI, resp)
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
	resp, err := s.googleStreamingRequest(ctx, anthReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		return s.HandleStreamingError(ctx, constants.ProviderGoogle, resp)
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
	return s.simulateStreamingFromArgoWithInterval(ctx, anthReq, handler, pingInterval)
}

// streamFromAnthropic handles streaming from Anthropic API
func (s *Server) streamFromAnthropic(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	resp, err := s.anthropicStreamingRequest(ctx, anthReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		return s.HandleStreamingError(ctx, constants.ProviderAnthropic, resp)
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
				if err := json.Unmarshal([]byte(data), &evt); err != nil {
					if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
						return handleErr
					}
					continue
				}
				handler.UpdateModel(evt.Message.Model)
				// Forward the event to the client
				if err := handler.SendEvent(EventMessageStart, evt); err != nil {
					return err
				}

			case EventContentBlockStart:
				var evt ContentBlockStartEvent
				if err := json.Unmarshal([]byte(data), &evt); err != nil {
					if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
						return handleErr
					}
					continue
				}
				// Log the content block start event
				log.Debugf("Content block start: type=%s", evt.ContentBlock.Type)
				// Forward the event to the client
				if err := handler.SendEvent(EventContentBlockStart, evt); err != nil {
					return err
				}

			case EventContentBlockDelta:
				var evt ContentBlockDeltaEvent
				if err := json.Unmarshal([]byte(data), &evt); err != nil {
					if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
						return handleErr
					}
					continue
				}
				// If it's a text_delta, accumulate the text
				if evt.Delta.Type == "text_delta" {
					handler.state.AccumulatedText += evt.Delta.Text
				}
				// Forward the event to the client
				if err := handler.SendEvent(EventContentBlockDelta, evt); err != nil {
					return err
				}

			case EventContentBlockStop:
				// Block completed
				var evt ContentBlockStopEvent
				if err := json.Unmarshal([]byte(data), &evt); err != nil {
					if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
						return handleErr
					}
					continue
				}
				// Forward the event to the client
				if err := handler.SendEvent(EventContentBlockStop, evt); err != nil {
					return err
				}

			case EventMessageDelta:
				var evt MessageDeltaEvent
				if err := json.Unmarshal([]byte(data), &evt); err != nil {
					if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
						return handleErr
					}
					continue
				}
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

			case EventMessageStop:
				// Stream completed; ensure we sent a message_delta earlier
				var evt MessageStopEvent
				if err := json.Unmarshal([]byte(data), &evt); err != nil {
					if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
						return handleErr
					}
					// For message_stop, we still return nil to end the stream gracefully
					return nil
				}
				// Forward the event to the client
				if err := handler.SendEvent(EventMessageStop, evt); err != nil {
					return err
				}
				return nil

			case EventError:
				var evt ErrorEvent
				if err := json.Unmarshal([]byte(data), &evt); err != nil {
					if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
						return handleErr
					}
					continue
				}
				// Forward the error event to the client
				if err := handler.SendEvent(EventError, evt); err != nil {
					return err
				}
				// Upstream error events are fatal - use handleStreamError for consistent logging
				// Pass nil emitter since we already forwarded the error to the client above
				upstreamErr := fmt.Errorf("%s: %s", evt.Error.Type, evt.Error.Message)
				return handleStreamError(handler.ctx, nil, "AnthropicStreamParser:upstream", upstreamErr)

			default:
				// Log unknown event types for debugging
				log.Debugf("Unknown SSE event type: %s, data: %s", currentEvent, data)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if handleErr := handleStreamError(handler.ctx, nil, "AnthropicSSEScanner", err); handleErr != nil {
			return handleErr
		}
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
	return streamSimulatedContentBlocks(ctx, anthResp.Content, anthropicSimulatedContentEmitter{
		ctx:     ctx,
		handler: handler,
	})
}

// streamTextBlock streams a text content block
func (s *Server) streamTextBlock(content string, blockIndex int, handler *AnthropicStreamHandler) error {
	emitter := anthropicSimulatedContentEmitter{
		ctx:     handler.ctx,
		handler: handler,
	}
	if err := emitter.StartTextBlock(blockIndex, content); err != nil {
		return err
	}
	if err := emitSimulatedTextChunks(handler.ctx, content, func(chunk string) error {
		return emitter.WriteTextChunk(blockIndex, chunk)
	}); err != nil {
		return err
	}
	return emitter.EndTextBlock(blockIndex, content)
}

// streamToolBlock streams a tool use content block
func (s *Server) streamToolBlock(ctx context.Context, block AnthropicContentBlock, blockIndex int, handler *AnthropicStreamHandler) error {
	emitter := anthropicSimulatedContentEmitter{
		ctx:     ctx,
		handler: handler,
	}
	if err := emitter.StartToolBlock(blockIndex, block); err != nil {
		return err
	}
	if err := emitSimulatedToolInputChunks(ctx, block.Input, func(chunk string) error {
		return emitter.WriteToolInputChunk(blockIndex, chunk)
	}); err != nil {
		return err
	}
	return emitter.EndToolBlock(blockIndex, block)
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
	resp, err := s.anthropicStreamingRequest(ctx, anthReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		return s.HandleStreamingError(ctx, constants.ProviderAnthropic, resp)
	}

	return s.convertAnthropicStreamToOpenAI(ctx, resp.Body, writer)
}

// convertAnthropicStreamToOpenAI converts an Anthropic SSE stream to OpenAI format.
// This function handles the SSE parsing and format conversion, keeping HTTP concerns
// in the caller (streamOpenAIFromAnthropic).
func (s *Server) convertAnthropicStreamToOpenAI(ctx context.Context, body io.Reader, writer *OpenAIStreamWriter) error {
	// Create converter
	converter := NewOpenAIStreamConverter(writer, ctx)

	// Parse Anthropic SSE stream
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

			// Convert to OpenAI format
			if err := converter.HandleAnthropicEvent(currentEvent, json.RawMessage(data)); err != nil {
				return handleStreamError(ctx, writer, "AnthropicToOpenAIConverter", err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if handleErr := handleStreamError(ctx, nil, "AnthropicToOpenAISSEScanner", err); handleErr != nil {
			return handleErr
		}
	}

	return nil
}

// streamOpenAIFromGoogle handles streaming from Google AI API and converts to OpenAI format
func (s *Server) streamOpenAIFromGoogle(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error {
	resp, err := s.googleStreamingRequest(ctx, anthReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		return s.HandleStreamingError(ctx, constants.ProviderGoogle, resp)
	}

	// Send initial assistant delta
	if err := writer.WriteInitialAssistantDelta(); err != nil {
		return err
	}

	return s.convertGoogleStreamToOpenAI(ctx, resp.Body, writer)
}

// convertGoogleStreamToOpenAI converts a Google JSON stream to OpenAI format.
// This function handles the JSON decoding and format conversion, keeping HTTP concerns
// in the caller (streamOpenAIFromGoogle).
func (s *Server) convertGoogleStreamToOpenAI(ctx context.Context, body io.Reader, writer *OpenAIStreamWriter) error {
	// Create converter
	converter := NewOpenAIStreamConverter(writer, ctx)

	// Parse Google stream
	decoder := json.NewDecoder(body)
	for {
		var chunk map[string]interface{}
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			if handleErr := handleStreamError(ctx, nil, "GoogleToOpenAIDecoder", err); handleErr != nil {
				return handleErr
			}
			continue // Recoverable error - skip bad chunk
		}

		// Convert to OpenAI format
		if err := converter.HandleGoogleChunk(chunk); err != nil {
			return handleStreamError(ctx, writer, "GoogleToOpenAIConverter", err)
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

	// Read the entire stream into memory first with size limit
	// This is necessary because ContentSplitter works on complete strings
	data, err := s.readStreamingBody(streamBody)
	if err != nil {
		return handleStreamError(ctx, writer, "ArgoToOpenAIReader", err)
	}

	// Use ContentSplitter for proper UTF-8 aware chunking
	splitter := NewContentSplitter(ctx, TextMode, 1024)
	chunks := splitter.Split(string(data))

	// Stream each chunk
	for _, chunk := range chunks {
		if err := converter.HandleArgoText(chunk); err != nil {
			return handleStreamError(ctx, writer, "ArgoToOpenAIConverter", err)
		}
	}

	// Complete the stream
	return converter.Complete("stop")
}

// simulateOpenAIStreamFromArgo simulates OpenAI streaming when Argo has tools
func (s *Server) simulateOpenAIStreamFromArgo(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error {
	// Get non-streaming response from Argo
	argoResp, err := s.forwardToArgo(ctx, anthReq)
	if err != nil {
		return err
	}

	// Convert to Anthropic format
	anthResp := s.converter.ConvertArgoToAnthropicWithRequest(argoResp, anthReq.Model, anthReq)

	// Log tool calls from response if present
	logToolUseBlocks(ctx, anthResp.Content, false)

	// Send initial assistant delta
	if err := writer.WriteInitialAssistantDelta(); err != nil {
		return err
	}

	if err := streamSimulatedContentBlocks(ctx, anthResp.Content, openAISimulatedContentEmitter{
		writer: writer,
	}); err != nil {
		return err
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

	// Write finish sequence: final chunk with finish_reason, optional usage, then [DONE]
	return writer.WriteFinish(finishReason, AnthropicUsageToOpenAI(anthResp.Usage))
}
