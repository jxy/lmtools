package providers

import "strings"

func OpenAIURLs(base string) (string, string, string, error) {
	if base == "" {
		base = "https://api.openai.com/v1"
	}

	trimmed := strings.TrimRight(base, "/")
	if strings.HasSuffix(trimmed, "/chat/completions") {
		base = strings.TrimSuffix(trimmed, "/chat/completions")
		return trimmed, base + "/responses", base + "/models", nil
	}
	if strings.HasSuffix(trimmed, "/responses") {
		base = strings.TrimSuffix(trimmed, "/responses")
		return base + "/chat/completions", trimmed, base + "/models", nil
	}

	chatURL, err := BuildProviderURL(base, "chat/completions")
	if err != nil {
		return "", "", "", err
	}
	responsesURL, err := BuildProviderURL(base, "responses")
	if err != nil {
		return "", "", "", err
	}
	modelsURL, err := BuildProviderURL(base, "models")
	if err != nil {
		return "", "", "", err
	}
	return chatURL, responsesURL, modelsURL, nil
}

func resolveOpenAIEndpoints(providerURL, _ string) (EndpointSet, error) {
	trimmed := strings.TrimRight(providerURL, "/")
	if strings.HasSuffix(trimmed, "/embeddings") {
		base := strings.TrimSuffix(trimmed, "/embeddings")
		chatURL, responsesURL, modelsURL, err := OpenAIURLs(base)
		if err != nil {
			return EndpointSet{}, err
		}
		return EndpointSet{
			Base:      strings.TrimSuffix(chatURL, "/chat/completions"),
			APIBase:   strings.TrimSuffix(chatURL, "/chat/completions"),
			Chat:      chatURL,
			Responses: responsesURL,
			Embed:     trimmed,
			Models:    modelsURL,
		}, nil
	}

	chatURL, responsesURL, modelsURL, err := OpenAIURLs(providerURL)
	if err != nil {
		return EndpointSet{}, err
	}
	base := strings.TrimSuffix(chatURL, "/chat/completions")
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
