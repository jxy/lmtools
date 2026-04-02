package providers

import (
	"lmtools/internal/constants"
	"testing"
)

func TestDetermineArgoModelProvider(t *testing.T) {
	tests := []struct {
		model    string
		provider string
	}{
		{model: "gpt-5", provider: "openai"},
		{model: "o3-mini", provider: "openai"},
		{model: "gemini-2.5-pro", provider: "google"},
		{model: "claude-opus-4-1", provider: "anthropic"},
		{model: "unknown-model", provider: "openai"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := DetermineArgoModelProvider(tt.model); got != tt.provider {
				t.Fatalf("DetermineArgoModelProvider(%q) = %q, want %q", tt.model, got, tt.provider)
			}
		})
	}
}

func TestUnknownProviderMetadataDefaultsAreExplicit(t *testing.T) {
	if got := DefaultChatModel("unknown-provider"); got != "" {
		t.Fatalf("DefaultChatModel(unknown-provider) = %q, want empty string", got)
	}
	if got := DefaultSmallModel("unknown-provider"); got != "" {
		t.Fatalf("DefaultSmallModel(unknown-provider) = %q, want empty string", got)
	}
	if got := CredentialKindFor("unknown-provider"); got != CredentialKindNone {
		t.Fatalf("CredentialKindFor(unknown-provider) = %q, want %q", got, CredentialKindNone)
	}
}

func TestProviderDescriptorsAreComplete(t *testing.T) {
	requiredProviders := []string{
		constants.ProviderArgo,
		constants.ProviderOpenAI,
		constants.ProviderAnthropic,
		constants.ProviderGoogle,
	}

	for _, provider := range requiredProviders {
		descriptor, ok := descriptorFor(provider)
		if !ok {
			t.Fatalf("descriptorFor(%q) = missing", provider)
		}
		if descriptor.Info.ID != provider {
			t.Fatalf("descriptorFor(%q).Info.ID = %q", provider, descriptor.Info.ID)
		}
		if descriptor.ResolveEndpoints == nil {
			t.Fatalf("descriptorFor(%q).ResolveEndpoints = nil", provider)
		}
		if _, ok := descriptor.Policies[ValidationSurfaceCLI]; !ok {
			t.Fatalf("descriptorFor(%q) missing CLI policy", provider)
		}
		if _, ok := descriptor.Policies[ValidationSurfaceProxy]; !ok {
			t.Fatalf("descriptorFor(%q) missing proxy policy", provider)
		}
	}
}

func TestProviderDescriptorsResolveEndpoints(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		providerURL string
		argoEnv     string
	}{
		{
			name:     "argo default env",
			provider: constants.ProviderArgo,
			argoEnv:  "dev",
		},
		{
			name:     "openai default",
			provider: constants.ProviderOpenAI,
		},
		{
			name:     "anthropic default",
			provider: constants.ProviderAnthropic,
		},
		{
			name:        "google custom base",
			provider:    constants.ProviderGoogle,
			providerURL: "https://example.test/v1beta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoints, err := ResolveEndpoints(tt.provider, tt.providerURL, tt.argoEnv)
			if err != nil {
				t.Fatalf("ResolveEndpoints() error = %v", err)
			}
			if endpoints.Models == "" {
				t.Fatal("Models endpoint must not be empty")
			}
			if SupportsEmbeddings(tt.provider) && endpoints.Embed == "" {
				t.Fatal("Embed endpoint must not be empty for embedding providers")
			}
			switch tt.provider {
			case constants.ProviderGoogle:
				if endpoints.Base == "" {
					t.Fatal("Base endpoint must not be empty")
				}
			default:
				if endpoints.Chat == "" {
					t.Fatal("Chat endpoint must not be empty")
				}
			}
		})
	}
}
