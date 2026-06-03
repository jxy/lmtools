package providers

import (
	"fmt"
	"net/url"
	"strings"
)

func AnthropicURLs(base string) (string, string, error) {
	if base == "" {
		base = "https://api.anthropic.com/v1/messages"
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", "", err
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host == "" {
		return "", "", fmt.Errorf("invalid base URL: http(s) URL must include a host")
	}

	messagesBase := *u
	if !strings.HasSuffix(strings.TrimRight(messagesBase.Path, "/"), "/messages") {
		var err error
		messagesBase.Path, err = url.JoinPath(messagesBase.Path, "messages")
		if err != nil {
			return "", "", err
		}
	} else {
		messagesBase.Path = strings.TrimRight(messagesBase.Path, "/")
	}

	modelsBase := messagesBase
	modelsBase.Path = strings.TrimSuffix(strings.TrimRight(modelsBase.Path, "/"), "/messages")
	modelsURL, err := providerURLFromParsed(modelsBase, "models")
	if err != nil {
		return "", "", err
	}
	return messagesBase.String(), modelsURL, nil
}

func resolveAnthropicEndpoints(providerURL, _ string) (EndpointSet, error) {
	messagesURL, modelsURL, err := AnthropicURLs(providerURL)
	if err != nil {
		return EndpointSet{}, err
	}
	base := endpointBaseURL(messagesURL, "/messages")
	countTokensURL, err := BuildProviderURL(base, "messages/count_tokens")
	if err != nil {
		return EndpointSet{}, err
	}
	return EndpointSet{
		Base:                 base,
		APIBase:              base,
		Chat:                 messagesURL,
		Models:               modelsURL,
		AnthropicMessages:    messagesURL,
		AnthropicCountTokens: countTokensURL,
	}, nil
}
