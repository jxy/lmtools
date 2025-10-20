package proxy

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
	"net/http"
)

// ServeHTTP implements http.Handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.From(ctx)

	switch r.URL.Path {
	case "/":
		log.Infof("%s %s | Root endpoint accessed", r.Method, r.URL.Path)
		s.handleRoot(w, r)
	case "/v1/messages":
		s.handleMessages(w, r)
	case "/v1/messages/count_tokens":
		s.handleCountTokens(w, r)
	case "/v1/chat/completions":
		s.handleOpenAIChatCompletions(w, r)
	case "/v1/models":
		s.handleModels(w, r)
	default:
		log.Warnf("%s %s | Path not found", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// handleRoot serves a simple health check response
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"name":   "lmtools-proxy",
	}); err != nil {
		logger.From(r.Context()).Errorf("Failed to encode response: %v", err)
	}
}

// hasCredentials checks if the server has credentials configured for the given provider
// Returns (hasCredentials, diagnosticMessage) where diagnosticMessage explains what's missing
func (s *Server) hasCredentials(provider string) (bool, string) {
	switch provider {
	case "openai":
		if s.config.OpenAIAPIKey == "" && s.config.ProviderURL == "" {
			return false, "Provider=openai: missing credentials (API key or ProviderURL)"
		}
		return true, ""
	case "anthropic":
		if s.config.AnthropicAPIKey == "" && s.config.ProviderURL == "" {
			return false, "Provider=anthropic: missing credentials (API key or ProviderURL)"
		}
		return true, ""
	case "google":
		// Google requires API key even with ProviderURL
		if s.config.GoogleAPIKey == "" {
			return false, "Provider=google: missing GoogleAPIKey"
		}
		return true, ""
	case "argo":
		if s.config.ArgoUser == "" && s.config.ProviderURL == "" {
			return false, "Provider=argo: missing ArgoUser or ProviderURL"
		}
		return true, ""
	default:
		return false, fmt.Sprintf("Provider=%s: unknown provider", provider)
	}
}
