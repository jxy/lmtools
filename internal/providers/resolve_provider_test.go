package providers

import "testing"

func TestResolveProviderWithFallback(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		fallback string
		want     string
	}{
		{name: "normalizes provider", provider: "OpenAI", fallback: "argo", want: "openai"},
		{name: "uses fallback when empty", provider: "", fallback: "Anthropic", want: "anthropic"},
		{name: "uses default provider when both empty", provider: "", fallback: "", want: DefaultProvider()},
	}

	for _, tt := range tests {
		if got := ResolveProviderWithFallback(tt.provider, tt.fallback); got != tt.want {
			t.Fatalf("%s: ResolveProviderWithFallback(%q, %q) = %q, want %q",
				tt.name, tt.provider, tt.fallback, got, tt.want)
		}
	}
}
