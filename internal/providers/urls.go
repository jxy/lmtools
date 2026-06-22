package providers

import (
	"fmt"
	"lmtools/internal/constants"
	"net/url"
	"strings"
)

type EndpointSet struct {
	Base                 string
	Chat                 string
	Stream               string
	Embed                string
	Responses            string
	Models               string
	OpenAIChat           string
	AnthropicMessages    string
	AnthropicCountTokens string
}

type endpointResolver func(providerURL, argoEnv string) (EndpointSet, error)

func ValidateProviderURL(providerURL, provider string) error {
	u, err := url.Parse(providerURL)
	if err != nil {
		return fmt.Errorf("invalid %s ProviderURL: %w", provider, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s ProviderURL must use http or https scheme", provider)
	}
	if u.Host == "" {
		return fmt.Errorf("%s ProviderURL must include a host", provider)
	}
	return nil
}

func endpointBaseURL(endpoint, suffix string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return strings.TrimSuffix(endpoint, suffix)
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), suffix)
	return u.String()
}

func BuildProviderURL(baseURL, pathToAppend string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host == "" {
		return "", fmt.Errorf("invalid base URL: http(s) URL must include a host")
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

func ResolveResponsesURL(provider, providerURL, argoEnv string) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI:
		return endpoints.Responses, nil
	default:
		return "", fmt.Errorf("%s provider does not support responses", DisplayName(provider))
	}
}

func ResolveModelsURL(provider, providerURL, argoEnv string) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}
	return endpoints.Models, nil
}

func ResolveCountTokensURL(provider, providerURL, argoEnv, model string) (string, error) {
	endpoints, err := ResolveEndpoints(provider, providerURL, argoEnv)
	if err != nil {
		return "", err
	}

	switch constants.NormalizeProvider(provider) {
	case constants.ProviderAnthropic, constants.ProviderArgo:
		return endpoints.AnthropicCountTokens, nil
	default:
		return "", fmt.Errorf("%s provider does not support count_tokens", DisplayName(provider))
	}
}
