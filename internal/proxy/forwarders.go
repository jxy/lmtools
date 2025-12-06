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
		if key := s.config.OpenAIAPIKey; key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}, &openAIResp, "OpenAI")
	if err != nil {
		return nil, err
	}

	return &openAIResp, nil
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
		// Apply API key
		if key := s.config.GoogleAPIKey; key != "" {
			if err := auth.ApplyGoogleAPIKey(req, key); err != nil {
				// Note: We can't return the error directly here, but the request will fail later
				logger.From(ctx).Errorf("Failed to apply Google API key: %v", err)
			}
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
		req.Header.Set("anthropic-version", "2023-06-01")
		if key := s.config.AnthropicAPIKey; key != "" {
			req.Header.Set("x-api-key", key)
		}
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

	// Marshal request
	reqBody, err := json.Marshal(argoReq)
	if err != nil {
		return nil, fmt.Errorf("marshal Argo request: %w", err)
	}

	// Create HTTP request - use streamchat endpoint
	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoints.GetArgoURL("streamchat"), bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create Argo stream request: %w", err)
	}

	// Add headers
	req.Header.Set("Content-Type", "application/json")

	// Log request if debug enabled
	logger.DebugJSON(log, "Outgoing Argo Streaming Request", argoReq)

	// Send request
	resp, err := s.client.Do(ctx, req, constants.ProviderArgo)
	if err != nil {
		return nil, fmt.Errorf("send Argo stream request: %w", err)
	}

	// Check status
	if resp.StatusCode != http.StatusOK {
		// HandleStreamingError reads the body but doesn't close it
		err := s.HandleStreamingError(ctx, constants.ProviderArgo, resp)
		resp.Body.Close() // Ensure body is closed after reading
		return nil, err
	}

	// Return the response body for streaming (caller is responsible for closing)
	return resp.Body, nil
}
