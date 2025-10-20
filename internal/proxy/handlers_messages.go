package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"net/http"
	"time"
)

// handleMessages processes the main messages endpoint
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.From(ctx)

	// Log endpoint access
	log.Infof("%s %s | Anthropic messages endpoint", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Errorf("Failed to read request body: %v", err)
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Failed to read request", http.StatusBadRequest)
		return
	}

	// Parse Anthropic request
	var anthReq AnthropicRequest
	if err := json.Unmarshal(body, &anthReq); err != nil {
		log.Errorf("Failed to parse request: %v", err)
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate messages are not empty
	if len(anthReq.Messages) == 0 {
		log.Warnf("Request with empty messages")
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Messages array cannot be empty", http.StatusBadRequest)
		return
	}

	// Log request if debug enabled
	if log.IsDebugEnabled() {
		log.Debugf("Request received: model=%s, streaming=%v, messages=%d",
			anthReq.Model, anthReq.Stream, len(anthReq.Messages))
	}

	// Log incoming Anthropic request
	if anthReq.Stream {
		logger.DebugJSON(log, "Streaming request details", anthReq)
	} else {
		logger.DebugJSON(log, "Request details", anthReq)
	}

	// Log tool information if present
	if len(anthReq.Tools) > 0 {
		logger.DebugJSON(log, "Tool information", anthReq.Tools)
	}

	// Map model to provider
	mappedModel := s.mapper.MapModel(anthReq.Model)
	provider := s.config.Provider // Provider always comes from config
	if provider == "" {
		log.Errorf("No provider configured")
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "No provider configured", http.StatusInternalServerError)
		return
	}

	// Check if provider has required credentials
	if hasCredentials, diagnostic := s.hasCredentials(provider); !hasCredentials {
		log.Errorf("No credentials configured for provider %s: %s", provider, diagnostic)
		s.sendAnthropicError(w, ErrTypeAuthentication, diagnostic, http.StatusUnauthorized)
		return
	}

	// Store original model for response
	originalModel := anthReq.Model
	anthReq.Model = mappedModel

	log.Infof("Model routing: %s -> provider=%s, mapped=%s",
		originalModel, provider, mappedModel)

	// Route based on streaming preference
	if anthReq.Stream {
		s.handleStreamingRequest(w, r, &anthReq, provider, originalModel, mappedModel)
	} else {
		s.handleNonStreamingRequest(w, r, &anthReq, provider, originalModel, mappedModel)
	}
}

// handleCountTokens handles the token counting endpoint
func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if r.Method != http.MethodPost {
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	log := logger.From(ctx)

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Errorf("Failed to read request body: %v", err)
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Failed to read request", http.StatusBadRequest)
		return
	}

	// Parse request - reuse AnthropicRequest structure
	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		log.Errorf("Failed to parse count tokens request: %v", err)
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate messages are not empty
	if len(req.Messages) == 0 {
		log.Warnf("Count tokens request with empty messages")
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Messages array cannot be empty", http.StatusBadRequest)
		return
	}

	// Log the incoming request
	log.Debugf("Count tokens request: model=%s, messages=%d", req.Model, len(req.Messages))
	logger.DebugJSON(log, "Incoming Token Count Request", req)

	// Map model to provider
	mappedModel := s.mapper.MapModel(req.Model)
	provider := s.config.Provider // Provider always comes from config
	if provider == "" {
		// For count_tokens, we can provide an estimate even for unknown providers
		log.Warnf("No provider configured, using estimation")
		provider = "estimation"
	} else {
		req.Model = mappedModel
	}

	log.Infof("Token counting: model=%s, provider=%s", req.Model, provider)

	// For now, provide a simple estimation
	// This could be enhanced with provider-specific token counting
	inputTokens := EstimateRequestTokens(&req)

	// Create response - use a simple map for now
	resp := map[string]interface{}{
		"input_tokens": inputTokens,
	}

	// Log the response
	log.Debugf("Count tokens response: input_tokens=%d", inputTokens)
	logger.DebugJSON(log, "Token Count Response", resp)

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Errorf("Failed to encode response: %v", err)
		s.sendAnthropicError(w, ErrTypeServer, "Failed to encode response", http.StatusInternalServerError)
		return
	}

	// Log input tokens info
	log.Infof("Input tokens: %d", inputTokens)

	// Log request summary
	RequestSummary(ctx, r.Method, r.URL.Path, req.Model, mappedModel, provider,
		len(req.Messages), len(req.Tools), http.StatusOK, false, time.Since(start))
}

