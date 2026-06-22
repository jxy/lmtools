package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
	"net/http"
	"strings"
)

// forwardToOpenAI forwards a request to the OpenAI API
func (s *Server) forwardToOpenAI(ctx context.Context, anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	stops := nonEmptyStopSequences(anthReq.StopSequences)

	// Convert to OpenAI format
	openAIReq, err := ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to OpenAI format: %w", err)
	}
	strippedStops := stripOpenAICompatibleStop(openAIReq)
	warnOpenAICompatibleStopSpecialProcessing(ctx, "OpenAI", strippedStops)

	var openAIResp OpenAIResponse
	err = s.doOpenAICompatibleJSONWithMaxTokenRetries(ctx, s.endpoints.OpenAI, openAIReq, func(req *http.Request) {
		auth.SetProviderHeaders(req, constants.ProviderOpenAI, s.config.ProviderKeySet.OpenAIAPIKey)
	}, &openAIResp, constants.ProviderOpenAI, "OpenAI")
	if err != nil {
		return nil, err
	}

	enforceOpenAIResponseStops(&openAIResp, stops)
	return &openAIResp, nil
}

func (s *Server) useLegacyArgo() bool {
	return s != nil && s.config != nil && s.config.ArgoLegacy
}

func (s *Server) argoAPIKey() string {
	if s == nil || s.config == nil {
		return ""
	}
	if s.config.ProviderKeySet.ArgoAPIKey != "" {
		return s.config.ProviderKeySet.ArgoAPIKey
	}
	// Argo currently expects -argo-user to act as the native API key; keep this
	// fallback until Argo changes authentication.
	return s.config.ArgoUser
}

func (s *Server) configureArgoOpenAIRequest(req *http.Request) {
	if apiKey := s.argoAPIKey(); apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	}
}

func (s *Server) configureArgoAnthropicRequest(req *http.Request) {
	if apiKey := s.argoAPIKey(); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
}

func applyAnthropicBetaHeader(req *http.Request, beta string) {
	if beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
}

func (s *Server) forwardToArgoOpenAI(ctx context.Context, anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	stops := nonEmptyStopSequences(anthReq.StopSequences)

	openAIReq, err := ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo OpenAI format: %w", err)
	}
	strippedStops := stripOpenAICompatibleStop(openAIReq)
	warnOpenAICompatibleStopSpecialProcessing(ctx, "Argo OpenAI", strippedStops)
	normalizeArgoOpenAIChatRequest(openAIReq)

	var openAIResp OpenAIResponse
	err = s.doOpenAICompatibleJSONWithMaxTokenRetries(ctx, s.endpoints.ArgoOpenAI, openAIReq, s.configureArgoOpenAIRequest, &openAIResp, constants.ProviderArgo, "Argo OpenAI")
	if err != nil {
		return nil, err
	}

	enforceOpenAIResponseStops(&openAIResp, stops)
	return &openAIResp, nil
}

