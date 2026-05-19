package proxy

import (
	"lmtools/internal/constants"
	"lmtools/internal/providers"
)

type providerCredentialState struct {
	ProviderURL  bool
	OpenAIKey    bool
	AnthropicKey bool
	GoogleKey    bool
	ArgoKey      bool
	ArgoUser     bool
}

func newProviderCredentialState(cfg *Config) providerCredentialState {
	if cfg == nil {
		return providerCredentialState{}
	}
	return providerCredentialState{
		ProviderURL:  cfg.ProviderURL != "",
		OpenAIKey:    cfg.ProviderKeySet.OpenAIAPIKey != "",
		AnthropicKey: cfg.ProviderKeySet.AnthropicAPIKey != "",
		GoogleKey:    cfg.ProviderKeySet.GoogleAPIKey != "",
		ArgoKey:      cfg.ProviderKeySet.ArgoAPIKey != "",
		ArgoUser:     cfg.ArgoUser != "",
	}
}

func (s providerCredentialState) forProvider(provider string) providers.CredentialState {
	apiKey := false
	switch provider {
	case constants.ProviderOpenAI:
		apiKey = s.OpenAIKey
	case constants.ProviderAnthropic:
		apiKey = s.AnthropicKey
	case constants.ProviderGoogle:
		apiKey = s.GoogleKey
	case constants.ProviderArgo:
		apiKey = s.ArgoKey
	}

	return providers.CredentialState{
		ProviderURL: s.ProviderURL,
		APIKey:      apiKey,
		ArgoUser:    s.ArgoUser,
	}
}

func evaluateProviderCredentials(provider string, state providerCredentialState) (bool, string) {
	return providers.EvaluateCredentialState(provider, state.forProvider(provider), providers.ValidationSurfaceProxy)
}

func providerValidationError(provider string) string {
	return providers.ValidationError(provider, providers.ValidationSurfaceProxy)
}
