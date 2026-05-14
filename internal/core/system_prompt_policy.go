package core

import "lmtools/internal/providers"

// configuredSystemPrompt returns the raw configured prompt without applying defaults.
// This keeps inline system messages authoritative when callers intentionally bypass
// the higher-level effective/defaulted system handling.
func configuredSystemPrompt(cfg RequestOptions) string {
	return cfg.GetSystem()
}

// resolvedSystemPrompt returns an explicit override when one is provided, otherwise
// it falls back to the effective system prompt (including defaults).
func resolvedSystemPrompt(cfg RequestOptions, override string) string {
	if override != "" {
		return override
	}
	return cfg.GetEffectiveSystem()
}

func resolvedBuildSystemPrompt(cfg RequestOptions, override string, overrideSet bool) (string, bool) {
	if overrideSet || override != "" {
		return override, true
	}
	return cfg.GetEffectiveSystem(), cfg.IsSystemExplicitlySet()
}

// providerUsesOutOfBandSystemPrompt reports whether a provider expects system
// prompts outside the inline message list.
func providerUsesOutOfBandSystemPrompt(provider string) bool {
	return providers.UsesOutOfBandSystemPrompt(provider)
}
