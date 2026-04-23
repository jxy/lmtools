package proxy

import (
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

func (s *Server) sendStreamingJSONRequest(
	ctx context.Context,
	provider string,
	requestName string,
	url string,
	payload interface{},
	extraHeaders map[string]string,
	configure func(*http.Request) error,
) (*http.Response, error) {
	resp, _, err := s.sendProviderJSONRequest(ctx, providerJSONRequest{
		URL:          url,
		Provider:     provider,
		RequestName:  requestName,
		Payload:      payload,
		ExtraHeaders: extraHeaders,
		Configure:    configure,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Server) ensureStreamingResponseOK(ctx context.Context, provider string, resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return s.HandleStreamingError(ctx, provider, resp)
	}
	return nil
}

func ensureAnthropicTextPreamble(handler *AnthropicStreamHandler) error {
	if err := handler.SendMessageStart(); err != nil {
		return err
	}
	return handler.SendContentBlockStart(0, "text")
}

func consumeSSEStream(reader io.Reader, onData func(event string, data json.RawMessage) error) error {
	scanner := NewSSEScanner(reader)
	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if err := onData(currentEvent, json.RawMessage(data)); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}

// googleStreamingRequest builds and sends an HTTP request for Google streaming.
// Returns the response for streaming, caller is responsible for closing the body.
func (s *Server) googleStreamingRequest(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	// Convert to Google format
	googleReq, err := s.converter.ConvertAnthropicToGoogle(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Google format: %w", err)
	}

	// Construct streaming URL with model
	url, err := buildGoogleModelURL(s.endpoints.Google, anthReq.Model, "streamGenerateContent")
	if err != nil {
		return nil, fmt.Errorf("build Google streaming URL: %w", err)
	}

	return s.sendStreamingJSONRequest(ctx, constants.ProviderGoogle, "Google", url, googleReq, nil, func(req *http.Request) error {
		return auth.ApplyProviderCredentials(req, constants.ProviderGoogle, s.config.GoogleAPIKey)
	})
}

// anthropicStreamingRequest builds and sends an HTTP request for Anthropic streaming.
// Returns the response for streaming, caller is responsible for closing the body.
func (s *Server) anthropicStreamingRequest(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	// Enable streaming
	anthReq.Stream = true
	extraHeaders := map[string]string{
		"Accept":            "text/event-stream",
		"anthropic-version": "2023-06-01",
	}
	if anthReq.Betas != "" {
		extraHeaders["anthropic-beta"] = anthReq.Betas
	}

	return s.sendStreamingJSONRequest(
		ctx,
		constants.ProviderAnthropic,
		"Anthropic",
		s.endpoints.Anthropic,
		anthReq,
		extraHeaders,
		func(req *http.Request) error {
			return auth.ApplyProviderCredentials(req, constants.ProviderAnthropic, s.config.AnthropicAPIKey)
		},
	)
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

	return s.sendStreamingJSONRequest(ctx, constants.ProviderOpenAI, "OpenAI", s.endpoints.OpenAI, openAIReq, nil, func(req *http.Request) error {
		return auth.ApplyProviderCredentials(req, constants.ProviderOpenAI, s.config.OpenAIAPIKey)
	})
}

// streamFromOpenAI handles streaming from OpenAI API
func (s *Server) streamFromOpenAI(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	resp, err := s.openAIStreamingRequest(ctx, anthReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderOpenAI, resp); err != nil {
		return err
	}

	if err := ensureAnthropicTextPreamble(handler); err != nil {
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

	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderGoogle, resp); err != nil {
		return err
	}

	if err := ensureAnthropicTextPreamble(handler); err != nil {
		return err
	}

	// Parse Google stream
	parser := NewGoogleStreamParser(handler)
	return parser.Parse(resp.Body)
}

// streamFromArgo handles streaming from Argo API
func (s *Server) streamFromArgo(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	if s.useLegacyArgo() {
		if len(anthReq.Tools) > 0 {
			pingInterval := s.config.PingInterval
			if pingInterval <= 0 {
				pingInterval = constants.DefaultPingInterval * time.Second
			}
			return s.streamFromArgoWithPings(ctx, anthReq, handler, pingInterval)
		}

		body, err := s.forwardToArgoStream(ctx, anthReq)
		if err != nil {
			return err
		}
		defer body.Close()

		if err := ensureAnthropicTextPreamble(handler); err != nil {
			return err
		}

		parser := NewArgoStreamParser(handler)
		if s.config.PingInterval > 0 {
			return parser.ParseWithPingInterval(body, s.validatePingInterval(ctx, s.config.PingInterval))
		}
		return parser.Parse(body)
	}

	switch s.argoWireProvider(anthReq.Model) {
	case constants.ProviderAnthropic:
		resp, err := s.argoAnthropicStreamingRequest(ctx, anthReq)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := s.ensureStreamingResponseOK(ctx, constants.ProviderArgo, resp); err != nil {
			return err
		}

		return s.parseAnthropicStream(resp.Body, handler)
	default:
		resp, err := s.argoOpenAIStreamingRequestFromAnthropic(ctx, anthReq)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := s.ensureStreamingResponseOK(ctx, constants.ProviderArgo, resp); err != nil {
			return err
		}

		if err := ensureAnthropicTextPreamble(handler); err != nil {
			return err
		}

		parser := NewOpenAIStreamParser(handler)
		return parser.Parse(resp.Body)
	}
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

	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderAnthropic, resp); err != nil {
		return err
	}

	// Parse Anthropic SSE stream directly
	return s.parseAnthropicStream(resp.Body, handler)
}

// parseAnthropicStream parses Anthropic's SSE format
func (s *Server) parseAnthropicStream(body io.Reader, handler *AnthropicStreamHandler) error {
	log := logger.From(handler.ctx)
	if err := consumeSSEStream(body, func(currentEvent string, data json.RawMessage) error {
		recognized := warnAnthropicStreamEventFields(handler.ctx, currentEvent, data)
		switch currentEvent {
		case EventMessageStart:
			var evt MessageStartEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			handler.UpdateModel(evt.Message.Model)
			return handler.SendEvent(EventMessageStart, evt)

		case EventContentBlockStart:
			var evt ContentBlockStartEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			log.Debugf("Content block start: type=%s", evt.ContentBlock.Type)
			return handler.SendEvent(EventContentBlockStart, evt)

		case EventContentBlockDelta:
			var evt ContentBlockDeltaEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			if evt.Delta.Type == "text_delta" {
				handler.state.AccumulatedText += evt.Delta.Text
			}
			return handler.SendEvent(EventContentBlockDelta, evt)

		case EventContentBlockStop:
			var evt ContentBlockStopEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			return handler.SendEvent(EventContentBlockStop, evt)

		case EventMessageDelta:
			var evt MessageDeltaEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			if evt.Delta.StopReason != "" {
				handler.SetStopReason(evt.Delta.StopReason)
			}
			if evt.Usage != nil {
				handler.SetUsage(evt.Usage.InputTokens, evt.Usage.OutputTokens)
			}
			return handler.SendEvent(EventMessageDelta, evt)

		case EventMessageStop:
			var evt MessageStopEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			if err := handler.SendEvent(EventMessageStop, evt); err != nil {
				return err
			}
			return io.EOF

		case EventPing:
			var evt PingEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			return handler.SendEvent(EventPing, evt)

		case EventError:
			var evt ErrorEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				if handleErr := handleStreamError(handler.ctx, nil, "AnthropicStreamParser", err); handleErr != nil {
					return handleErr
				}
				return nil
			}
			if err := handler.SendEvent(EventError, evt); err != nil {
				return err
			}
			upstreamErr := fmt.Errorf("%s: %s", evt.Error.Type, evt.Error.Message)
			return handleStreamError(handler.ctx, nil, "AnthropicStreamParser:upstream", upstreamErr)

		default:
			if !recognized {
				log.Warnf("Unknown Anthropic SSE event type %q ignored: %s", currentEvent, string(data))
			}
			return nil
		}
	}); err != nil {
		if err == io.EOF {
			return nil
		}
		if handleErr := handleStreamError(handler.ctx, nil, "AnthropicSSEScanner", err); handleErr != nil {
			return handleErr
		}
	}

	return nil
}

