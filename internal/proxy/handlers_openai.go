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
	"lmtools/internal/providers"
	"net/http"
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
	ctx := r.Context()
	log := logger.From(ctx)
	log.Infof("%s %s | OpenAI chat completions endpoint", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}

	openAIReq, err := s.parseOpenAIRequest(r)
	if err != nil {
		log.Errorf("Failed to parse request: %s", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}
	info := endpointRequestInfo{
		Model:        openAIReq.Model,
		Stream:       openAIReq.Stream,
		MessageCount: len(openAIReq.Messages),
		ToolCount:    len(openAIReq.Tools),
		Payload:      openAIReq,
		Tools:        openAIReq.Tools,
	}
	logEndpointRequest(ctx, info)

	route, routeErr := s.resolveEndpointRoute(ctx, info.Model)
	if routeErr != nil {
		if routeErr.Kind == endpointRouteAuthError {
			s.sendOpenAIError(w, ErrTypeAuthentication, routeErr.Message, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.sendOpenAIError(w, ErrTypeInvalidRequest, routeErr.Message, "configuration_error", http.StatusInternalServerError)
		return
	}

	if err := validateOpenAIRequestForProvider(openAIReq, route.Provider, route.MappedModel); err != nil {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "", http.StatusBadRequest)
		return
	}

	openAIReq.Model = route.MappedModel
	clientStops := nonEmptyStopSequences([]string(openAIReq.Stop))

	// If provider is OpenAI, do a direct pass-through
	if route.Provider == constants.ProviderOpenAI {
		s.forwardOpenAIDirectly(w, r, openAIReq, route.OriginalModel, clientStops)
		return
	}
	if route.Provider == constants.ProviderArgo && !s.useLegacyArgo() && providers.DetermineArgoModelProvider(route.MappedModel) == constants.ProviderOpenAI {
		s.forwardArgoOpenAIDirectly(w, r, openAIReq, route.OriginalModel, clientStops)
		return
	}
	if route.Provider == constants.ProviderGoogle && !info.Stream {
		s.forwardOpenAIToGoogle(w, r, openAIReq, route.MappedModel, route.OriginalModel)
		return
	}

	// For other providers, convert OpenAI request to Anthropic format through TypedRequest
	// ARCHITECTURAL NOTE: Always go through TypedRequest for conversions
	anthReq, err := ConvertOpenAIRequestToAnthropic(ctx, openAIReq)
	if err != nil {
		log.Errorf("Failed to convert OpenAI to Anthropic format: %v", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Failed to process request", "conversion_error", http.StatusBadRequest)
		return
	}
	ensureAnthropicRequestWireMaxTokens(anthReq, route.Provider, route.MappedModel)

	// Handle streaming vs non-streaming
	if info.Stream {
		// Handle streaming for OpenAI format
		s.handleOpenAIStreamingRequest(w, r, openAIReq, anthReq, route.Provider, route.OriginalModel, clientStops)
		return
	}

	// Process non-streaming request through existing pipeline
	anthResp, err := s.forwardAnthropicRequest(ctx, anthReq, route.Provider, route.OriginalModel)
	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, route.Provider, err)
		return
	}

	// Convert Anthropic response back to OpenAI format
	registry := responseToolNameRegistryFromCoreTools(OpenAIRequestToTyped(openAIReq).Tools)
	openAIResp := ConvertAnthropicResponseToOpenAIWithToolNameRegistry(anthResp, route.OriginalModel, registry)
	enforceOpenAIResponseStops(openAIResp, clientStops)

	// Log the complete OpenAI response before sending (only if debug enabled)
	logger.DebugJSON(log, "Sending OpenAI response", openAIResp)

	// Send response using centralized helper
	_ = s.sendJSONResponse(ctx, w, openAIResp)
}

func (s *Server) sendOpenAICompatibleRequest(ctx context.Context, url, apiKey, requestName, provider string, openAIReq *OpenAIRequest, stream bool) (*http.Response, error) {
	omitNonPositiveOpenAITokenLimits(openAIReq)
	strippedStops := stripOpenAICompatibleStop(openAIReq)
	warnOpenAICompatibleStopSpecialProcessing(ctx, requestName, strippedStops)
	if constants.NormalizeProvider(provider) == constants.ProviderArgo {
		normalizeArgoOpenAIChatRequest(openAIReq)
	}

	extraHeaders := map[string]string{}
	if stream {
		extraHeaders["Accept"] = "text/event-stream"
	}
	if openAIReq.MaxCompletionTokens == nil {
		return s.sendOpenAICompatibleRequestOnce(ctx, url, apiKey, requestName, provider, openAIReq, extraHeaders)
	}
	return s.sendOpenAICompatibleRequestWithMaxCompletionTokenRetries(ctx, url, apiKey, requestName, provider, openAIReq, extraHeaders)
}

