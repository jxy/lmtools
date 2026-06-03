package providerrequest

import (
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/providerconfig"
	"lmtools/internal/providers"
	"net/http"
)

type ModelsRequestOptions struct {
	ProviderOptions                 providerconfig.Options
	ProviderKey                     *auth.ProviderKey
	RequireAPIKeyWithoutProviderURL bool
}

func BuildModelsRequest(opts ModelsRequestOptions) (*http.Request, error) {
	providerOpts := opts.ProviderOptions
	if err := providerOpts.Normalize(); err != nil {
		return nil, err
	}
	provider := providerOpts.Provider

	url, err := providers.ResolveModelsURL(provider, providerOpts.ProviderURL, providerOpts.ArgoEnv)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	providerKey, err := resolveModelsProviderKey(provider, providerOpts.APIKeyFile, opts.ProviderKey)
	if err != nil {
		return nil, err
	}
	if providers.RequiresAPIKey(provider) && providerKey == nil && providerOpts.ProviderURL == "" && opts.RequireAPIKeyWithoutProviderURL {
		return nil, fmt.Errorf("-api-key-file is required for %s provider when listing models", provider)
	}
	if providerKey != nil {
		auth.SetProviderHeaders(req, providerKey.Provider, providerKey.Value)
	}

	return req, nil
}

func resolveModelsProviderKey(provider, apiKeyFile string, explicit *auth.ProviderKey) (*auth.ProviderKey, error) {
	if explicit != nil && explicit.Value != "" {
		providerKey := *explicit
		if providerKey.Provider == "" {
			loaded, err := auth.NewProviderKey(provider, providerKey.Value)
			if err != nil {
				return nil, err
			}
			providerKey = loaded
		}
		return &providerKey, nil
	}
	if apiKeyFile == "" {
		return nil, nil
	}
	providerKey, err := auth.LoadProviderKeyFile(provider, apiKeyFile)
	if err != nil {
		return nil, err
	}
	return &providerKey, nil
}