func warnAnthropicStreamEventFields(ctx context.Context, currentEvent string, data json.RawMessage) bool {
	switch currentEvent {
	case EventMessageStart:
		warnUnknownFields(ctx, data, MessageStartEvent{}, "Anthropic stream message_start")
		return true
	case EventContentBlockStart:
		warnUnknownFields(ctx, data, ContentBlockStartEvent{}, "Anthropic stream content_block_start")
		return true
	case EventContentBlockDelta:
		warnUnknownFields(ctx, data, ContentBlockDeltaEvent{}, "Anthropic stream content_block_delta")
		return true
	case EventContentBlockStop:
		warnUnknownFields(ctx, data, ContentBlockStopEvent{}, "Anthropic stream content_block_stop")
		return true
	case EventMessageDelta:
		warnUnknownFields(ctx, data, MessageDeltaEvent{}, "Anthropic stream message_delta")
		return true
	case EventMessageStop:
		warnUnknownFields(ctx, data, MessageStopEvent{}, "Anthropic stream message_stop")
		return true
	case EventError:
		warnUnknownFields(ctx, data, ErrorEvent{}, "Anthropic stream error")
		return true
	case EventPing:
		warnUnknownFields(ctx, data, PingEvent{}, "Anthropic stream ping")
		return true
	default:
		return false
	}
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

	nextPingAt := time.Now().Add(pingInterval)
	timer := time.NewTimer(pingInterval)
	defer timer.Stop()

	// Send pings until response arrives
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case res := <-respChan:
			now := time.Now()
			for !nextPingAt.After(now) {
				if err := handler.SendPing(); err != nil {
					log.Warnf("Failed to send ping: %v", err)
					break
				}
				nextPingAt = nextPingAt.Add(pingInterval)
			}
			return res.resp, res.err

		case <-timer.C:
			if err := handler.SendPing(); err != nil {
				log.Warnf("Failed to send ping: %v", err)
			}
			nextPingAt = nextPingAt.Add(pingInterval)
			wait := time.Until(nextPingAt)
			if wait < 0 {
				wait = 0
			}
			timer.Reset(wait)
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

	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderAnthropic, resp); err != nil {
		return err
	}

	return s.convertAnthropicStreamToOpenAI(ctx, resp.Body, writer)
}

