package providers

import (
	"fmt"
	"lmtools/internal/constants"
	"net/url"
)

type EndpointSet struct {
	Base    string
	APIBase string
	Chat    string
	Stream  string
	Embed   string
	Models  string
}

type endpointResolver func(providerURL, argoEnv string) (EndpointSet, error)

func BuildProviderURL(baseURL, pathToAppend string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}

	u.Path, err = url.JoinPath(u.Path, pathToAppend)
	if err != nil {
		return "", fmt.Errorf("invalid path join: %w", err)
	}

	return u.String(), nil
}

func ResolveEndpoints(provider, providerURL, argoEnv string) (EndpointSet, error) {
	descriptor, ok := descriptorFor(provider)
	if !ok {
		return EndpointSet{}, fmt.Errorf("unknown provider: %s", provider)
	}
	if descriptor.ResolveEndpoints == nil {
		return EndpointSet{}, fmt.Errorf("provider %s does not define endpoint resolution", provider)
	}
	return descriptor.ResolveEndpoints(providerURL, argoEnv)
}

func ResolveChatURL(provider, providerURL, argoEnv, model string, stream bool) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI, constants.ProviderAnthropic, constants.ProviderArgo:
		if constants.NormalizeProvider(provider) == constants.ProviderArgo && stream {
			return endpoints.Stream, nil
		}
		return endpoints.Chat, nil
	case constants.ProviderGoogle:
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		return BuildGoogleModelURL(endpoints.Base, model, action)
	default:
		return "", fmt.Errorf("unknown provider: %s", provider)
	}
}

func ResolveEmbedURL(provider, providerURL, argoEnv string) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI, constants.ProviderArgo:
		return endpoints.Embed, nil
	default:
		return "", fmt.Errorf("%s provider does not support embedding mode", DisplayName(provider))
	}
}

func ResolveModelsURL(provider, providerURL, argoEnv string) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}
	return endpoints.Models, nil
}
