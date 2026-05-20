package proxy

import (
	"context"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"net/http"
	"strings"
)

// forwardToOpenAI forwards a request to the OpenAI API
func (s *Server) forwardToOpenAI(ctx context.Context, anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	// Convert to OpenAI format
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to OpenAI format: %w", err)
	}

	var openAIResp OpenAIResponse
	err = s.doJSON(ctx, s.endpoints.OpenAI, openAIReq, func(req *http.Request) error {
		return auth.ApplyProviderCredentials(req, constants.ProviderOpenAI, s.config.ProviderKeySet.OpenAIAPIKey)
	}, &openAIResp, "OpenAI")
	if err != nil {
		return nil, err
	}

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

func (s *Server) configureArgoOpenAIRequest(req *http.Request) error {
	if apiKey := s.argoAPIKey(); apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	}
	return nil
}

func (s *Server) configureArgoAnthropicRequest(req *http.Request) error {
	if apiKey := s.argoAPIKey(); apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	return nil
}

func applyAnthropicBetaHeader(req *http.Request, beta string) {
	if beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
}

func (s *Server) forwardToArgoOpenAI(ctx context.Context, anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo OpenAI format: %w", err)
	}
	normalizeArgoOpenAIChatRequest(openAIReq)

	var openAIResp OpenAIResponse
	err = s.doJSON(ctx, s.endpoints.ArgoOpenAI, openAIReq, s.configureArgoOpenAIRequest, &openAIResp, "Argo OpenAI")
	if err != nil {
		return nil, err
	}

	return &openAIResp, nil
}

func (s *Server) forwardToArgoAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*AnthropicResponse, error) {
	var anthResp AnthropicResponse
	err := s.doJSON(ctx, s.endpoints.ArgoAnthropic, anthReq, func(req *http.Request) error {
		if err := s.configureArgoAnthropicRequest(req); err != nil {
			return err
		}
		applyAnthropicBetaHeader(req, anthReq.Betas)
		return nil
	}, &anthResp, "Argo Anthropic")
	if err != nil {
		return nil, err
	}

	return &anthResp, nil
}

func (s *Server) argoOpenAIStreamingRequest(ctx context.Context, openAIReq *OpenAIRequest) (*http.Response, error) {
	normalizeArgoOpenAIChatRequest(openAIReq)
	openAIReq.Stream = true
	logger.DebugJSON(logger.From(ctx), "Outgoing Argo Streaming Request", openAIReq)
	return s.sendStreamingJSONRequest(
		ctx,
		constants.ProviderArgo,
		"Argo OpenAI",
		s.endpoints.ArgoOpenAI,
		openAIReq,
		map[string]string{"Accept": "text/event-stream"},
		s.configureArgoOpenAIRequest,
	)
}

func (s *Server) argoOpenAIStreamingRequestFromAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo OpenAI format: %w", err)
	}
	return s.argoOpenAIStreamingRequest(ctx, openAIReq)
}

func (s *Server) argoAnthropicStreamingRequest(ctx context.Context, anthReq *AnthropicRequest) (*http.Response, error) {
	logger.DebugJSON(logger.From(ctx), "Outgoing Argo Streaming Request", anthReq)
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
		constants.ProviderArgo,
		"Argo Anthropic",
		s.endpoints.ArgoAnthropic,
		anthReq,
		extraHeaders,
		s.configureArgoAnthropicRequest,
	)
}

// forwardToGoogle forwards a request to the Google Gemini API
func (s *Server) forwardToGoogle(ctx context.Context, anthReq *AnthropicRequest) (*GoogleResponse, error) {
	// Convert to Google format
	googleReq, err := s.converter.ConvertAnthropicToGoogle(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Google format: %w", err)
	}

	// Construct URL with model
	url, err := buildGoogleModelURL(s.endpoints.Google, anthReq.Model, "generateContent")
	if err != nil {
		return nil, fmt.Errorf("build Google URL: %w", err)
	}

	var googleResp GoogleResponse
	err = s.doJSON(ctx, url, googleReq, func(req *http.Request) error {
		return auth.ApplyProviderCredentials(req, constants.ProviderGoogle, s.config.ProviderKeySet.GoogleAPIKey)
	}, &googleResp, "Google")
	if err != nil {
		return nil, err
	}

	return &googleResp, nil
}

func (s *Server) forwardGoogleCountTokens(ctx context.Context, googleReq *GoogleRequest, model string) (*GoogleCountTokensResponse, error) {
	url, err := buildGoogleModelURL(s.endpoints.Google, model, "countTokens")
	if err != nil {
		return nil, fmt.Errorf("build Google countTokens URL: %w", err)
	}
	if googleReq != nil && googleReq.Model == "" {
		googleReq.Model = googleModelResourceName(model)
	}
	payload := &GoogleCountTokensRequest{GenerateContentRequest: googleReq}
	var googleResp GoogleCountTokensResponse
	err = s.doJSON(ctx, url, payload, func(req *http.Request) error {
		return auth.ApplyProviderCredentials(req, constants.ProviderGoogle, s.config.ProviderKeySet.GoogleAPIKey)
	}, &googleResp, "Google countTokens")
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
	argoReq, err := s.converter.ConvertAnthropicToArgo(ctx, anthReq, s.config.ArgoUser)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo format: %w", err)
	}

	var argoResp ArgoChatResponse
	err = s.doJSON(ctx, s.endpoints.GetArgoURL("chat"), argoReq, nil, &argoResp, "Argo")
	if err != nil {
		return nil, err
	}

	return &argoResp, nil
}

// forwardToAnthropic forwards a request to the Anthropic API
func (s *Server) forwardToAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*AnthropicResponse, error) {
	var anthResp AnthropicResponse
	err := s.doJSON(ctx, s.endpoints.Anthropic, anthReq, func(req *http.Request) error {
		if err := auth.ApplyProviderCredentials(req, constants.ProviderAnthropic, s.config.ProviderKeySet.AnthropicAPIKey); err != nil {
			return err
		}
		applyAnthropicBetaHeader(req, anthReq.Betas)
		return nil
	}, &anthResp, "Anthropic")
	if err != nil {
		return nil, err
	}

	return &anthResp, nil
}

// forwardToArgoStream forwards a request to Argo's streaming endpoint
// This should only be used when no tools are configured
func (s *Server) forwardToArgoStream(ctx context.Context, anthReq *AnthropicRequest) (io.ReadCloser, error) {
	log := logger.From(ctx)

	// Convert to Argo format
	argoReq, err := s.converter.ConvertAnthropicToArgo(ctx, anthReq, s.config.ArgoUser)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo format: %w", err)
	}

	// Log request if debug enabled
	logger.DebugJSON(log, "Outgoing Argo Streaming Request", argoReq)

	resp, err := s.sendStreamingJSONRequest(
		ctx,
		constants.ProviderArgo,
		"Argo stream",
		s.endpoints.GetArgoURL("streamchat"),
		argoReq,
		nil,
		nil,
	)
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
