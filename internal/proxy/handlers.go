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

// ServeHTTP implements http.Handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.From(ctx)

	switch r.URL.Path {
	case "/":
		log.Infof("%s %s | Root endpoint accessed", r.Method, r.URL.Path)
		s.handleRoot(w, r)
	case "/v1/messages":
		s.handleMessages(w, r)
	case "/v1/messages/count_tokens":
		s.handleCountTokens(w, r)
	default:
		log.Warnf("%s %s | Path not found", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// handleRoot serves a simple health check response
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"name":   "lmtools-proxy",
	}); err != nil {
		logger.From(r.Context()).Errorf("Failed to encode response: %v", err)
	}
}

// handleMessages processes the main messages endpoint
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	log := logger.From(ctx)

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Errorf("Failed to read request body: %v", err)
		http.Error(w, "Failed to read request", http.StatusBadRequest)
		return
	}

	// Parse Anthropic request
	var anthReq AnthropicRequest
	if err := json.Unmarshal(body, &anthReq); err != nil {
		log.Errorf("Failed to parse request: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate messages are not empty
	if len(anthReq.Messages) == 0 {
		log.Warnf("Request with empty messages")
		http.Error(w, "Messages array cannot be empty", http.StatusBadRequest)
		return
	}

	// Log request if debug enabled
	if log.IsDebugEnabled() {
		log.Debugf("Received request: model=%s, streaming=%v, messages=%d",
			anthReq.Model, anthReq.Stream, len(anthReq.Messages))
	}

	// Log incoming Anthropic request
	if anthReq.Stream {
		logger.DebugJSON(log, "Incoming Anthropic Streaming Request", anthReq)
	} else {
		logger.DebugJSON(log, "Incoming Anthropic Request", anthReq)
	}

	// Log tool information if present
	if len(anthReq.Tools) > 0 {
		logger.DebugJSON(log, "Tool information", anthReq.Tools)
	}

	// Map model to provider
	provider, mappedModel := s.mapper.MapModel(anthReq.Model)
	if provider == "" {
		log.Errorf("Failed to map model %s", anthReq.Model)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", anthReq.Model), http.StatusBadRequest)
		return
	}

	// Store original model for response
	originalModel := anthReq.Model
	anthReq.Model = mappedModel

	log.Infof("Routing request: model=%s -> provider=%s, mapped=%s",
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
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	log := logger.From(ctx)

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Errorf("Failed to read request body: %v", err)
		http.Error(w, "Failed to read request", http.StatusBadRequest)
		return
	}

	// Parse request - reuse AnthropicRequest structure
	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		log.Errorf("Failed to parse count tokens request: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate messages are not empty
	if len(req.Messages) == 0 {
		log.Warnf("Count tokens request with empty messages")
		http.Error(w, "Messages array cannot be empty", http.StatusBadRequest)
		return
	}

	// Log the incoming request
	log.Debugf("Count tokens request: model=%s, messages=%d", req.Model, len(req.Messages))
	logger.DebugJSON(log, "Incoming Token Count Request", req)

	// Map model to provider
	provider, mappedModel := s.mapper.MapModel(req.Model)
	if provider == "" {
		// For count_tokens, we can provide an estimate even for unknown models
		log.Warnf("Unknown model %s, using estimation", req.Model)
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
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
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
		}

	case "anthropic":
		anthResp, err = s.forwardToAnthropic(ctx, anthReq)

	default:
		err = errors.WrapError("route request", fmt.Errorf("unknown provider: %s", provider))
	}

	if err != nil {
		log.Errorf("Provider %s request failed: %v", provider, err)

		// Try to extract status code from error
		statusCode := http.StatusInternalServerError
		if respErr, ok := err.(*ResponseError); ok {
			statusCode = respErr.StatusCode
		}

		http.Error(w, err.Error(), statusCode)
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

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(anthResp); err != nil {
		log.Errorf("Failed to encode response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
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
		http.Error(w, "Failed to initialize streaming", http.StatusInternalServerError)
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