func (s *Server) sendOpenAICompatibleRequestOnce(ctx context.Context, url, apiKey, requestName, provider string, openAIReq *OpenAIRequest, extraHeaders map[string]string) (*http.Response, error) {
	resp, _, err := s.sendProviderJSONRequest(ctx, providerJSONRequest{
		URL:          url,
		Provider:     provider,
		RequestName:  requestName,
		Payload:      openAIReq,
		ExtraHeaders: extraHeaders,
		Configure: func(req *http.Request) {
			if apiKey != "" {
				req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Server) sendOpenAICompatibleRequestWithMaxCompletionTokenRetries(ctx context.Context, url, apiKey, requestName, provider string, openAIReq *OpenAIRequest, extraHeaders map[string]string) (*http.Response, error) {
	baseMaxCompletionTokens := *openAIReq.MaxCompletionTokens
	attemptValues := []int{baseMaxCompletionTokens, baseMaxCompletionTokens + 256, baseMaxCompletionTokens + 512, baseMaxCompletionTokens + 1024}
	var firstBadRequest *http.Response
	var firstBadRequestBody []byte

	for attempt, maxCompletionTokens := range attemptValues {
		attemptReq := *openAIReq
		attemptReq.MaxCompletionTokens = &maxCompletionTokens

		resp, err := s.sendOpenAICompatibleRequestOnce(ctx, url, apiKey, requestName, provider, &attemptReq, extraHeaders)
		if err != nil {
			if firstBadRequest != nil {
				logger.From(ctx).Warnf("%s chat/completions retry with max_completion_tokens=%d failed: %v; returning first 400 response", requestName, maxCompletionTokens, err)
				return restoreResponseBody(firstBadRequest, firstBadRequestBody), nil
			}
			return nil, err
		}
		if resp.StatusCode != http.StatusBadRequest {
			return resp, nil
		}

		body, err := s.readResponseBody(resp)
		if err != nil {
			resp.Body.Close()
			if firstBadRequest != nil {
				logger.From(ctx).Warnf("Failed to read %s chat/completions retry 400 response body: %v; returning first 400 response", requestName, err)
				return restoreResponseBody(firstBadRequest, firstBadRequestBody), nil
			}
			return nil, err
		}
		resp.Body.Close()
		if firstBadRequest == nil {
			firstBadRequest = resp
			firstBadRequestBody = append([]byte(nil), body...)
		}
		if attempt == len(attemptValues)-1 {
			return restoreResponseBody(firstBadRequest, firstBadRequestBody), nil
		}

		nextMaxCompletionTokens := attemptValues[attempt+1]
		logger.From(ctx).Warnf("%s chat/completions returned 400 with max_completion_tokens=%d; retrying with max_completion_tokens=%d (retry %d/3)", requestName, maxCompletionTokens, nextMaxCompletionTokens, attempt+1)
	}

	return restoreResponseBody(firstBadRequest, firstBadRequestBody), nil
}

func restoreResponseBody(resp *http.Response, body []byte) *http.Response {
	if resp == nil {
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp
}

func rewriteOpenAIResponseModel(ctx context.Context, body []byte, originalModel, source string) (*OpenAIResponse, error) {
	var openAIResp OpenAIResponse
	warnUnknownFields(ctx, body, OpenAIResponse{}, source)
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return nil, err
	}
	openAIResp.Model = originalModel
	return &openAIResp, nil
}

// forwardOpenAIDirectly forwards an OpenAI request directly to OpenAI
func (s *Server) forwardOpenAICompatibleDirectly(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, originalModel, url, apiKey, requestName, provider string, stops []string) {
	ctx := r.Context()
	log := logger.From(ctx)

	if url == "" {
		log.Errorf("%s URL not configured", requestName)
		s.sendOpenAIError(w, ErrTypeServer, fmt.Sprintf("%s URL not configured", requestName), "configuration_error", http.StatusInternalServerError)
		return
	}

	if openAIReq.Stream {
		s.forwardOpenAICompatibleStreamDirectly(w, r, openAIReq, originalModel, url, apiKey, requestName, provider, stops)
		return
	}

	resp, err := s.sendOpenAICompatibleRequest(ctx, url, apiKey, requestName, provider, openAIReq, false)
	if err != nil {
		log.Errorf("%s request failed: %v", requestName, err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, ok := s.readDirectProviderResponse(ctx, w, resp, provider, "OpenAI")
	if !ok {
		return
	}

	openAIResp, err := rewriteOpenAIResponseModel(ctx, respBody, originalModel, requestName+" response")
	if err != nil {
		log.Errorf("Failed to parse OpenAI response: %v", err)
		// Still send the response even if we can't parse it
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBody)
		return
	}
	enforceOpenAIResponseStops(openAIResp, stops)

	// Send response using centralized helper
	_ = s.sendJSONResponse(ctx, w, openAIResp)
}

func (s *Server) forwardOpenAIDirectly(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, originalModel string, stops []string) {
	s.forwardOpenAICompatibleDirectly(w, r, openAIReq, originalModel, s.endpoints.OpenAI, s.config.ProviderKeySet.OpenAIAPIKey, "OpenAI", constants.ProviderOpenAI, stops)
}

func (s *Server) forwardArgoOpenAIDirectly(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, originalModel string, stops []string) {
	s.forwardOpenAICompatibleDirectly(w, r, openAIReq, originalModel, s.endpoints.ArgoOpenAI, s.argoAPIKey(), "Argo OpenAI", constants.ProviderArgo, stops)
}

func (s *Server) forwardOpenAIToGoogle(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, mappedModel, originalModel string) {
	ctx := r.Context()
	log := logger.From(ctx)
	warnOpenAIRequestDropsForGoogle(ctx, openAIReq)

	typed, err := OpenAIRequestToTypedStrict(openAIReq)
	if err != nil {
		log.Errorf("Failed to convert OpenAI request: %v", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, err.Error(), "conversion_error", http.StatusBadRequest)
		return
	}
	googleReq, err := TypedToGoogleRequest(typed, mappedModel, nil)
	if err != nil {
		log.Errorf("Failed to convert OpenAI to Google format: %v", err)
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Failed to process request", "conversion_error", http.StatusBadRequest)
		return
	}

	url, err := buildGoogleModelURL(s.endpoints.Google, mappedModel, "generateContent")
	if err != nil {
		log.Errorf("Failed to build Google URL: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to build upstream request", "configuration_error", http.StatusInternalServerError)
		return
	}

	var googleResp GoogleResponse
	err = s.doJSON(ctx, url, googleReq, func(req *http.Request) {
		auth.SetProviderHeaders(req, constants.ProviderGoogle, s.config.ProviderKeySet.GoogleAPIKey)
	}, &googleResp, constants.ProviderGoogle, "Google")
	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, constants.ProviderGoogle, err)
		return
	}

	registry := responseToolNameRegistryFromCoreTools(typed.Tools)
	anthResp := ConvertGoogleToAnthropicWithToolNameRegistry(&googleResp, originalModel, registry)
	openAIResp := ConvertAnthropicResponseToOpenAIWithToolNameRegistry(anthResp, originalModel, registry)
	logger.DebugJSON(log, "Sending OpenAI response", openAIResp)
	_ = s.sendJSONResponse(ctx, w, openAIResp)
}

// handleOpenAIStreamingRequest handles streaming requests for the OpenAI chat completions endpoint
func (s *Server) handleOpenAIStreamingRequest(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, anthReq *AnthropicRequest, provider, originalModel string, stops []string) {
	ctx := r.Context()
	log := logger.From(ctx)

	// Create OpenAI stream writer with include_usage option
	writer, err := NewOpenAIStreamWriter(w, originalModel, ctx, WithIncludeUsage(includeUsageFromMetadata(anthReq)), WithStopSequences(stops))
	if err != nil {
		log.Errorf("Failed to create OpenAI stream writer: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to initialize streaming", "stream_init_error", http.StatusInternalServerError)
		return
	}

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderAnthropic:
		err = s.streamOpenAIFromAnthropic(ctx, anthReq, writer)
	case constants.ProviderGoogle:
		err = s.streamOpenAIFromGoogle(ctx, anthReq, writer)
	case constants.ProviderArgo:
		err = s.streamOpenAIFromArgo(ctx, anthReq, writer)
	default:
		err = fmt.Errorf("unknown provider: %s", provider)
	}

	if err != nil {
		// handleStreamError classifies error, logs appropriately, and notifies client
		_ = handleStreamError(ctx, writer, fmt.Sprintf("OpenAI->%s", provider), err)
	}
}

// forwardOpenAICompatibleStreamDirectly forwards an OpenAI-compatible streaming request directly upstream.
func (s *Server) forwardOpenAICompatibleStreamDirectly(w http.ResponseWriter, r *http.Request, openAIReq *OpenAIRequest, originalModel, url, apiKey, requestName, provider string, stops []string) {
	ctx := r.Context()
	log := logger.From(ctx)

	resp, err := s.sendOpenAICompatibleRequest(ctx, url, apiKey, requestName, provider, openAIReq, true)
	if err != nil {
		log.Errorf("%s streaming request failed: %v", requestName, err)
		s.sendOpenAIError(w, ErrTypeServer, "Upstream request failed", "upstream_error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := s.readErrorBody(resp) // Use readErrorBody for error responses
		passthroughErrorResponse(ctx, w, provider, resp.StatusCode, body)
		return
	}

	// Stream the response directly with model name replacement and local stop enforcement.
	setSSEHeaders(w)
	if err := forwardOpenAICompatibleSSEWithStops(ctx, w, resp.Body, originalModel, requestName, stops); err != nil {
		_ = handleStreamError(ctx, nil, "OpenAIDirectSSE", err)
	}
}
