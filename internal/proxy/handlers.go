package proxy

import (
	"lmtools/internal/logger"
	"net/http"
	"strings"
)

// ServeHTTP implements http.Handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logger.From(ctx)

	if r.URL.Path == "/v1/conversations" || strings.HasPrefix(r.URL.Path, "/v1/conversations/") {
		s.handleOpenAIConversations(w, r)
		return
	}

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
	case "/v1/responses":
		s.handleOpenAIResponses(w, r)
	case "/v1/responses/input_tokens":
		s.handleOpenAIResponsesInputTokens(w, r)
	case "/v1/responses/compact":
		s.handleOpenAIResponsesCompact(w, r)
	case "/v1/models":
		s.handleModels(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/v1/responses/") {
			s.handleOpenAIResponsesLifecycle(w, r)
			return
		}
		log.Warnf("%s %s | Path not found", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// handleRoot serves a simple health check response
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_ = s.sendJSONResponse(r.Context(), w, map[string]string{
		"status": "ok",
		"name":   "lmtools-proxy",
	})
}

// hasCredentials checks if the server has credentials configured for the given provider
// Returns (hasCredentials, diagnosticMessage) where diagnosticMessage explains what's missing
func (s *Server) hasCredentials(provider string) (bool, string) {
	return evaluateProviderCredentials(provider, newProviderCredentialState(s.config))
}
