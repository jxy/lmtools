package core

import (
	"lmtools/internal/providers"
)

// GetDefaultChatModel returns the default chat model for a provider
func GetDefaultChatModel(provider string) string {
	return providers.DefaultChatModel(provider)
}

// GetDefaultSmallModel returns the default small/fast model for a provider
func GetDefaultSmallModel(provider string) string {
	return providers.DefaultSmallModel(provider)
}
