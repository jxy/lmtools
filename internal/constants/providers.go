package constants

import "strings"

var validProviders = []string{
	ProviderArgo,
	ProviderOpenAI,
	ProviderGoogle,
	ProviderAnthropic,
}

// ValidProviders returns the supported provider list in display order.
func ValidProviders() []string {
	providers := make([]string, len(validProviders))
	copy(providers, validProviders)
	return providers
}

// JoinedProviders returns the supported provider list as a comma-separated string.
func JoinedProviders() string {
	return strings.Join(validProviders, ", ")
}

// NormalizeProvider normalizes provider names for comparison and validation.
func NormalizeProvider(provider string) string {
	return strings.ToLower(provider)
}

// IsValidProvider reports whether the provider name is supported.
func IsValidProvider(provider string) bool {
	normalized := NormalizeProvider(provider)
	for _, candidate := range validProviders {
		if normalized == candidate {
			return true
		}
	}
	return false
}