// convertAnthropicStreamToOpenAI converts an Anthropic SSE stream to OpenAI format.
// This function handles the SSE parsing and format conversion, keeping HTTP concerns
// in the caller (streamOpenAIFromAnthropic).
func (s *Server) convertAnthropicStreamToOpenAI(ctx context.Context, body io.Reader, writer *OpenAIStreamWriter) error {
	// Create converter
	converter := NewOpenAIStreamConverter(writer, ctx)

	if err := consumeSSEStream(body, func(currentEvent string, data json.RawMessage) error {
		if !warnAnthropicStreamEventFields(ctx, currentEvent, data) {
			logger.From(ctx).Warnf("Unknown Anthropic SSE event type %q ignored during OpenAI conversion: %s", currentEvent, string(data))
		}
		if err := converter.HandleAnthropicEvent(currentEvent, data); err != nil {
			return handleStreamError(ctx, writer, "AnthropicToOpenAIConverter", err)
		}
		return nil
	}); err != nil {
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

	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderGoogle, resp); err != nil {
		return err
	}

	return s.convertGoogleStreamToOpenAI(ctx, resp.Body, writer)
}

// convertGoogleStreamToOpenAI converts a Google SSE stream to OpenAI format.
// This function handles the SSE parsing and format conversion, keeping HTTP concerns
// in the caller (streamOpenAIFromGoogle).
func (s *Server) convertGoogleStreamToOpenAI(ctx context.Context, body io.Reader, writer *OpenAIStreamWriter) error {
	// Create converter
	converter := NewOpenAIStreamConverter(writer, ctx)

	if err := consumeSSEStream(body, func(_ string, data json.RawMessage) error {
		warnUnknownFields(ctx, data, GoogleResponse{}, "Google stream chunk")
		var chunk map[string]interface{}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return handleStreamError(ctx, nil, "GoogleToOpenAIChunk", err)
		}
		if err := converter.HandleGoogleChunk(chunk); err != nil {
			return handleStreamError(ctx, writer, "GoogleToOpenAIConverter", err)
		}
		return nil
	}); err != nil {
		if handleErr := handleStreamError(ctx, nil, "GoogleToOpenAISSEScanner", err); handleErr != nil {
			return handleErr
		}
	}

	return nil
}

// streamOpenAIFromArgo handles streaming from Argo API and converts to OpenAI format
func (s *Server) streamOpenAIFromArgo(ctx context.Context, anthReq *AnthropicRequest, writer *OpenAIStreamWriter) error {
	if s.useLegacyArgo() {
		if len(anthReq.Tools) > 0 {
			return s.simulateOpenAIStreamFromArgo(ctx, anthReq, writer)
		}

		body, err := s.forwardToArgoStream(ctx, anthReq)
		if err != nil {
			return err
		}
		defer body.Close()

		converter := NewOpenAIStreamConverter(writer, ctx)
		buffer := make([]byte, 1024)
		for {
			n, readErr := body.Read(buffer)
			if n > 0 {
				if err := converter.HandleArgoText(string(buffer[:n])); err != nil {
					return err
				}
			}
			if readErr == io.EOF {
				return converter.FinishStream("stop", nil)
			}
			if readErr != nil {
				return readErr
			}
		}
	}

	switch s.argoWireProvider(anthReq.Model) {
	case constants.ProviderAnthropic:
		resp, err := s.argoAnthropicStreamingRequest(ctx, anthReq)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if err := s.ensureStreamingResponseOK(ctx, constants.ProviderArgo, resp); err != nil {
			return err
		}

		return s.convertAnthropicStreamToOpenAI(ctx, resp.Body, writer)
	default:
		return fmt.Errorf("direct Argo OpenAI streaming should bypass conversion path")
	}
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

	if err := streamSimulatedContentBlocks(ctx, anthResp.Content, &openAISimulatedContentEmitter{
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
