package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"strings"
)

// parseOpenAIRequest reads and validates an OpenAI API request.
func (s *Server) parseOpenAIRequest(r *http.Request) (*OpenAIRequest, error) {
	body, err := s.readRequestBody(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON in request body")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages array cannot be empty")
	}
	return &req, nil
}

// handleOpenAIChatCompletions handles the OpenAI chat completions endpoint
func (s *Server) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.From(ctx)

	// Log endpoint access
	log.Infof("%s %s | OpenAI chat completions endpoint", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse and validate request
	openAIReq, err := s.parseOpenAIRequest(r)
	if err != nil {
		log.Errorf("Failed to parse request: %s", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}

	// Log request
	log.Debugf("Request received: model=%s, streaming=%v, messages=%d", openAIReq.Model, openAIReq.Stream, len(openAIReq.Messages))
	if openAIReq.Stream {
		logger.DebugJSON(log, "Streaming request details", openAIReq)
	} else {
		logger.DebugJSON(log, "Request details", openAIReq)
	}

	// Log tool information if present
	if len(openAIReq.Tools) > 0 {
		logger.DebugJSON(log, "Tool information", openAIReq.Tools)
	}

	// Map model to provider
	originalModel := openAIReq.Model
	mappedModel := s.mapper.MapModel(openAIReq.Model)
	provider := s.config.Provider // Provider always comes from config
	if provider == "" {
		log.Errorf("No provider configured")
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "No provider configured", "configuration_error", http.StatusInternalServerError)
		return
	}

	// Check if provider has required credentials
	if hasCredentials, diagnostic := s.hasCredentials(provider); !hasCredentials {
		log.Errorf("No credentials configured for provider %s: %s", provider, diagnostic)
		s.sendOpenAIError(w, ErrTypeAuthentication, diagnostic, "unauthorized", http.StatusUnauthorized)
		return
	}

	openAIReq.Model = mappedModel
	log.Infof("Model routing: %s -> provider=%s, mapped=%s", originalModel, provider, mappedModel)

	// If provider is OpenAI, do a direct pass-through
	if provider == constants.ProviderOpenAI {
		s.forwardOpenAIDirectly(w, r, openAIReq, originalModel)
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
	if openAIReq.Stream {
		// Handle streaming for OpenAI format
		s.handleOpenAIStreamingRequest(w, r, openAIReq, anthReq, provider, originalModel)
		return
	}

	// Process non-streaming request through existing pipeline
	var anthResp *AnthropicResponse

	switch provider {
	case constants.ProviderAnthropic:
		anthResp, err = s.forwardToAnthropic(ctx, anthReq)
	case constants.ProviderGoogle:
		googleResp, googleErr := s.forwardToGoogle(ctx, anthReq)
		if googleErr != nil {
			err = googleErr
		} else {
			anthResp = s.converter.ConvertGoogleToAnthropic(googleResp, originalModel)
		}
	case constants.ProviderArgo:
		argoResp, argoErr := s.forwardToArgo(ctx, anthReq)
		if argoErr != nil {
			err = argoErr
		} else {
			anthResp = s.converter.ConvertArgoToAnthropicWithRequest(argoResp, originalModel, anthReq)
			// Log tool calls from response if present
			for _, block := range anthResp.Content {
				if block.Type == "tool_use" {
					if inputJSON, err := json.Marshal(block.Input); err == nil {
						log.Debugf("Tool call from response: %s: %s", block.Name, string(inputJSON))
					}
				}
			}
		}
	default:
		err = fmt.Errorf("unsupported provider: %s", provider)
	}

	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, provider, err)
		return
	}

	// Convert Anthropic response back to OpenAI format
	openAIResp := s.converter.ConvertAnthropicResponseToOpenAI(anthResp, originalModel)

	// Log the complete OpenAI response before sending (only if debug enabled)
	logger.DebugJSON(log, "Sending OpenAI response", openAIResp)

	// Send response using centralized helper
	_ = s.sendJSONResponse(ctx, w, openAIResp)
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

	// Marshal request
	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		log.Errorf("Failed to marshal OpenAI request: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to prepare request", "marshal_error", http.StatusInternalServerError)
		return
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoints.OpenAI, bytes.NewReader(reqBody))
	if err != nil {
		log.Errorf("Failed to create OpenAI request: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to create request", "request_error", http.StatusInternalServerError)
		return
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.config.OpenAIAPIKey))

	// Make request
	resp, err := s.client.Do(ctx, req, constants.ProviderOpenAI)
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

	// Parse response to update model name
	var openAIResp OpenAIResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		log.Errorf("Failed to parse OpenAI response: %v", err)
		// Still send the response even if we can't parse it
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBody)
		return
	}

	// Update model name to original
	openAIResp.Model = originalModel

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

	// Route to appropriate streaming provider
	switch provider {
	case constants.ProviderAnthropic:
		err = s.streamOpenAIFromAnthropic(ctx, anthReq, writer)
	case constants.ProviderGoogle:
		err = s.streamOpenAIFromGoogle(ctx, anthReq, writer)
	case constants.ProviderArgo:
		err = s.streamOpenAIFromArgo(ctx, anthReq, writer)
	default:
		err = fmt.Errorf("unsupported provider for streaming: %s", provider)
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

	// Marshal request
	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		log.Errorf("Failed to marshal OpenAI request: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to prepare request", "marshal_error", http.StatusInternalServerError)
		return
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoints.OpenAI, bytes.NewReader(reqBody))
	if err != nil {
		log.Errorf("Failed to create OpenAI request: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to create request", "request_error", http.StatusInternalServerError)
		return
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.config.OpenAIAPIKey))
	req.Header.Set("Accept", "text/event-stream")

	// Make request
	resp, err := s.client.Do(ctx, req, constants.ProviderOpenAI)
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
			if strings.TrimSpace(line) == "data: [DONE]" {
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
