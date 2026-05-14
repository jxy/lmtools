package core

import (
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"testing"
)

var providerSpecExpectations = map[string]struct {
	expectConvertTools   bool
	expectGenericRequest bool
}{
	constants.ProviderOpenAI: {
		expectConvertTools:   true,
		expectGenericRequest: true,
	},
	constants.ProviderAnthropic: {
		expectConvertTools:   true,
		expectGenericRequest: true,
	},
	constants.ProviderGoogle: {
		expectConvertTools:   true,
		expectGenericRequest: true,
	},
	constants.ProviderArgo: {
		expectConvertTools: false,
	},
}

func TestProviderSpecsCompleteness(t *testing.T) {
	providerIDs := providers.ProviderIDs()
	if len(providerSpecRegistry()) != len(providerIDs) {
		t.Fatalf("providerSpecRegistry() has %d providers, want %d", len(providerSpecRegistry()), len(providerIDs))
	}

	for _, provider := range providerIDs {
		t.Run(provider, func(t *testing.T) {
			tt, ok := providerSpecExpectations[provider]
			if !ok {
				t.Fatalf("missing provider spec expectations for %q", provider)
			}
			spec, err := providerSpecForName(provider)
			if err != nil {
				t.Fatalf("providerSpecForName(%q) error = %v", provider, err)
			}
			if spec.Provider != provider {
				t.Fatalf("spec.Provider = %q, want %q", spec.Provider, provider)
			}
			if spec.displayName() == "" {
				t.Fatal("displayName() must not be empty")
			}
			if spec.BuildChat == nil {
				t.Fatal("BuildChat must be set")
			}
			if spec.HandleStream == nil {
				t.Fatal("HandleStream must be set")
			}
			if spec.ParseResponse == nil {
				t.Fatal("ParseResponse must be set")
			}
			if got := spec.supportsEmbeddings(); got != providers.SupportsEmbeddings(provider) {
				t.Fatalf("supportsEmbeddings() = %v, want %v", got, providers.SupportsEmbeddings(provider))
			}
			if got := spec.BuildEmbed != nil; got != spec.supportsEmbeddings() {
				t.Fatalf("BuildEmbed set = %v, want supportsEmbeddings()=%v", got, spec.supportsEmbeddings())
			}
			if got := spec.ConvertTools != nil; got != tt.expectConvertTools {
				t.Fatalf("ConvertTools set = %v, want %v", got, tt.expectConvertTools)
			}
			genericRequest := spec.supportsGenericRequest()
			if genericRequest != tt.expectGenericRequest {
				t.Fatalf("generic request support = %v, want %v", genericRequest, tt.expectGenericRequest)
			}
			if got := spec.usesOutOfBandSystemPrompt(); got != providers.UsesOutOfBandSystemPrompt(provider) {
				t.Fatalf("usesOutOfBandSystemPrompt() = %v, want %v", got, providers.UsesOutOfBandSystemPrompt(provider))
			}
		})
	}

	for provider := range providerSpecRegistry() {
		if _, ok := providers.InfoFor(provider); !ok {
			t.Fatalf("provider spec %q is not registered in providers", provider)
		}
	}
}
