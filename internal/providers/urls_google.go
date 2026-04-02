package providers

import (
	"fmt"
	"strings"
)

func BuildGoogleModelURL(baseURL, model, action string) (string, error) {
	modelPath := fmt.Sprintf("%s:%s", model, action)
	return BuildProviderURL(baseURL, modelPath)
}

func GoogleURLs(base string) (string, string) {
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta/models"
	}

	trimmed := strings.TrimRight(base, "/")
	if strings.HasSuffix(trimmed, "/v1beta/models") {
		return trimmed, trimmed
	}
	if strings.HasSuffix(trimmed, "/v1beta") {
		trimmed += "/models"
		return trimmed, trimmed
	}

	trimmed += "/v1beta/models"
	return trimmed, trimmed
}

func resolveGoogleEndpoints(providerURL, _ string) (EndpointSet, error) {
	base, models := GoogleURLs(providerURL)
	return EndpointSet{
		Base:    base,
		APIBase: base,
		Models:  models,
	}, nil
}
