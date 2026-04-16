package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"strings"
)

// parseOpenAIRequest reads and validates an OpenAI API request.
func (s *Server) parseOpenAIRequest(r *http.Request) (*OpenAIRequest, error) {
	var req OpenAIRequest
	if err := s.decodeEndpointRequest(r, &req); err != nil {
		return nil, err
	}
	if err := validateParsedOpenAIRequest(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// handleOpenAIChatCompletions handles the OpenAI chat completions endpoint
func (s *Server) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	var openAIReq *OpenAIRequest
	info, route, ok := s.handlePOSTEndpoint(
		w,
		r,
		"OpenAI chat completions endpoint",
		func(r *http.Request) (*endpointRequestInfo, error) {
			req, err := s.parseOpenAIRequest(r)
			if err != nil {
				return nil, err
			}
			openAIReq = req
			return &endpointRequestInfo{
				Model:        req.Model,
				Stream:       req.Stream,
				MessageCount: len(req.Messages),
				ToolCount:    len(req.Tools),
				Payload:      req,
				Tools:        req.Tools,
			}, nil
		},
		endpointErrorHandlers{
			MethodNotAllowed: func() {
				s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
			},
			BadRequest: func(message string) {
				s.sendOpenAIError(w, ErrTypeInvalidRequest, message, "", http.StatusBadRequest)
			},
			ConfigError: func(message string) {
				s.sendOpenAIError(w, ErrTypeInvalidRequest, message, "configuration_error", http.StatusInternalServerError)
			},
			AuthError: func(message string) {
				s.sendOpenAIError(w, ErrTypeAuthentication, message, "unauthorized", http.StatusUnauthorized)
			},
		},
	)
	if !ok {
		return
	}

	ctx := r.Context()
	log := logger.From(ctx)

	if err := validateOpenAIRequestForProvider(openAIReq, route.Provider); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}

	openAIReq.Model = route.MappedModel

	// If provider is OpenAI, do a direct pass-through
	if route.Provider == constants.ProviderOpenAI {
		s.forwardOpenAIDirectly(w, r, openAIReq, route.OriginalModel)
		return
	}

	// For other providers, convert OpenAI request to Anthropic format through TypedRequest
	// ARCHITECTURAL NOTE: Always go through TypedRequest for conversions
	anthReq, err := s.converter.ConvertOpenAIRequestToAnthropic(ctx, openAIReq)
	if err != nil {
		log.Errorf("Failed to convert OpenAI to Anthropic format: %v", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Failed to process request", "conversion_error", http.StatusBadRequest)
		return
	}

	// Handle streaming vs non-streaming
	if info.Stream {
		// Handle streaming for OpenAI format
		s.handleOpenAIStreamingRequest(w, r, openAIReq, anthReq, route.Provider, route.OriginalModel)
		return
	}

	// Process non-streaming request through existing pipeline
	anthResp, err := s.forwardAnthropicRequest(ctx, anthReq, route.Provider, route.OriginalModel)
	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, route.Provider, err)
		return
	}

	// Convert Anthropic response back to OpenAI format
	openAIResp := s.converter.ConvertAnthropicResponseToOpenAI(anthResp, route.OriginalModel)

	// Log the complete OpenAI response before sending (only if debug enabled)
	logger.DebugJSON(log, "Sending OpenAI response", openAIResp)

	// Send response using centralized helper
	_ = s.sendJSONResponse(ctx, w, openAIResp)
}

