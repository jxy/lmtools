package core

import "lmtools/internal/providers"

// configuredSystemPrompt returns the raw configured prompt without applying defaults.
// This keeps inline system messages authoritative when callers intentionally bypass
// the higher-level effective/defaulted system handling.
func configuredSystemPrompt(cfg SystemConfig) string {
	return cfg.GetSystem()
}

// resolvedSystemPrompt returns an explicit override when one is provided, otherwise
// it falls back to the effective system prompt (including defaults).
func resolvedSystemPrompt(cfg SystemConfig, override string) string {
	if override != "" {
		return override
	}
	return cfg.GetEffectiveSystem()
}

// providerUsesOutOfBandSystemPrompt reports whether a provider expects system
// prompts outside the inline message list.
func providerUsesOutOfBandSystemPrompt(provider string) bool {
	return providers.UsesOutOfBandSystemPrompt(provider)
}
