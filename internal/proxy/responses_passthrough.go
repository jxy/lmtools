package proxy

import (
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/http"
)

// responsesPassthroughTarget describes the upstream Responses API a request is
// forwarded to verbatim. OpenAI always has one; Argo has one when the server was
// started with -openai-responses.
type responsesPassthroughTarget struct {
	URL string
	// Provider is the credential/retry provider for the upstream call.
	Provider  string
	Configure func(*http.Request)
}

// logName labels this target's calls in logs, for example "Argo responses".
func (t responsesPassthroughTarget) logName(kind string) string {
	return providers.DisplayName(t.Provider) + " " + kind
}

// responsesPassthroughTarget resolves the upstream Responses API for the configured
// provider. ok is false when the provider has no direct Responses backend or its URL
// is unset, in which case /v1/responses uses the converted path instead.
func (s *Server) responsesPassthroughTarget() (responsesPassthroughTarget, bool) {
	if s == nil || s.config == nil || s.endpoints == nil {
		return responsesPassthroughTarget{}, false
	}
	var target responsesPassthroughTarget
	switch s.config.Provider {
	case constants.ProviderOpenAI:
		target = responsesPassthroughTarget{
			URL:       s.endpoints.OpenAIResponses,
			Provider:  constants.ProviderOpenAI,
			Configure: s.configureOpenAIRequest,
		}
	case constants.ProviderArgo:
		if !s.config.OpenAIResponses || s.useLegacyArgo() {
			return responsesPassthroughTarget{}, false
		}
		// Responses is an OpenAI-wire API, so authenticate it exactly like Argo's
		// OpenAI-compatible chat/completions route.
		target = responsesPassthroughTarget{
			URL:       s.endpoints.ArgoResponses,
			Provider:  constants.ProviderArgo,
			Configure: s.configureArgoOpenAIRequest,
		}
	default:
		return responsesPassthroughTarget{}, false
	}
	if target.URL == "" {
		return responsesPassthroughTarget{}, false
	}
	return target, true
}

// responsesPassthroughEnabled reports whether the configured provider has a direct
// Responses backend at all. Use it for lifecycle and conversation routes, which
// carry an id or nothing rather than a model.
func (s *Server) responsesPassthroughEnabled() bool {
	_, ok := s.responsesPassthroughTarget()
	return ok
}

// useDirectResponsesForModel reports whether a model-bearing Responses request is
// forwarded verbatim rather than converted. The proxy forwards to OpenAI with or
// without -openai-responses, which is the one place it diverges from the shared
// providers.UsesResponsesAPIWire rule; Argo needs the flag and serves /v1/responses
// for gpt* models only, so other Argo models still take the converted path.
func (s *Server) useDirectResponsesForModel(mappedModel string) bool {
	if !s.responsesPassthroughEnabled() {
		return false
	}
	return s.config.Provider == constants.ProviderOpenAI ||
		providers.UsesResponsesAPIWire(s.config.Provider, mappedModel, s.config.OpenAIResponses, s.config.ArgoLegacy)
}
