package providers

import (
	"fmt"
	"net/url"
	"strings"
)

func BuildGoogleModelURL(baseURL, model, action string) (string, error) {
	modelPath := fmt.Sprintf("%s:%s", model, action)
	built, err := BuildProviderURL(baseURL, modelPath)
	if err != nil {
		return "", err
	}
	if action != "streamGenerateContent" {
		return built, nil
	}

	u, err := url.Parse(built)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("alt", "sse")
	u.RawQuery = query.Encode()
	return u.String(), nil
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
