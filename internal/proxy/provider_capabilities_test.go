package proxy

import (
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"testing"
)

func TestProxyProviderCapabilitiesComplete(t *testing.T) {
	providerNames := []string{
		constants.ProviderOpenAI,
		constants.ProviderAnthropic,
		constants.ProviderGoogle,
		constants.ProviderArgo,
	}

	for _, provider := range providerNames {
		t.Run(provider, func(t *testing.T) {
			capability, ok := proxyProviderCapabilityFor(provider)
			if !ok {
				t.Fatalf("missing capability for provider %q", provider)
			}
			if capability.Provider != provider {
				t.Fatalf("capability provider = %q, want %q", capability.Provider, provider)
			}
			if capability.displayName() != providers.DisplayName(provider) {
				t.Fatalf("displayName() = %q, want %q", capability.displayName(), providers.DisplayName(provider))
			}
			if providers.SupportsModelsEndpoint(provider) && capability.ParseModels == nil {
				t.Fatal("ParseModels must be set when models endpoint is supported")
			}
			if capability.RenderTyped == nil {
				t.Fatal("RenderTyped must be set")
			}
			if capability.AnthropicResponse == nil || capability.AnthropicStream == nil {
				t.Fatal("Anthropic forwarding must be set")
			}
			if provider != constants.ProviderOpenAI && capability.OpenAIStream == nil {
				t.Fatal("OpenAI-format streaming forwarder must be set")
			}
		})
	}
}

func TestEvaluateProviderCredentials(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		state    providerCredentialState
		wantOK   bool
		wantMsg  string
	}{
		{
			name:     "openai accepts provider URL fallback",
			provider: constants.ProviderOpenAI,
			state:    providerCredentialState{ProviderURL: true},
			wantOK:   true,
		},
		{
			name:     "anthropic requires key or provider URL",
			provider: constants.ProviderAnthropic,
			state:    providerCredentialState{},
			wantMsg:  "Provider=anthropic: missing credentials (API key or ProviderURL)",
		},
		{
			name:     "google accepts provider URL fallback",
			provider: constants.ProviderGoogle,
			state:    providerCredentialState{ProviderURL: true},
			wantOK:   true,
		},
		{
			name:     "argo accepts argo user",
			provider: constants.ProviderArgo,
			state:    providerCredentialState{ArgoUser: true},
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOK, gotMsg := evaluateProviderCredentials(tt.provider, tt.state)
			if gotOK != tt.wantOK {
				t.Fatalf("evaluateProviderCredentials() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotMsg != tt.wantMsg {
				t.Fatalf("evaluateProviderCredentials() msg = %q, want %q", gotMsg, tt.wantMsg)
			}
		})
	}
}
