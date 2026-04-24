package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/modelcatalog"
	"lmtools/internal/providers"
	"net/http"
	neturl "net/url"
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
			ID:                         m.ID,
			Object:                     m.Object,
			Created:                    m.Created,
			OwnedBy:                    m.OwnedBy,
			DisplayName:                m.DisplayName,
			CreatedAt:                  m.CreatedAt,
			MaxInputTokens:             m.MaxInputTokens,
			MaxOutputTokens:            m.MaxOutputTokens,
			SupportedGenerationMethods: append([]string(nil), m.SupportedGenerationMethods...),
			Capabilities:               m.Capabilities,
			Metadata:                   m.Metadata,
		})
	}

	log.Debugf("Models response: returning %d models", len(response.Data))

	// Send response using centralized helper (logs errors internally)
	_ = s.sendJSONResponse(ctx, w, response)
}

// ModelItem represents a model in the response
type ModelItem struct {
	ID                         string                 `json:"id"`
	Object                     string                 `json:"object"`
	Created                    int64                  `json:"created"`
	OwnedBy                    string                 `json:"owned_by"`
	DisplayName                string                 `json:"display_name,omitempty"`
	CreatedAt                  string                 `json:"created_at,omitempty"`
	MaxInputTokens             int64                  `json:"max_input_tokens,omitempty"`
	MaxOutputTokens            int64                  `json:"max_output_tokens,omitempty"`
	SupportedGenerationMethods []string               `json:"supported_generation_methods,omitempty"`
	Capabilities               map[string]interface{} `json:"capabilities,omitempty"`
	Metadata                   map[string]interface{} `json:"metadata,omitempty"`
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

	capability, ok := modelProviderCapabilityFor(provider)
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

func (s *Server) fetchModelsWithCapability(ctx context.Context, capability modelProviderCapability) ([]ModelItem, error) {
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

	return s.fetchModelPages(ctx, capability, url, prepareRequest)
}

func (s *Server) fetchModelPages(ctx context.Context, capability modelProviderCapability, url string, prepareRequest func(*http.Request)) ([]ModelItem, error) {
	switch capability.Provider {
	case constants.ProviderAnthropic:
		return s.fetchAnthropicModelPages(ctx, capability, url, prepareRequest)
	case constants.ProviderGoogle:
		return s.fetchGoogleModelPages(ctx, capability, url, prepareRequest)
	default:
		return s.fetchSingleModelPage(ctx, capability, url, prepareRequest)
	}
}

func (s *Server) fetchSingleModelPage(ctx context.Context, capability modelProviderCapability, url string, prepareRequest func(*http.Request)) ([]ModelItem, error) {
	data, statusCode, err := s.fetchModels(ctx, url, prepareRequest)
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		return nil, s.handleModelsNonOK(ctx, capability.displayName(), statusCode, data)
	}

	return capability.ParseModels(s, ctx, data)
}

func (s *Server) fetchAnthropicModelPages(ctx context.Context, capability modelProviderCapability, rawURL string, prepareRequest func(*http.Request)) ([]ModelItem, error) {
	var all []ModelItem
	afterID := ""
	seenCursors := map[string]struct{}{}

	for page := 0; page < maxModelListPages; page++ {
		pageURL, err := withQueryParams(rawURL, map[string]string{
			"limit":    "1000",
			"after_id": afterID,
		})
		if err != nil {
			return nil, err
		}

		data, statusCode, err := s.fetchModels(ctx, pageURL, prepareRequest)
		if err != nil {
			return nil, err
		}
		if statusCode != http.StatusOK {
			return nil, s.handleModelsNonOK(ctx, capability.displayName(), statusCode, data)
		}

		models, err := capability.ParseModels(s, ctx, data)
		if err != nil {
			return nil, err
		}
		all = append(all, models...)

		var response modelcatalog.AnthropicModelsResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("parse Anthropic models pagination: %w", err)
		}
		if !response.HasMore || response.LastID == "" {
			return all, nil
		}
		if _, ok := seenCursors[response.LastID]; ok {
			return nil, fmt.Errorf("anthropic models pagination repeated cursor %q", response.LastID)
		}
		seenCursors[response.LastID] = struct{}{}
		afterID = response.LastID
	}

	return nil, fmt.Errorf("anthropic models pagination exceeded %d pages", maxModelListPages)
}

func (s *Server) fetchGoogleModelPages(ctx context.Context, capability modelProviderCapability, rawURL string, prepareRequest func(*http.Request)) ([]ModelItem, error) {
	var all []ModelItem
	pageToken := ""
	seenTokens := map[string]struct{}{}

	for page := 0; page < maxModelListPages; page++ {
		pageURL, err := withQueryParams(rawURL, map[string]string{
			"pageSize":  "1000",
			"pageToken": pageToken,
		})
		if err != nil {
			return nil, err
		}

		data, statusCode, err := s.fetchModels(ctx, pageURL, prepareRequest)
		if err != nil {
			return nil, err
		}
		if statusCode != http.StatusOK {
			return nil, s.handleModelsNonOK(ctx, capability.displayName(), statusCode, data)
		}

		models, err := capability.ParseModels(s, ctx, data)
		if err != nil {
			return nil, err
		}
		all = append(all, models...)

		var response modelcatalog.GoogleModelsResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("parse Google models pagination: %w", err)
		}
		if response.NextPageToken == "" {
			return all, nil
		}
		if _, ok := seenTokens[response.NextPageToken]; ok {
			return nil, fmt.Errorf("google models pagination repeated token %q", response.NextPageToken)
		}
		seenTokens[response.NextPageToken] = struct{}{}
		pageToken = response.NextPageToken
	}

	return nil, fmt.Errorf("google models pagination exceeded %d pages", maxModelListPages)
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

const maxModelListPages = 100

func withQueryParams(rawURL string, params map[string]string) (string, error) {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	for key, value := range params {
		if value == "" {
			query.Del(key)
			continue
		}
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
