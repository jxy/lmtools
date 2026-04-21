package providers

import "strings"

func AnthropicURLs(base string) (string, string) {
	if base == "" {
		base = "https://api.anthropic.com/v1/messages"
	}

	messagesURL := strings.TrimRight(base, "/")
	if !strings.HasSuffix(messagesURL, "/messages") {
		messagesURL += "/messages"
	}
	modelsBase := strings.TrimSuffix(messagesURL, "/messages")
	modelsBase = strings.TrimRight(modelsBase, "/")
	return messagesURL, modelsBase + "/models"
}

func resolveAnthropicEndpoints(providerURL, _ string) (EndpointSet, error) {
	messagesURL, modelsURL := AnthropicURLs(providerURL)
	base := strings.TrimSuffix(messagesURL, "/messages")
	return EndpointSet{
		Base:                 base,
		APIBase:              base,
		Chat:                 messagesURL,
		Models:               modelsURL,
		AnthropicMessages:    messagesURL,
		AnthropicCountTokens: base + "/messages/count_tokens",
	}, nil
}