func (s *Server) doOpenAICompatibleJSONWithMaxTokenRetries(ctx context.Context, url string, openAIReq *OpenAIRequest, configure func(*http.Request), respBody interface{}, provider, requestName string) error {
	fieldName, baseValue, ok := openAIChatTokenLimit(openAIReq)
	if !ok {
		return s.doJSON(ctx, url, openAIReq, configure, respBody, provider, requestName)
	}

	attemptValues := []int{baseValue, baseValue + 256, baseValue + 512, baseValue + 1024}
	var firstBadRequestBody []byte

	for attempt, tokenLimit := range attemptValues {
		attemptReq := *openAIReq
		setOpenAIChatTokenLimit(&attemptReq, fieldName, tokenLimit)

		resp, _, err := s.sendProviderJSONRequest(ctx, providerJSONRequest{
			URL:         url,
			Provider:    provider,
			RequestName: requestName,
			Payload:     &attemptReq,
			Configure:   configure,
		})
		if err != nil {
			if firstBadRequestBody != nil {
				logger.From(ctx).Warnf("%s chat/completions retry with %s=%d failed: %v; returning first 400 response", requestName, fieldName, tokenLimit, err)
				return NewResponseError(http.StatusBadRequest, string(firstBadRequestBody))
			}
			return err
		}

		body, readErr := s.readResponseBody(resp)
		resp.Body.Close()
		if readErr != nil {
			if firstBadRequestBody != nil {
				logger.From(ctx).Warnf("Failed to read %s chat/completions retry response body: %v; returning first 400 response", requestName, readErr)
				return NewResponseError(http.StatusBadRequest, string(firstBadRequestBody))
			}
			return readErr
		}

		if resp.StatusCode != http.StatusBadRequest {
			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				return NewResponseError(resp.StatusCode, string(body))
			}
			if resp.StatusCode == http.StatusNoContent {
				return nil
			}
			warnUnknownFields(ctx, body, respBody, requestName+" response")
			if err := json.Unmarshal(body, respBody); err != nil {
				return fmt.Errorf("parse %s response: %w", requestName, err)
			}
			return nil
		}

		if firstBadRequestBody == nil {
			firstBadRequestBody = append([]byte(nil), body...)
		}
		if attempt == len(attemptValues)-1 {
			return NewResponseError(http.StatusBadRequest, string(firstBadRequestBody))
		}

		nextTokenLimit := attemptValues[attempt+1]
		logger.From(ctx).Warnf("%s chat/completions returned 400 with %s=%d; retrying with %s=%d (retry %d/3)", requestName, fieldName, tokenLimit, fieldName, nextTokenLimit, attempt+1)
	}

	return NewResponseError(http.StatusBadRequest, string(firstBadRequestBody))
}

func openAIChatTokenLimit(req *OpenAIRequest) (string, int, bool) {
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		return "max_completion_tokens", *req.MaxCompletionTokens, true
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return "max_tokens", *req.MaxTokens, true
	}
	return "", 0, false
}

func setOpenAIChatTokenLimit(req *OpenAIRequest, fieldName string, value int) {
	switch fieldName {
	case "max_completion_tokens":
		req.MaxCompletionTokens = &value
	case "max_tokens":
		req.MaxTokens = &value
	}
}

func (s *Server) forwardToArgoAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*AnthropicResponse, error) {
	var anthResp AnthropicResponse
	err := s.doJSON(ctx, s.endpoints.ArgoAnthropic, anthReq, func(req *http.Request) {
		s.configureArgoAnthropicRequest(req)
		applyAnthropicBetaHeader(req, anthReq.Betas)
	}, &anthResp, constants.ProviderArgo, "Argo Anthropic")
	if err != nil {
		return nil, err
	}

	return &anthResp, nil
}

func (s *Server) argoOpenAIStreamingRequest(ctx context.Context, openAIReq *OpenAIRequest) (*http.Response, error) {
	strippedStops := stripOpenAICompatibleStop(openAIReq)
	warnOpenAICompatibleStopSpecialProcessing(ctx, "Argo OpenAI", strippedStops)
	normalizeArgoOpenAIChatRequest(openAIReq)
	openAIReq.Stream = true
	return s.sendProviderStreamingJSONRequest(ctx, providerJSONRequest{
		URL:          s.endpoints.ArgoOpenAI,
		Provider:     constants.ProviderArgo,
		RequestName:  "Argo OpenAI",
		Payload:      openAIReq,
		ExtraHeaders: map[string]string{"Accept": "text/event-stream"},
		Configure:    s.configureArgoOpenAIRequest,
	})
}

func (s *Server) argoOpenAIStreamingRequestFromAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	openAIReq, err := ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo OpenAI format: %w", err)
	}
	return s.argoOpenAIStreamingRequest(ctx, openAIReq)
}

func (s *Server) argoAnthropicStreamingRequest(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	anthReq.Stream = true
	extraHeaders := map[string]string{
		"Accept": "text/event-stream",
	}
	if anthReq.Betas != "" {
		extraHeaders["anthropic-beta"] = anthReq.Betas
	}
	return s.sendProviderStreamingJSONRequest(ctx, providerJSONRequest{
		URL:          s.endpoints.ArgoAnthropic,
		Provider:     constants.ProviderArgo,
		RequestName:  "Argo Anthropic",
		Payload:      anthReq,
		ExtraHeaders: extraHeaders,
		Configure:    s.configureArgoAnthropicRequest,
	})
}

