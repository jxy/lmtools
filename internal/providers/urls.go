package providers

import (
	"fmt"
	"lmtools/internal/constants"
	"net/url"
)

type EndpointSet struct {
	Base                 string
	APIBase              string
	Chat                 string
	Stream               string
	Embed                string
	Models               string
	OpenAIChat           string
	AnthropicMessages    string
	AnthropicCountTokens string
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
	return ResolveChatURLWithArgoOptions(provider, providerURL, argoEnv, model, stream, false)
}

func ResolveChatURLWithArgoOptions(provider, providerURL, argoEnv, model string, stream, argoLegacy bool) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderArgo:
		if argoLegacy {
			if stream {
				return endpoints.Stream, nil
			}
			return endpoints.Chat, nil
		}
		if DetermineArgoModelProvider(model) == constants.ProviderAnthropic {
			return endpoints.AnthropicMessages, nil
		}
		return endpoints.OpenAIChat, nil
	case constants.ProviderOpenAI, constants.ProviderAnthropic:
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
	return ResolveEmbedURLWithArgoOptions(provider, providerURL, argoEnv, false)
}

func ResolveEmbedURLWithArgoOptions(provider, providerURL, argoEnv string, _ bool) (string, error) {
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
	return ResolveModelsURLWithArgoOptions(provider, providerURL, argoEnv, false)
}

func ResolveModelsURLWithArgoOptions(provider, providerURL, argoEnv string, _ bool) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}
	return endpoints.Models, nil
}

func ResolveCountTokensURL(provider, providerURL, argoEnv, model string) (string, error) {
	return ResolveCountTokensURLWithArgoOptions(provider, providerURL, argoEnv, model, false)
}

func ResolveCountTokensURLWithArgoOptions(provider, providerURL, argoEnv, model string, argoLegacy bool) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderAnthropic:
		return endpoints.AnthropicCountTokens, nil
	case constants.ProviderArgo:
		if argoLegacy {
			return "", fmt.Errorf("%s provider does not support count_tokens in legacy mode", DisplayName(provider))
		}
		return endpoints.AnthropicCountTokens, nil
	default:
		return "", fmt.Errorf("%s provider does not support count_tokens", DisplayName(provider))
	}
}
