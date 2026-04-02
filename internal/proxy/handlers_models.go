package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
	"net/http"
)

// handleModels handles the models listing endpoint
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.From(ctx)

	// Log endpoint access
	log.Infof("%s %s | Models listing endpoint", r.Method, r.URL.Path)

	if r.Method != http.MethodGet {
		s.sendOpenAIError(w, ErrTypeInvalidRequest, "Method not allowed", "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}

	// Build response with available models
	// Fetch models from the provider
	models, err := s.fetchProviderModels(ctx)
	if err != nil {
		s.sendProviderErrorAsOpenAI(ctx, w, s.config.Provider, err)
		return
	}

	response := struct {
		Object string      `json:"object"`
		Data   []ModelItem `json:"data"`
	}{
		Object: "list",
		Data:   []ModelItem{},
	}

	// Use the fetched models
	for _, m := range models {
		response.Data = append(response.Data, ModelItem{
			ID:      m.ID,
			Object:  m.Object,
			Created: m.Created,
			OwnedBy: m.OwnedBy,
		})
	}

	log.Debugf("Models response: returning %d models", len(response.Data))

	// Send response using centralized helper (logs errors internally)
	_ = s.sendJSONResponse(ctx, w, response)
}

// ModelItem represents a model in the response
type ModelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// handleModelsNonOK centralizes error handling for non-200 status codes from models endpoints
// Uses centralized error helpers for consistency with streaming error handling
func (s *Server) handleModelsNonOK(ctx context.Context, provider string, status int, body []byte) error {
	// Log the error response using centralized helper
	logErrorResponse(ctx, provider, status, body)

	// Return ResponseError for consistent error handling
	return NewResponseError(status, string(body))
}

// fetchProviderModels queries the provider's models endpoint and returns the list of available models
func (s *Server) fetchProviderModels(ctx context.Context) ([]ModelItem, error) {
	provider := s.config.Provider

	capability, ok := proxyProviderCapabilityFor(provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	return s.fetchModelsWithCapability(ctx, capability)
}

// fetchModels is a generic helper for fetching models from any provider
func (s *Server) fetchModels(ctx context.Context, url string, prepareRequest func(*http.Request)) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	if prepareRequest != nil {
		prepareRequest(req)
	}

	// Use the appropriate provider name for logging
	provider := s.config.Provider
	if provider == "" {
		provider = "custom"
	}

	resp, err := s.client.Do(ctx, req, provider)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()

	// Use safe response body reading with size limit
	data, err := s.readResponseBody(resp)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return data, resp.StatusCode, nil
}

func (s *Server) fetchModelsWithCapability(ctx context.Context, capability proxyProviderCapability) ([]ModelItem, error) {
	log := logger.From(ctx)

	url, err := providers.ResolveModelsURL(s.config.Provider, s.config.ProviderURL, s.config.ArgoEnv)
	if err != nil {
		return nil, err
	}

	sanitizedURL := sanitizeURLForLogging(url)
	log.Debugf("Fetching %s models from: %s", capability.displayName(), sanitizedURL)

	prepareRequest := func(req *http.Request) {
		if apiKey := s.apiKeyForProvider(s.config.Provider); apiKey != "" {
			_ = auth.ApplyProviderCredentials(req, s.config.Provider, apiKey)
		}
	}

	data, statusCode, err := s.fetchModels(ctx, url, prepareRequest)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, s.handleModelsNonOK(ctx, capability.displayName(), statusCode, data)
	}

	return capability.ParseModels(s, data)
}

func (s *Server) apiKeyForProvider(provider string) string {
	if s == nil || s.config == nil {
		return ""
	}

	switch provider {
	case constants.ProviderOpenAI:
		return s.config.OpenAIAPIKey
	case constants.ProviderAnthropic:
		return s.config.AnthropicAPIKey
	case constants.ProviderGoogle:
		return s.config.GoogleAPIKey
	default:
		return ""
	}
}
