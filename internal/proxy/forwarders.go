package proxy

import (
	"context"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
	"net/http"
)

// forwardToOpenAI forwards a request to the OpenAI API
func (s *Server) forwardToOpenAI(ctx context.Context, anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	// Convert to OpenAI format
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to OpenAI format: %w", err)
	}

	var openAIResp OpenAIResponse
	err = s.doJSON(ctx, s.endpoints.OpenAI, openAIReq, func(req *http.Request) {
		_ = auth.ApplyProviderCredentials(req, constants.ProviderOpenAI, s.config.OpenAIAPIKey)
	}, &openAIResp, "OpenAI")
	if err != nil {
		return nil, err
	}

	return &openAIResp, nil
}

func (s *Server) argoWireProvider(model string) string {
	return providers.DetermineArgoModelProvider(model)
}

func (s *Server) useLegacyArgo() bool {
	return s != nil && s.config != nil && s.config.ArgoLegacy
}

func (s *Server) configureArgoOpenAIRequest(req *http.Request) {
	if s.config.ArgoAPIKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.config.ArgoAPIKey))
	}
}

func (s *Server) configureArgoAnthropicRequest(req *http.Request) {
	if s.config.ArgoAPIKey != "" {
		req.Header.Set("x-api-key", s.config.ArgoAPIKey)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
}

func (s *Server) forwardToArgoOpenAI(ctx context.Context, anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("convert to Argo OpenAI format: %w", err)
	}

	var openAIResp OpenAIResponse
	err = s.doJSON(ctx, s.endpoints.ArgoOpenAI, openAIReq, s.configureArgoOpenAIRequest, &openAIResp, "Argo OpenAI")
	if err != nil {
		return nil, err
	}

	return &openAIResp, nil
}

func (s *Server) forwardToArgoAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*AnthropicResponse, error) {
	var anthResp AnthropicResponse
	err := s.doJSON(ctx, s.endpoints.ArgoAnthropic, anthReq, s.configureArgoAnthropicRequest, &anthResp, "Argo Anthropic")
	if err != nil {
		return nil, err
	}

	return &anthResp, nil
}

func (s *Server) argoOpenAIStreamingRequest(ctx context.Context, openAIReq *OpenAIRequest) (*http.Response, error) {
	logger.DebugJSON(logger.From(ctx), "Outgoing Argo Streaming Request", openAIReq)
	openAIReq.Stream = true
	return s.sendStreamingJSONRequest(
		ctx,
		constants.ProviderArgo,
		"Argo OpenAI",
		s.endpoints.ArgoOpenAI,
		openAIReq,
		map[string]string{"Accept": "text/event-stream"},
		noErrorRequestConfigurer(s.configureArgoOpenAIRequest),
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
	return s.sendStreamingJSONRequest(
		ctx,
		constants.ProviderArgo,
		"Argo Anthropic",
		s.endpoints.ArgoAnthropic,
		anthReq,
		map[string]string{
			"Accept":            "text/event-stream",
			"anthropic-version": "2023-06-01",
		},
		noErrorRequestConfigurer(s.configureArgoAnthropicRequest),
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
	err = s.doJSON(ctx, url, googleReq, func(req *http.Request) {
		if err := auth.ApplyProviderCredentials(req, constants.ProviderGoogle, s.config.GoogleAPIKey); err != nil {
			// Note: We can't return the error directly here, but the request will fail later.
			logger.From(ctx).Errorf("Failed to apply Google API key: %v", err)
		}
	}, &googleResp, "Google")
	if err != nil {
		return nil, err
	}

	return &googleResp, nil
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
	err := s.doJSON(ctx, s.endpoints.Anthropic, anthReq, func(req *http.Request) {
		_ = auth.ApplyProviderCredentials(req, constants.ProviderAnthropic, s.config.AnthropicAPIKey)
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
