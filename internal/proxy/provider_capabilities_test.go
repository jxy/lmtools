package proxy

import (
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"testing"
)

func TestModelProviderCapabilitiesComplete(t *testing.T) {
	providerNames := providers.ProviderIDs()
	if len(modelProviderCapabilities) == 0 {
		modelProviderCapabilitiesOnce.Do(initModelProviderCapabilities)
	}
	if len(modelProviderCapabilities) != len(providerNames) {
		t.Fatalf("modelProviderCapabilities has %d providers, want %d", len(modelProviderCapabilities), len(providerNames))
	}

	for _, provider := range providerNames {
		t.Run(provider, func(t *testing.T) {
			capability, ok := modelProviderCapabilityFor(provider)
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
		})
	}

	for provider := range modelProviderCapabilities {
		if _, ok := providers.InfoFor(provider); !ok {
			t.Fatalf("model provider capability %q is not registered in providers", provider)
		}
	}
}

func TestProviderForwardingPoliciesComplete(t *testing.T) {
	providerNames := providers.ProviderIDs()
	forwardingPoliciesOnce.Do(initForwardingPolicies)
	if len(anthropicForwardingPolicies) != len(providerNames) {
		t.Fatalf("anthropicForwardingPolicies has %d providers, want %d", len(anthropicForwardingPolicies), len(providerNames))
	}

	for _, provider := range providerNames {
		t.Run(provider, func(t *testing.T) {
			policy, ok := anthropicForwardingPolicyFor(provider)
			if !ok {
				t.Fatalf("missing Anthropic forwarding policy for provider %q", provider)
			}
			if policy.Response == nil || policy.Stream == nil {
				t.Fatal("Anthropic forwarding must be set")
			}

			openAIStream, ok := openAIStreamPolicyFor(provider)
			if provider == constants.ProviderOpenAI {
				if ok {
					t.Fatal("OpenAI provider should use direct OpenAI streaming, not conversion policy")
				}
				return
			}
			if !ok {
				t.Fatalf("missing OpenAI stream policy for provider %q", provider)
			}
			if openAIStream.Stream == nil {
				t.Fatal("OpenAI-format streaming forwarder must be set")
			}
		})
	}

	for provider := range anthropicForwardingPolicies {
		if _, ok := providers.InfoFor(provider); !ok {
			t.Fatalf("Anthropic forwarding policy %q is not registered in providers", provider)
		}
	}
	for provider := range openAIStreamPolicies {
		if provider == constants.ProviderOpenAI {
			t.Fatal("OpenAI provider should not have a conversion stream policy")
		}
		if _, ok := providers.InfoFor(provider); !ok {
			t.Fatalf("OpenAI stream policy %q is not registered in providers", provider)
		}
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
