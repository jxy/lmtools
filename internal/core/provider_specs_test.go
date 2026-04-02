package core

import (
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"testing"
)

func TestProviderSpecsCompleteness(t *testing.T) {
	tests := []struct {
		provider             string
		expectConvertTools   bool
		expectGenericRequest bool
	}{
		{
			provider:             constants.ProviderOpenAI,
			expectConvertTools:   true,
			expectGenericRequest: true,
		},
		{
			provider:             constants.ProviderAnthropic,
			expectConvertTools:   true,
			expectGenericRequest: true,
		},
		{
			provider:             constants.ProviderGoogle,
			expectConvertTools:   true,
			expectGenericRequest: true,
		},
		{
			provider:           constants.ProviderArgo,
			expectConvertTools: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			spec, err := providerSpecForName(tt.provider)
			if err != nil {
				t.Fatalf("providerSpecForName(%q) error = %v", tt.provider, err)
			}
			if spec.Provider != tt.provider {
				t.Fatalf("spec.Provider = %q, want %q", spec.Provider, tt.provider)
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
			if got := spec.supportsEmbeddings(); got != providers.SupportsEmbeddings(tt.provider) {
				t.Fatalf("supportsEmbeddings() = %v, want %v", got, providers.SupportsEmbeddings(tt.provider))
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
			if got := spec.usesOutOfBandSystemPrompt(); got != providers.UsesOutOfBandSystemPrompt(tt.provider) {
				t.Fatalf("usesOutOfBandSystemPrompt() = %v, want %v", got, providers.UsesOutOfBandSystemPrompt(tt.provider))
			}
		})
	}
}
