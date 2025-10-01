package core

import (
	"lmtools/internal/constants"
	"strings"
)

// argoModelProviders maps model prefixes to their provider formats
// This is the single source of truth for model-to-provider mapping
var argoModelProviders = map[string]string{
	"gpt":    constants.ProviderOpenAI,
	"o1":     constants.ProviderOpenAI,
	"o3":     constants.ProviderOpenAI,
	"gemini": constants.ProviderGoogle,
	"claude": constants.ProviderAnthropic,
}

// DetermineArgoModelProvider determines the underlying provider for an Argo model
// This is the centralized function that should be used across the codebase
func DetermineArgoModelProvider(model string) string {
	modelLower := strings.ToLower(model)

	// Check known prefixes
	for prefix, provider := range argoModelProviders {
		if strings.HasPrefix(modelLower, prefix) {
			return provider
		}
	}

	// Default to OpenAI format for unknown models
	return constants.ProviderOpenAI
}
