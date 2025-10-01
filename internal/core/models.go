package core

import (
	"lmtools/internal/constants"
	"strings"
)

// GetDefaultChatModel returns the default chat model for a provider
func GetDefaultChatModel(provider string) string {
	switch provider {
	case constants.ProviderArgo:
		return "gpt5"
	case constants.ProviderOpenAI:
		return "gpt-5"
	case constants.ProviderAnthropic:
		return "claude-opus-4-1-20250805"
	case constants.ProviderGoogle:
		return "gemini-2.5-pro"
	default:
		return "gpt5"
	}
}

// GetDefaultSmallModel returns the default small/fast model for a provider
func GetDefaultSmallModel(provider string) string {
	switch provider {
	case constants.ProviderArgo:
		return "gpt5mini"
	case constants.ProviderOpenAI:
		return "gpt-5-mini"
	case constants.ProviderAnthropic:
		return "claude-3-haiku-20240307"
	case constants.ProviderGoogle:
		return "gemini-2.5-flash"
	default:
		return "gpt5mini"
	}
}

// getBaseURL returns the base URL for a given environment
func getBaseURL(env string) string {
	// If env looks like a URL, return it as-is (for mock testing)
	if strings.HasPrefix(env, "http://") || strings.HasPrefix(env, "https://") {
		return env
	}

	// Otherwise use environment mappings
	switch env {
	case "prod":
		return ArgoProdURL
	case "dev":
		return ArgoDevURL
	default:
		// Default to prod for safety
		return ArgoProdURL
	}
}