// handleNonStreamingRequest processes non-streaming requests
func (s *Server) handleNonStreamingRequest(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, provider, originalModel, mappedModel string) {
	ctx := r.Context()
	log := logger.From(ctx)

	var anthResp *AnthropicResponse
	var err error

	// Route to appropriate provider
	switch provider {
	case "openai":
		openAIResp, openAIErr := s.forwardToOpenAI(ctx, anthReq)
		if openAIErr != nil {
			err = openAIErr
		} else {
			anthResp = s.converter.ConvertOpenAIToAnthropic(openAIResp, originalModel)
		}

	case "google":
		googleResp, googleErr := s.forwardToGoogle(ctx, anthReq)
		if googleErr != nil {
			err = googleErr
		} else {
			anthResp = s.converter.ConvertGoogleToAnthropic(googleResp, originalModel)
		}

	case "argo":
		// Check if tools are requested
		if len(anthReq.Tools) > 0 {
			log.Warnf("Tools requested but not supported by Argo provider, falling back to non-streaming")
			// Could return an error here if strict tool support is required
		}

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

	case "anthropic":
		anthResp, err = s.forwardToAnthropic(ctx, anthReq)

	default:
		err = errors.WrapError("route request", fmt.Errorf("unknown provider: %s", provider))
	}

	if err != nil {
		log.Errorf("Provider %s request failed: %v", provider, err)

		// Try to extract status code and body from error
		statusCode := http.StatusInternalServerError
		errorMsg := fmt.Sprintf("Upstream %s error (HTTP %d)", provider, statusCode)

		if respErr, ok := err.(*ResponseError); ok {
			statusCode = respErr.StatusCode
			// Include truncated error body for better debugging
			if respErr.Body != "" {
				truncated := truncateErrorBody(respErr.Body, 512)
				errorMsg = fmt.Sprintf("Upstream %s error (HTTP %d): %s", provider, statusCode, truncated)
			}
		}

		s.sendAnthropicError(w, ErrTypeServer, errorMsg, statusCode)
		return
	}

	// Restore original model in response
	anthResp.Model = originalModel

	// Log tool calls if present
	for _, block := range anthResp.Content {
		if block.Type == "tool_use" {
			if inputJSON, err := json.Marshal(block.Input); err == nil {
				log.Infof("Tool call: %s: %s", block.Name, string(inputJSON))
			}
		}
	}

	// Log the complete response before sending (only if debug enabled)
	logger.DebugJSON(log, "Sending Anthropic response", anthResp)

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(anthResp); err != nil {
		log.Errorf("Failed to encode response: %v", err)
		s.sendAnthropicError(w, ErrTypeServer, "Failed to encode response", http.StatusInternalServerError)
	}
}

// handleStreamingRequest processes streaming requests
func (s *Server) handleStreamingRequest(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, provider, originalModel, mappedModel string) {
	ctx := r.Context()
	log := logger.From(ctx)

	// Create stream handler (NewSSEWriter will set up SSE headers)
	handler, err := NewAnthropicStreamHandler(w, originalModel, ctx)
	if err != nil {
		log.Errorf("Failed to create stream handler: %v", err)
		s.sendAnthropicError(w, ErrTypeServer, "Failed to initialize streaming", http.StatusInternalServerError)
		return
	}

	// Route to appropriate streaming provider
	switch provider {
	case "openai":
		err = s.streamFromOpenAI(ctx, anthReq, handler)
	case "google":
		err = s.streamFromGoogle(ctx, anthReq, handler)
	case "argo":
		err = s.streamFromArgo(ctx, anthReq, handler)
	case "anthropic":
		err = s.streamFromAnthropic(ctx, anthReq, handler)
	default:
		err = errors.WrapError("stream request", fmt.Errorf("unknown provider: %s", provider))
	}

	if err != nil {
		log.Errorf("Streaming from %s failed: %v", provider, err)
		// Try to send error event if possible
		if sendErr := handler.SendError(err.Error()); sendErr != nil {
			log.Errorf("Failed to send error event: %v", sendErr)
		}
	}

	// Ensure stream is properly closed
	handler.Close()
}
