package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"time"
)

// parseAnthropicRequest reads and validates an Anthropic API request.
func (s *Server) parseAnthropicRequest(r *http.Request) (*AnthropicRequest, error) {
	var req AnthropicRequest
	if err := s.decodeEndpointRequest(r, &req); err != nil {
		return nil, err
	}
	if err := validateParsedAnthropicRequest(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// parseAnthropicTokenCountRequest reads and validates an Anthropic token-count request.
// Unlike /v1/messages, this endpoint allows model-less requests so we can still provide
// an estimated count from the request structure alone.
func (s *Server) parseAnthropicTokenCountRequest(r *http.Request) (*AnthropicTokenCountRequest, error) {
	var req AnthropicTokenCountRequest
	if err := s.decodeEndpointRequest(r, &req); err != nil {
		return nil, err
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages array cannot be empty")
	}
	return &req, nil
}

// handleMessages processes the main messages endpoint
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	var anthReq *AnthropicRequest
	info, route, ok := s.handlePOSTEndpoint(
		w,
		r,
		"Anthropic messages endpoint",
		func(r *http.Request) (*endpointRequestInfo, error) {
			req, err := s.parseAnthropicRequest(r)
			if err != nil {
				return nil, err
			}
			anthReq = req
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
				s.sendAnthropicError(w, ErrTypeInvalidRequest, "Method not allowed", http.StatusMethodNotAllowed)
			},
			BadRequest: func(message string) {
				s.sendAnthropicError(w, ErrTypeInvalidRequest, message, http.StatusBadRequest)
			},
			ConfigError: func(message string) {
				s.sendAnthropicError(w, ErrTypeInvalidRequest, message, http.StatusInternalServerError)
			},
			AuthError: func(message string) {
				s.sendAnthropicError(w, ErrTypeAuthentication, message, http.StatusUnauthorized)
			},
		},
	)
	if !ok {
		return
	}

	if err := validateAnthropicRequestForProvider(anthReq, route.Provider); err != nil {
		s.sendAnthropicError(w, ErrTypeInvalidRequest, err.Error(), http.StatusBadRequest)
		return
	}

	anthReq.Model = route.MappedModel

	// Route based on streaming preference
	if info.Stream {
		s.handleStreamingRequest(w, r, anthReq, route.Provider, route.OriginalModel, route.MappedModel)
	} else {
		s.handleNonStreamingRequest(w, r, anthReq, route.Provider, route.OriginalModel, route.MappedModel)
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

	// Parse and validate request
	req, err := s.parseAnthropicTokenCountRequest(r)
	if err != nil {
		log.Errorf("Failed to parse count tokens request: %s", err)
		s.sendAnthropicError(w, ErrTypeInvalidRequest, err.Error(), http.StatusBadRequest)
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
	} else if req.Model != "" {
		req.Model = mappedModel
	}

	log.Infof("Token counting: model=%s, provider=%s", req.Model, provider)

	if provider == constants.ProviderArgo && !s.useLegacyArgo() {
		resp, err := s.forwardArgoCountTokens(ctx, req)
		if err != nil {
			s.sendProviderErrorAsAnthropic(ctx, w, provider, err)
			return
		}
		logger.DebugJSON(log, "Token Count Response", resp)
		if err := s.sendJSONResponse(ctx, w, resp); err != nil {
			return
		}
		log.Infof("Input tokens: %d", resp.InputTokens)
		RequestSummary(ctx, r.Method, r.URL.Path, req.Model, mappedModel, provider,
			len(req.Messages), len(req.Tools), http.StatusOK, false, time.Since(start))
		return
	}

	// For now, provide a simple estimation.
	// This could be enhanced with provider-specific token counting.
	inputTokens := EstimateRequestTokens(&AnthropicRequest{
		Model:    req.Model,
		System:   req.System,
		Messages: req.Messages,
		Tools:    req.Tools,
	})

	// Create response - use a simple map for now
	resp := map[string]interface{}{
		"input_tokens": inputTokens,
	}

	// Log the response
	log.Debugf("Count tokens response: input_tokens=%d", inputTokens)
	logger.DebugJSON(log, "Token Count Response", resp)

	// Send response using centralized helper (logs errors internally)
	if err := s.sendJSONResponse(ctx, w, resp); err != nil {
		return // Error already logged, can't send another response
	}

	// Log input tokens info
	log.Infof("Input tokens: %d", inputTokens)

	// Log request summary
	RequestSummary(ctx, r.Method, r.URL.Path, req.Model, mappedModel, provider,
		len(req.Messages), len(req.Tools), http.StatusOK, false, time.Since(start))
}

func logToolUseBlocks(ctx context.Context, content []AnthropicContentBlock, info bool) {
	log := logger.From(ctx)
	for _, block := range content {
		if block.Type != "tool_use" {
			continue
		}
		if inputJSON, err := json.Marshal(block.Input); err == nil {
			if info {
				log.Infof("Tool call: %s: %s", block.Name, string(inputJSON))
			} else {
				log.Debugf("Tool call from response: %s: %s", block.Name, string(inputJSON))
			}
		}
	}
}

func (s *Server) forwardAnthropicRequest(ctx context.Context, anthReq *AnthropicRequest, provider, originalModel string) (*AnthropicResponse, error) {
	capability, ok := proxyProviderCapabilityFor(provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	forward, err := capability.requireAnthropicResponseForwarder(s)
	if err != nil {
		return nil, err
	}

	return forward(ctx, anthReq, originalModel)
}

func (s *Server) forwardArgoCountTokens(ctx context.Context, req *AnthropicTokenCountRequest) (*AnthropicTokenCountResponse, error) {
	var resp AnthropicTokenCountResponse
	err := s.doJSON(ctx, s.endpoints.ArgoAnthropicCountTokens, req, s.configureArgoAnthropicRequest, &resp, "Argo Anthropic count_tokens")
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// handleNonStreamingRequest processes non-streaming requests
func (s *Server) handleNonStreamingRequest(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, provider, originalModel, mappedModel string) {
	ctx := r.Context()
	log := logger.From(ctx)

	anthResp, err := s.forwardAnthropicRequest(ctx, anthReq, provider, originalModel)
	if err != nil {
		s.sendProviderErrorAsAnthropic(ctx, w, provider, err)
		return
	}

	// Restore original model in response
	anthResp.Model = originalModel

	// Log tool calls if present
	logToolUseBlocks(ctx, anthResp.Content, true)

	// Log the complete response before sending (only if debug enabled)
	logger.DebugJSON(log, "Sending Anthropic response", anthResp)

	// Send response using centralized helper (logs errors internally)
	_ = s.sendJSONResponse(ctx, w, anthResp)
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

	capability, ok := proxyProviderCapabilityFor(provider)
	if !ok {
		err = fmt.Errorf("unknown provider: %s", provider)
	} else {
		forward, lookupErr := capability.requireAnthropicStreamForwarder(s)
		if lookupErr != nil {
			err = lookupErr
		} else {
			err = forward(ctx, anthReq, handler)
		}
	}

	if err != nil {
		// handleStreamError classifies error, logs appropriately, and notifies client
		_ = handleStreamError(ctx, handler, fmt.Sprintf("Anthropic->%s", provider), err)
	}

	// Ensure stream is properly closed
	handler.Close()
}