func (s *Server) sendDirectOpenAIRequest(ctx context.Context, openAIReq *OpenAIRequest, stream bool) (*http.Response, error) {
	extraHeaders := map[string]string{}
	if stream {
		extraHeaders["Accept"] = "text/event-stream"
	}

	resp, _, err := s.sendProviderJSONRequest(ctx, providerJSONRequest{
		URL:          s.endpoints.OpenAI,
		Provider:     constants.ProviderOpenAI,
		RequestName:  "OpenAI",
		Payload:      openAIReq,
		ExtraHeaders: extraHeaders,
		Configure: func(req *http.Request) error {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.config.OpenAIAPIKey))
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func rewriteOpenAIResponseModel(body []byte, originalModel string) (*OpenAIResponse, error) {
	var openAIResp OpenAIResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return nil, err
	}
	openAIResp.Model = originalModel
	return &openAIResp, nil
}

// forwardOpenAIDirectly forwards an OpenAI request directly to OpenAI
func (s *Server) forwardOpenAIDirectly(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, originalModel string) {
	ctx := r.Context()
	log := logger.From(ctx)

	// Early validation: check if OpenAI URL is configured
	if s.endpoints.OpenAI == "" {
		log.Errorf("OpenAI URL not configured")
		s.sendOpenAIError(w, ErrTypeServer, "OpenAI URL not configured", "configuration_error", http.StatusInternalServerError)
		return
	}

	// For streaming requests, we need special handling
	if openAIReq.Stream {
		s.forwardOpenAIStreamDirectly(w, r, openAIReq, originalModel)
		return
	}

	resp, err := s.sendDirectOpenAIRequest(ctx, openAIReq, false)
	if err != nil {
		log.Errorf("OpenAI request failed: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := s.readResponseBody(resp)
	if err != nil {
		log.Errorf("Failed to read OpenAI response: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to read response", "read_error", http.StatusBadGateway)
		return
	}

	// If error response, pass it through with consistent logging
	if resp.StatusCode >= 400 {
		passthroughErrorResponse(ctx, w, constants.ProviderOpenAI, resp.StatusCode, respBody)
		return
	}

	openAIResp, err := rewriteOpenAIResponseModel(respBody, originalModel)
	if err != nil {
		log.Errorf("Failed to parse OpenAI response: %v", err)
		// Still send the response even if we can't parse it
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBody)
		return
	}

	// Send response using centralized helper
	_ = s.sendJSONResponse(ctx, w, openAIResp)
}

// handleOpenAIStreamingRequest handles streaming requests for the OpenAI chat completions endpoint
func (s *Server) handleOpenAIStreamingRequest(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, anthReq *AnthropicRequest, provider, originalModel string) {
	ctx := r.Context()
	log := logger.From(ctx)

	// Create OpenAI stream writer with include_usage option
	writer, err := NewOpenAIStreamWriter(w, originalModel, ctx, WithIncludeUsage(includeUsageFromMetadata(anthReq)))
	if err != nil {
		log.Errorf("Failed to create OpenAI stream writer: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to initialize streaming", "stream_init_error", http.StatusInternalServerError)
		return
	}

	capability, ok := proxyProviderCapabilityFor(provider)
	if !ok {
		err = fmt.Errorf("unknown provider: %s", provider)
	} else {
		forward, lookupErr := capability.requireOpenAIStreamForwarder(s)
		if lookupErr != nil {
			err = lookupErr
		} else {
			err = forward(ctx, anthReq, writer)
		}
	}

	if err != nil {
		// handleStreamError classifies error, logs appropriately, and notifies client
		_ = handleStreamError(ctx, writer, fmt.Sprintf("OpenAI->%s", provider), err)
	}
}

// forwardOpenAIStreamDirectly forwards an OpenAI streaming request directly to OpenAI
func (s *Server) forwardOpenAIStreamDirectly(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, originalModel string) {
	ctx := r.Context()
	log := logger.From(ctx)

	resp, err := s.sendDirectOpenAIRequest(ctx, openAIReq, true)
	if err != nil {
		log.Errorf("OpenAI streaming request failed: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode >= 400 {
		body, _ := s.readErrorBody(resp) // Use readErrorBody for error responses
		passthroughErrorResponse(ctx, w, constants.ProviderOpenAI, resp.StatusCode, body)
		return
	}

	// Set SSE headers
	setSSEHeaders(w)

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Errorf("ResponseWriter does not support flushing")
		return
	}

	// Stream the response directly with model name replacement
	scanner := NewSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// Check if this is a data line
		if strings.HasPrefix(line, "data: ") {
			// Check for the DONE sentinel explicitly
			if strings.TrimSpace(line) == "data: "+OpenAIDoneMarker {
				// Write as-is and continue
				fmt.Fprintf(w, "%s\n\n", line)
				flusher.Flush()
				continue
			}

			// Extract the data portion
			data := strings.TrimPrefix(line, "data: ")

			// Try to parse and update model name
			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err == nil {
				// Update model name
				chunk["model"] = originalModel

				// Re-marshal
				if updated, err := json.Marshal(chunk); err == nil {
					line = "data: " + string(updated)
				}
			}

			// Write the line with empty line after
			fmt.Fprintf(w, "%s\n\n", line)
		} else {
			// Non-data lines (like comments or events)
			fmt.Fprintf(w, "%s\n", line)
		}

		flusher.Flush()
	}

	if err := scanner.Err(); err != nil {
		// Note: we can't send error to client as stream is already in progress
		// handleStreamError handles all logging internally
		_ = handleStreamError(ctx, nil, "OpenAIDirectSSEScanner", err)
	}
}
