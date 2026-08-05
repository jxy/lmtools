package providers

import (
	"lmtools/internal/constants"
	"strings"
)

// DetermineArgoModelProvider reports which provider format an Argo model should use.
// Argo's native compatibility layer is binary:
//   - Claude models use Anthropic's messages wire format
//   - Everything else uses OpenAI's chat/completions wire format
func DetermineArgoModelProvider(model string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude") {
		return constants.ProviderAnthropic
	}
	return constants.ProviderOpenAI
}

// ArgoModelSupportsResponses reports whether an Argo model is served by Argo's
// OpenAI-compatible Responses endpoint. Argo currently exposes /v1/responses for
// its gpt* models only; every other model keeps the chat/completions or messages
// wire format chosen by DetermineArgoModelProvider.
func ArgoModelSupportsResponses(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt")
}

// UsesResponsesAPIWire reports whether a request should be rendered and parsed as
// OpenAI Responses. It is the single routing predicate shared by lmc and apiproxy
// so request building and response parsing can never disagree.
func UsesResponsesAPIWire(provider, model string, responsesEnabled, argoLegacy bool) bool {
	if !responsesEnabled {
		return false
	}
	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI:
		return true
	case constants.ProviderArgo:
		return !argoLegacy && ArgoModelSupportsResponses(model)
	default:
		return false
	}
}
