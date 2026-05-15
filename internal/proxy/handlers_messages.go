package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
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
	req.Betas = r.Header.Get("anthropic-beta")
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
	ctx := r.Context()
	log := logger.From(ctx)
	log.Infof("%s %s | Anthropic messages endpoint", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		s.sendAnthropicError(w, ErrTypeInvalidRequest, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	anthReq, err := s.parseAnthropicRequest(r)
	if err != nil {
		log.Errorf("Failed to parse request: %s", err)
		s.sendAnthropicError(w, ErrTypeInvalidRequest, err.Error(), http.StatusBadRequest)
		return
	}
	info := endpointRequestInfo{
		Model:        anthReq.Model,
		Stream:       anthReq.Stream,
		MessageCount: len(anthReq.Messages),
		ToolCount:    len(anthReq.Tools),
		Payload:      anthReq,
		Tools:        anthReq.Tools,
	}
	logEndpointRequest(ctx, info)

	route, routeErr := s.resolveEndpointRoute(ctx, info.Model)
	if routeErr != nil {
		if routeErr.Kind == endpointRouteAuthError {
			s.sendAnthropicError(w, ErrTypeAuthentication, routeErr.Message, http.StatusUnauthorized)
			return
		}
		s.sendAnthropicError(w, ErrTypeInvalidRequest, routeErr.Message, http.StatusInternalServerError)
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
	if mappedModel == "" {
		mappedModel = req.Model
	}
	provider := s.config.Provider // Provider always comes from config
	if provider == "" {
		// For count_tokens, we can provide an estimate even for unknown providers
		log.Warnf("No provider configured, using estimation")
		provider = "estimation"
	} else if req.Model != "" {
		req.Model = mappedModel
	}

	log.Infof("Token counting: model=%s, provider=%s", req.Model, provider)

	if resp, handled, err := s.countAnthropicTokensWithProvider(ctx, req, provider, mappedModel); handled {
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

func (s *Server) countAnthropicTokensWithProvider(ctx context.Context, req *AnthropicTokenCountRequest, provider, mappedModel string) (*AnthropicTokenCountResponse, bool, error) {
	if req.Model == "" || mappedModel == "" {
		return nil, false, nil
	}

	switch provider {
	case constants.ProviderAnthropic:
		resp, err := s.forwardAnthropicCountTokens(ctx, req)
		return resp, true, err
	case constants.ProviderArgo:
		if s.useLegacyArgo() || providers.DetermineArgoModelProvider(mappedModel) != constants.ProviderAnthropic {
			return nil, false, nil
		}
		resp, err := s.forwardArgoCountTokens(ctx, req)
		return resp, true, err
	case constants.ProviderGoogle:
		resp, err := s.forwardGoogleTokenCount(ctx, req, mappedModel)
		return resp, true, err
	default:
		return nil, false, nil
	}
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
	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI:
		return s.forwardAnthropicViaOpenAI(ctx, anthReq, originalModel)
	case constants.ProviderAnthropic:
		return s.forwardAnthropicViaAnthropic(ctx, anthReq, originalModel)
	case constants.ProviderGoogle:
		return s.forwardAnthropicViaGoogle(ctx, anthReq, originalModel)
	case constants.ProviderArgo:
		return s.forwardAnthropicViaArgo(ctx, anthReq, originalModel)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

func (s *Server) forwardArgoCountTokens(ctx context.Context, req *AnthropicTokenCountRequest) (*AnthropicTokenCountResponse, error) {
	var resp AnthropicTokenCountResponse
	err := s.doJSON(ctx, s.endpoints.ArgoAnthropicCountTokens, req, s.configureArgoAnthropicRequest, &resp, "Argo Anthropic count_tokens")
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *Server) forwardAnthropicCountTokens(ctx context.Context, req *AnthropicTokenCountRequest) (*AnthropicTokenCountResponse, error) {
	var resp AnthropicTokenCountResponse
	err := s.doJSON(ctx, s.endpoints.AnthropicCountTokens, req, func(httpReq *http.Request) {
		_ = auth.ApplyProviderCredentials(httpReq, constants.ProviderAnthropic, s.config.AnthropicAPIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}, &resp, "Anthropic count_tokens")
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *Server) forwardGoogleTokenCount(ctx context.Context, req *AnthropicTokenCountRequest, model string) (*AnthropicTokenCountResponse, error) {
	typed := AnthropicRequestToTyped(&AnthropicRequest{
		Model:    model,
		System:   req.System,
		Messages: req.Messages,
		Tools:    req.Tools,
	})
	typed.MaxTokens = nil

	googleReq, err := TypedToGoogleRequest(typed, model, nil)
	if err != nil {
		return nil, err
	}
	googleResp, err := s.forwardGoogleCountTokens(ctx, googleReq, model)
	if err != nil {
		return nil, err
	}
	return &AnthropicTokenCountResponse{InputTokens: googleResp.TotalTokens}, nil
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

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI:
		err = s.streamFromOpenAI(ctx, anthReq, handler)
	case constants.ProviderAnthropic:
		err = s.streamFromAnthropic(ctx, anthReq, handler)
	case constants.ProviderGoogle:
		err = s.streamFromGoogle(ctx, anthReq, handler)
	case constants.ProviderArgo:
		err = s.streamFromArgo(ctx, anthReq, handler)
	default:
		err = fmt.Errorf("unknown provider: %s", provider)
	}

	if err != nil {
		// handleStreamError classifies error, logs appropriately, and notifies client
		_ = handleStreamError(ctx, handler, fmt.Sprintf("Anthropic->%s", provider), err)
	}
}
