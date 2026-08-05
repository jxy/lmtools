package providers

import (
	"lmtools/internal/constants"
	"testing"
)

func TestArgoModelSupportsResponses(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gpt5", true},
		{"gpt5mini", true},
		{"gpt-5", true},
		{"GPT-5", true},
		{"  gpt4o  ", true},
		{"gpto3", true},
		{"claude-opus-4-1-20250805", false},
		{"gemini25pro", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ArgoModelSupportsResponses(tt.model); got != tt.want {
			t.Errorf("ArgoModelSupportsResponses(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestUsesResponsesAPIWire(t *testing.T) {
	tests := []struct {
		name             string
		provider         string
		model            string
		responsesEnabled bool
		argoLegacy       bool
		want             bool
	}{
		{name: "openai enabled", provider: constants.ProviderOpenAI, model: "gpt-5", responsesEnabled: true, want: true},
		{name: "openai enabled non-gpt model", provider: constants.ProviderOpenAI, model: "o3", responsesEnabled: true, want: true},
		{name: "openai disabled", provider: constants.ProviderOpenAI, model: "gpt-5", want: false},
		{name: "argo gpt model", provider: constants.ProviderArgo, model: "gpt5", responsesEnabled: true, want: true},
		{name: "argo claude model", provider: constants.ProviderArgo, model: "claude-opus-4-1-20250805", responsesEnabled: true, want: false},
		{name: "argo other model", provider: constants.ProviderArgo, model: "gemini25pro", responsesEnabled: true, want: false},
		{name: "argo legacy", provider: constants.ProviderArgo, model: "gpt5", responsesEnabled: true, argoLegacy: true, want: false},
		{name: "argo disabled", provider: constants.ProviderArgo, model: "gpt5", want: false},
		{name: "anthropic", provider: constants.ProviderAnthropic, model: "claude-opus-4-1-20250805", responsesEnabled: true, want: false},
		{name: "google", provider: constants.ProviderGoogle, model: "gemini-2.5-pro", responsesEnabled: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UsesResponsesAPIWire(tt.provider, tt.model, tt.responsesEnabled, tt.argoLegacy); got != tt.want {
				t.Fatalf("UsesResponsesAPIWire(%q, %q, %v, %v) = %v, want %v",
					tt.provider, tt.model, tt.responsesEnabled, tt.argoLegacy, got, tt.want)
			}
		})
	}
}
