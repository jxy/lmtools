package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"strings"
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
		log.Errorf("Failed to fetch models from provider: %v", err)
		// Propagate the error to the client instead of masking it
		s.sendOpenAIError(w, ErrTypeServer, fmt.Sprintf("Failed to fetch models: %v", err), "models_fetch_error", http.StatusBadGateway)
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

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Errorf("Failed to encode models response: %v", err)
		s.sendOpenAIError(w, ErrTypeServer, "Failed to encode response", "encoding_error", http.StatusInternalServerError)
	}
}

// ModelItem represents a model in the response
type ModelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// fetchProviderModels queries the provider's models endpoint and returns the list of available models
func (s *Server) fetchProviderModels(ctx context.Context) ([]ModelItem, error) {
	provider := s.config.Provider

	// Dispatch to provider-specific function
	switch provider {
	case "openai":
		return s.fetchOpenAIModels(ctx)
	case "anthropic":
		return s.fetchAnthropicModels(ctx)
	case "google":
		return s.fetchGoogleModels(ctx)
	case "argo":
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

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return data, resp.StatusCode, nil
}

// fetchOpenAIModels fetches models from OpenAI API
func (s *Server) fetchOpenAIModels(ctx context.Context) ([]ModelItem, error) {
	url := "https://api.openai.com/v1/models"
	if s.config.ProviderURL != "" {
		url = strings.TrimRight(s.config.ProviderURL, "/") + "/models"
	}

	headers := make(http.Header)
	if s.config.OpenAIAPIKey != "" {
		headers.Set("Authorization", "Bearer "+s.config.OpenAIAPIKey)
	}

	data, statusCode, err := s.fetchModels(ctx, url, headers, nil)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("openAI HTTP %d: %s", statusCode, string(data))
	}

	return s.parseOpenAIModels(data)
}

// fetchAnthropicModels fetches models from Anthropic API
func (s *Server) fetchAnthropicModels(ctx context.Context) ([]ModelItem, error) {
	url := "https://api.anthropic.com/v1/models"
	if s.config.ProviderURL != "" {
		url = strings.TrimRight(s.config.ProviderURL, "/") + "/models"
	}

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
		return nil, fmt.Errorf("anthropic HTTP %d: %s", statusCode, string(data))
	}

	return s.parseAnthropicModels(data)
}

// fetchGoogleModels fetches models from Google AI API
func (s *Server) fetchGoogleModels(ctx context.Context) ([]ModelItem, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models"
	if s.config.ProviderURL != "" {
		url = strings.TrimRight(s.config.ProviderURL, "/") + "/models"
	}

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
		return nil, fmt.Errorf("google HTTP %d: %s", statusCode, string(data))
	}

	return s.parseGoogleModels(data)
}

// fetchArgoModels fetches models from Argo API
func (s *Server) fetchArgoModels(ctx context.Context) ([]ModelItem, error) {
	var url string
	if s.config.ProviderURL != "" {
		url = strings.TrimRight(s.config.ProviderURL, "/") + "/models"
	} else if s.config.ArgoBaseURL != "" {
		url = strings.TrimRight(s.config.ArgoBaseURL, "/") + "/api/v1/models"
	} else if s.config.ArgoEnv == "prod" {
		url = "https://apps.inside.anl.gov/argoapi/api/v1/models"
	} else {
		url = "https://apps-dev.inside.anl.gov/argoapi/api/v1/models"
	}

	// Argo doesn't require authentication for models endpoint
	headers := make(http.Header)

	data, statusCode, err := s.fetchModels(ctx, url, headers, nil)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("argo HTTP %d: %s", statusCode, string(data))
	}

	return s.parseArgoModels(data)
}

// fetchCustomProviderModels fetches models from a custom provider URL
func (s *Server) fetchCustomProviderModels(ctx context.Context) ([]ModelItem, error) {
	url := strings.TrimRight(s.config.ProviderURL, "/") + "/models"

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
		return nil, fmt.Errorf("custom provider HTTP %d: %s", statusCode, string(data))
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
