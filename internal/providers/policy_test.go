package providers

import (
	"lmtools/internal/constants"
	"testing"
)

func TestEvaluateCredentialState(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		state    CredentialState
		surface  ValidationSurface
		wantOK   bool
		wantMsg  string
	}{
		{
			name:     "cli google accepts provider url fallback",
			provider: constants.ProviderGoogle,
			state:    CredentialState{ProviderURL: true},
			surface:  ValidationSurfaceCLI,
			wantOK:   true,
		},
		{
			name:     "proxy google accepts provider url fallback",
			provider: constants.ProviderGoogle,
			state:    CredentialState{ProviderURL: true},
			surface:  ValidationSurfaceProxy,
			wantOK:   true,
		},
		{
			name:     "cli argo still requires argo user",
			provider: constants.ProviderArgo,
			state:    CredentialState{ProviderURL: true},
			surface:  ValidationSurfaceCLI,
			wantMsg:  "Provider=argo: missing ArgoUser",
		},
		{
			name:     "proxy argo accepts provider url fallback",
			provider: constants.ProviderArgo,
			state:    CredentialState{ProviderURL: true},
			surface:  ValidationSurfaceProxy,
			wantOK:   true,
		},
		{
			name:     "proxy anthropic accepts api key",
			provider: constants.ProviderAnthropic,
			state:    CredentialState{APIKey: true},
			surface:  ValidationSurfaceProxy,
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOK, gotMsg := EvaluateCredentialState(tt.provider, tt.state, tt.surface)
			if gotOK != tt.wantOK {
				t.Fatalf("EvaluateCredentialState() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotMsg != tt.wantMsg {
				t.Fatalf("EvaluateCredentialState() msg = %q, want %q", gotMsg, tt.wantMsg)
			}
		})
	}
}

func TestValidationError(t *testing.T) {
	if got := ValidationError(constants.ProviderOpenAI, ValidationSurfaceProxy); got != "-api-key-file is required when -provider is 'openai' (unless using -provider-url)" {
		t.Fatalf("ValidationError(openai, proxy) = %q", got)
	}
	if got := ValidationError(constants.ProviderArgo, ValidationSurfaceCLI); got != "user identifier (-argo-user) is required for Argo provider" {
		t.Fatalf("ValidationError(argo, cli) = %q", got)
	}
}
