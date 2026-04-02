package providers

import (
	"fmt"
	"net/url"
	"strings"
)

const apiV1Path = "/api/v1"

type ArgoEndpoints struct {
	BaseAPI  string
	Resource string
	Chat     string
	Stream   string
	Embed    string
	Models   string
}

func isCustomArgoURL(argoEnv string) bool {
	return strings.HasPrefix(argoEnv, "http://") || strings.HasPrefix(argoEnv, "https://")
}

func ensureURLTrailingSlash(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

func buildDirectArgoURL(baseURL, path string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid Argo base URL %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q, must be http or https", u.Scheme)
	}

	resolved, err := BuildProviderURL(baseURL, path)
	if err != nil {
		return "", err
	}
	return ensureURLTrailingSlash(resolved)
}

func buildDirectArgoEndpoints(baseURL string) (EndpointSet, error) {
	chat, err := buildDirectArgoURL(baseURL, "chat")
	if err != nil {
		return EndpointSet{}, err
	}
	stream, err := buildDirectArgoURL(baseURL, "streamchat")
	if err != nil {
		return EndpointSet{}, err
	}
	embed, err := buildDirectArgoURL(baseURL, "embed")
	if err != nil {
		return EndpointSet{}, err
	}
	models, err := buildDirectArgoURL(baseURL, "models")
	if err != nil {
		return EndpointSet{}, err
	}

	trimmed := strings.TrimRight(baseURL, "/")
	return EndpointSet{
		Base:    trimmed,
		APIBase: trimmed,
		Chat:    chat,
		Stream:  stream,
		Embed:   embed,
		Models:  models,
	}, nil
}

func BuildArgoEndpoints(base string) (ArgoEndpoints, error) {
	u, err := url.Parse(base)
	if err != nil {
		return ArgoEndpoints{}, fmt.Errorf("invalid Argo base URL %q: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ArgoEndpoints{}, fmt.Errorf("unsupported scheme %q, must be http or https", u.Scheme)
	}

	path := strings.TrimRight(u.Path, "/")
	if idx := strings.Index(path, apiV1Path); idx != -1 {
		path = path[:idx+len(apiV1Path)]
	} else {
		path = path + apiV1Path
	}

	u.Path = path
	baseAPI := u.String()
	resource := strings.TrimRight(baseAPI, "/") + "/resource"

	return ArgoEndpoints{
		BaseAPI:  baseAPI,
		Resource: resource,
		Chat:     resource + "/chat/",
		Stream:   resource + "/streamchat/",
		Embed:    resource + "/embed/",
		Models:   baseAPI + "/models/",
	}, nil
}

func buildNormalizedArgoEndpoints(baseURL string) (EndpointSet, error) {
	eps, err := BuildArgoEndpoints(baseURL)
	if err != nil {
		return EndpointSet{}, err
	}
	return EndpointSet{
		Base:    eps.Resource,
		APIBase: eps.BaseAPI,
		Chat:    eps.Chat,
		Stream:  eps.Stream,
		Embed:   eps.Embed,
		Models:  eps.Models,
	}, nil
}

func ResolveArgoBaseURL(argoEnv string) string {
	if isCustomArgoURL(argoEnv) {
		return argoEnv
	}
	if argoEnv == "prod" {
		return ArgoProdURL
	}
	return ArgoDevURL
}

func resolveArgoEndpoints(providerURL, argoEnv string) (EndpointSet, error) {
	switch {
	case providerURL != "":
		return buildNormalizedArgoEndpoints(providerURL)
	case isCustomArgoURL(argoEnv):
		return buildDirectArgoEndpoints(argoEnv)
	default:
		return buildNormalizedArgoEndpoints(ResolveArgoBaseURL(argoEnv))
	}
}
