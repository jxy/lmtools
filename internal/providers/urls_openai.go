package providers

import (
	"fmt"
	"net/url"
	"strings"
)

func OpenAIURLs(base string) (string, string, string, error) {
	if base == "" {
		base = "https://api.openai.com/v1"
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", "", "", err
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host == "" {
		return "", "", "", fmt.Errorf("invalid base URL: http(s) URL must include a host")
	}

	endpointPath := strings.TrimRight(u.Path, "/")
	apiBase := *u
	switch {
	case strings.HasSuffix(endpointPath, "/chat/completions"):
		apiBase.Path = strings.TrimSuffix(endpointPath, "/chat/completions")
	case strings.HasSuffix(endpointPath, "/responses"):
		apiBase.Path = strings.TrimSuffix(endpointPath, "/responses")
	default:
		apiBase.Path = u.Path
	}

	chatURL, err := BuildProviderURL(apiBase.String(), "chat/completions")
	if err != nil {
		return "", "", "", err
	}
	responsesURL, err := BuildProviderURL(apiBase.String(), "responses")
	if err != nil {
		return "", "", "", err
	}
	modelsURL, err := BuildProviderURL(apiBase.String(), "models")
	if err != nil {
		return "", "", "", err
	}
	return chatURL, responsesURL, modelsURL, nil
}

func resolveOpenAIEndpoints(providerURL, _ string) (EndpointSet, error) {
	u, err := url.Parse(providerURL)
	if err != nil {
		return EndpointSet{}, err
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/embeddings") {
		base := *u
		base.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/embeddings")
		chatURL, responsesURL, modelsURL, err := OpenAIURLs(base.String())
		if err != nil {
			return EndpointSet{}, err
		}
		apiBase := endpointBaseURL(chatURL, "/chat/completions")
		return EndpointSet{
			Base:      apiBase,
			APIBase:   apiBase,
			Chat:      chatURL,
			Responses: responsesURL,
			Embed:     u.String(),
			Models:    modelsURL,
		}, nil
	}

	chatURL, responsesURL, modelsURL, err := OpenAIURLs(providerURL)
	if err != nil {
		return EndpointSet{}, err
	}
	base := endpointBaseURL(chatURL, "/chat/completions")
	embedURL, err := BuildProviderURL(base, "embeddings")
	if err != nil {
		return EndpointSet{}, err
	}

	return EndpointSet{
		Base:      base,
		APIBase:   base,
		Chat:      chatURL,
		Responses: responsesURL,
		Embed:     embedURL,
		Models:    modelsURL,
	}, nil
}