// forwardToGoogle forwards a request to the Google Gemini API
func (s *Server) forwardToGoogle(ctx context.Context, anthReq *AnthropicRequest) (*GoogleResponse, error) {
	// Convert to Google format
	googleReq, err := ConvertAnthropicToGoogle(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Google format: %w", err)
	}

	// Construct URL with model
	url, err := providers.BuildGoogleModelURL(s.endpoints.Google, anthReq.Model, "generateContent")
	if err != nil {
		return nil, fmt.Errorf("build Google URL: %w", err)
	}

	var googleResp GoogleResponse
	err = s.doJSON(ctx, url, googleReq, func(req *http.Request) {
		auth.SetProviderHeaders(req, constants.ProviderGoogle, s.config.ProviderKeySet.GoogleAPIKey)
	}, &googleResp, constants.ProviderGoogle, "Google")
	if err != nil {
		return nil, err
	}

	return &googleResp, nil
}

func (s *Server) forwardGoogleCountTokens(ctx context.Context, googleReq *GoogleRequest, model string) (*GoogleCountTokensResponse, error) {
	url, err := providers.BuildGoogleModelURL(s.endpoints.Google, model, "countTokens")
	if err != nil {
		return nil, fmt.Errorf("build Google countTokens URL: %w", err)
	}
	if googleReq != nil && googleReq.Model == "" {
		googleReq.Model = googleModelResourceName(model)
	}
	payload := &GoogleCountTokensRequest{GenerateContentRequest: googleReq}
	var googleResp GoogleCountTokensResponse
	err = s.doJSON(ctx, url, payload, func(req *http.Request) {
		auth.SetProviderHeaders(req, constants.ProviderGoogle, s.config.ProviderKeySet.GoogleAPIKey)
	}, &googleResp, constants.ProviderGoogle, "Google countTokens")
	if err != nil {
		return nil, err
	}
	return &googleResp, nil
}

func googleModelResourceName(model string) string {
	if strings.HasPrefix(model, "models/") {
		return model
	}
	return "models/" + model
}

// forwardToArgo forwards a request to the Argo API
func (s *Server) forwardToArgo(ctx context.Context, anthReq *AnthropicRequest) (*ArgoChatResponse, error) {
	// Convert to Argo format
	argoReq, err := ConvertAnthropicToArgo(ctx, anthReq, s.config.ArgoUser)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo format: %w", err)
	}

	var argoResp ArgoChatResponse
	err = s.doJSON(ctx, s.endpoints.ArgoChat, argoReq, nil, &argoResp, constants.ProviderArgo, "Argo")
	if err != nil {
		return nil, err
	}

	return &argoResp, nil
}

// forwardToAnthropic forwards a request to the Anthropic API
func (s *Server) forwardToAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*AnthropicResponse, error) {
	var anthResp AnthropicResponse
	err := s.doJSON(ctx, s.endpoints.Anthropic, anthReq, func(req *http.Request) {
		auth.SetProviderHeaders(req, constants.ProviderAnthropic, s.config.ProviderKeySet.AnthropicAPIKey)
		applyAnthropicBetaHeader(req, anthReq.Betas)
	}, &anthResp, constants.ProviderAnthropic, "Anthropic")
	if err != nil {
		return nil, err
	}

	return &anthResp, nil
}

// forwardToArgoStream forwards a request to Argo's streaming endpoint
// This should only be used when no tools are configured
func (s *Server) forwardToArgoStream(ctx context.Context, anthReq *AnthropicRequest) (io.ReadCloser, error) {
	// Convert to Argo format
	argoReq, err := ConvertAnthropicToArgo(ctx, anthReq, s.config.ArgoUser)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo format: %w", err)
	}

	resp, err := s.sendProviderStreamingJSONRequest(ctx, providerJSONRequest{
		URL:         s.endpoints.ArgoStreamChat,
		Provider:    constants.ProviderArgo,
		RequestName: "Argo stream",
		Payload:     argoReq,
	})
	if err != nil {
		return nil, err
	}

	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderArgo, resp); err != nil {
		resp.Body.Close() // Ensure body is closed after reading
		return nil, err
	}

	// Return the response body for streaming (caller is responsible for closing)
	return resp.Body, nil
}
