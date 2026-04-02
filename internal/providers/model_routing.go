package providers

import (
	"lmtools/internal/constants"
	"strings"
)

// argoModelProviders maps model prefixes to their provider-native message/tool formats.
// This is static provider metadata and is shared by both core and proxy.
var argoModelProviders = map[string]string{
	"gpt":    constants.ProviderOpenAI,
	"o1":     constants.ProviderOpenAI,
	"o3":     constants.ProviderOpenAI,
	"gemini": constants.ProviderGoogle,
	"claude": constants.ProviderAnthropic,
}

// DetermineArgoModelProvider reports which provider format an Argo model should use.
// Unknown models intentionally default to OpenAI-style formatting.
func DetermineArgoModelProvider(model string) string {
	modelLower := strings.ToLower(model)
	for prefix, provider := range argoModelProviders {
		if strings.HasPrefix(modelLower, prefix) {
			return provider
		}
	}
	return constants.ProviderOpenAI
}
