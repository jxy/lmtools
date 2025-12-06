package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
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

	// Dispatch to provider-specific function
	switch provider {
	case constants.ProviderOpenAI:
		return s.fetchOpenAIModels(ctx)
	case constants.ProviderAnthropic:
		return s.fetchAnthropicModels(ctx)
	case constants.ProviderGoogle:
		return s.fetchGoogleModels(ctx)
	case constants.ProviderArgo:
		return s.fetchArgoModels(ctx)
	default:
		// For custom providers with ProviderURL, try OpenAI format
		if s.config.ProviderURL != "" {
			return s.fetchCustomProviderModels(ctx)
		}
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// fetchModels is a generic helper for fetching models from any provider
func (s *Server) fetchModels(ctx context.Context, url string, headers http.Header, applyQuery func(*http.Request)) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}

	// Apply headers
	req.Header = headers

	// Apply query parameters if needed
	if applyQuery != nil {
		applyQuery(req)
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

// fetchOpenAIModels fetches models from OpenAI API
func (s *Server) fetchOpenAIModels(ctx context.Context) ([]ModelItem, error) {
	log := logger.From(ctx)

	// Use precomputed models URL from endpoints
	url := s.endpoints.OpenAIModels
	if url == "" {
		return nil, fmt.Errorf("openai models URL not configured")
	}

	// Log sanitized URL for debugging
	sanitizedURL := sanitizeURLForLogging(url)
	log.Debugf("Fetching OpenAI models from: %s", sanitizedURL)

	headers := make(http.Header)
	if s.config.OpenAIAPIKey != "" {
		headers.Set("Authorization", "Bearer "+s.config.OpenAIAPIKey)
	}

	data, statusCode, err := s.fetchModels(ctx, url, headers, nil)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, s.handleModelsNonOK(ctx, "OpenAI", statusCode, data)
	}

	return s.parseOpenAIModels(data)
}

// fetchAnthropicModels fetches models from Anthropic API
func (s *Server) fetchAnthropicModels(ctx context.Context) ([]ModelItem, error) {
	log := logger.From(ctx)

	// Use precomputed models URL from endpoints
	url := s.endpoints.AnthropicModels
	if url == "" {
		return nil, fmt.Errorf("anthropic models URL not configured")
	}

	// Log sanitized URL for debugging
	sanitizedURL := sanitizeURLForLogging(url)
	log.Debugf("Fetching Anthropic models from: %s", sanitizedURL)

	headers := make(http.Header)
	if s.config.AnthropicAPIKey != "" {
		headers.Set("x-api-key", s.config.AnthropicAPIKey)
		headers.Set("anthropic-version", "2023-06-01")
	}

	data, statusCode, err := s.fetchModels(ctx, url, headers, nil)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, s.handleModelsNonOK(ctx, "Anthropic", statusCode, data)
	}

	return s.parseAnthropicModels(data)
}

// fetchGoogleModels fetches models from Google AI API
func (s *Server) fetchGoogleModels(ctx context.Context) ([]ModelItem, error) {
	log := logger.From(ctx)

	// Use precomputed models URL from endpoints
	url := s.endpoints.GoogleModels
	if url == "" {
		return nil, fmt.Errorf("google models URL not configured")
	}

	// Log sanitized URL for debugging (note: API key is in query params, will be removed)
	sanitizedURL := sanitizeURLForLogging(url)
	log.Debugf("Fetching Google models from: %s", sanitizedURL)

	headers := make(http.Header)

	// Google uses API key in URL query parameters
	applyQuery := func(req *http.Request) {
		if s.config.GoogleAPIKey != "" {
			q := req.URL.Query()
			q.Set("key", s.config.GoogleAPIKey)
			req.URL.RawQuery = q.Encode()
		}
	}

	data, statusCode, err := s.fetchModels(ctx, url, headers, applyQuery)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, s.handleModelsNonOK(ctx, "Google", statusCode, data)
	}

	return s.parseGoogleModels(data)
}

// fetchArgoModels fetches models from Argo API
func (s *Server) fetchArgoModels(ctx context.Context) ([]ModelItem, error) {
	log := logger.From(ctx)

	// Use precomputed models URL from endpoints
	url := s.endpoints.ArgoModels
	if url == "" {
		return nil, fmt.Errorf("argo models URL not configured")
	}

	// Sanitize URL for logging (remove credentials and query params)
	sanitizedURL := sanitizeURLForLogging(url)
	log.Debugf("Fetching Argo models from: %s", sanitizedURL)

	// Argo doesn't require authentication for models endpoint
	headers := make(http.Header)

	data, statusCode, err := s.fetchModels(ctx, url, headers, nil)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, s.handleModelsNonOK(ctx, "Argo", statusCode, data)
	}

	return s.parseArgoModels(data)
}

// fetchCustomProviderModels fetches models from a custom provider URL
func (s *Server) fetchCustomProviderModels(ctx context.Context) ([]ModelItem, error) {
	log := logger.From(ctx)

	// Construct the models endpoint URL from ProviderURL
	if s.config.ProviderURL == "" {
		return nil, fmt.Errorf("ProviderURL not configured for custom provider")
	}
	url, err := buildProviderURL(s.config.ProviderURL, "models")
	if err != nil {
		return nil, fmt.Errorf("invalid ProviderURL: %w", err)
	}

	// Log sanitized URL for debugging
	sanitizedURL := sanitizeURLForLogging(url)
	log.Debugf("Fetching custom provider models from: %s", sanitizedURL)

	headers := make(http.Header)
	// Try to add authentication based on configured keys
	if s.config.OpenAIAPIKey != "" {
		headers.Set("Authorization", "Bearer "+s.config.OpenAIAPIKey)
	} else if s.config.AnthropicAPIKey != "" {
		headers.Set("x-api-key", s.config.AnthropicAPIKey)
		headers.Set("anthropic-version", "2023-06-01")
	}

	data, statusCode, err := s.fetchModels(ctx, url, headers, nil)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, s.handleModelsNonOK(ctx, "Custom provider", statusCode, data)
	}

	// Try OpenAI format first (most common)
	if models, err := s.parseOpenAIModels(data); err == nil {
		return models, nil
	}

	// Try other formats
	if models, err := s.parseAnthropicModels(data); err == nil {
		return models, nil
	}

	return nil, fmt.Errorf("unable to parse models response from custom provider")
}
