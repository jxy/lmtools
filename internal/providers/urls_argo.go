package providers

import (
	"fmt"
	"net/url"
	"strings"
)

type ArgoEndpoints struct {
	Root                 string
	BaseAPI              string
	Resource             string
	Chat                 string
	Stream               string
	Embed                string
	Models               string
	OpenAIChat           string
	AnthropicMessages    string
	AnthropicCountTokens string
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

func buildArgoURL(root, suffix string, trailingSlash bool) (string, error) {
	resolved, err := BuildProviderURL(root, suffix)
	if err != nil {
		return "", err
	}
	if trailingSlash {
		return ensureURLTrailingSlash(resolved)
	}
	return resolved, nil
}

func argoRoot(rawBase string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawBase))
	if err != nil {
		return "", fmt.Errorf("invalid Argo base URL %q: %w", rawBase, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q, must be http or https", u.Scheme)
	}

	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.Count(path, "/api/v1") > 1:
		path = path[:strings.Index(path, "/api/v1")]
	case strings.Count(path, "/v1") > 1 && !strings.Contains(path, "/api/v1"):
		path = path[:strings.Index(path, "/v1")]
	}
	switch {
	case strings.HasSuffix(path, "/api/v1/chat/completions"):
		path = strings.TrimSuffix(path, "/api/v1/chat/completions")
	case strings.HasSuffix(path, "/api/v1/messages/count_tokens"):
		path = strings.TrimSuffix(path, "/api/v1/messages/count_tokens")
	case strings.HasSuffix(path, "/api/v1/messages"):
		path = strings.TrimSuffix(path, "/api/v1/messages")
	case strings.HasSuffix(path, "/api/v1/resource"):
		path = strings.TrimSuffix(path, "/api/v1/resource")
	case strings.HasSuffix(path, "/api/v1"):
		path = strings.TrimSuffix(path, "/api/v1")
	case strings.HasSuffix(path, "/v1/chat/completions"):
		path = strings.TrimSuffix(path, "/v1/chat/completions")
	case strings.HasSuffix(path, "/v1/messages/count_tokens"):
		path = strings.TrimSuffix(path, "/v1/messages/count_tokens")
	case strings.HasSuffix(path, "/v1/messages"):
		path = strings.TrimSuffix(path, "/v1/messages")
	case strings.Contains(path, "/api/v1"):
		path = path[:strings.Index(path, "/api/v1")]
	case strings.Contains(path, "/v1"):
		path = path[:strings.Index(path, "/v1")]
	}

	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func BuildArgoEndpoints(base string) (ArgoEndpoints, error) {
	root, err := argoRoot(base)
	if err != nil {
		return ArgoEndpoints{}, err
	}

	baseAPI, err := buildArgoURL(root, "api/v1", false)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	resource, err := buildArgoURL(root, "api/v1/resource", false)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	chat, err := buildArgoURL(root, "api/v1/resource/chat", true)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	stream, err := buildArgoURL(root, "api/v1/resource/streamchat", true)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	embed, err := buildArgoURL(root, "api/v1/resource/embed", true)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	models, err := buildArgoURL(root, "api/v1/models", true)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	openAIChat, err := buildArgoURL(root, "v1/chat/completions", false)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	anthropicMessages, err := buildArgoURL(root, "v1/messages", false)
	if err != nil {
		return ArgoEndpoints{}, err
	}
	anthropicCountTokens, err := buildArgoURL(root, "v1/messages/count_tokens", false)
	if err != nil {
		return ArgoEndpoints{}, err
	}

	return ArgoEndpoints{
		Root:                 root,
		BaseAPI:              baseAPI,
		Resource:             resource,
		Chat:                 chat,
		Stream:               stream,
		Embed:                embed,
		Models:               models,
		OpenAIChat:           openAIChat,
		AnthropicMessages:    anthropicMessages,
		AnthropicCountTokens: anthropicCountTokens,
	}, nil
}

func buildNormalizedArgoEndpoints(baseURL string) (EndpointSet, error) {
	eps, err := BuildArgoEndpoints(baseURL)
	if err != nil {
		return EndpointSet{}, err
	}
	return EndpointSet{
		Base:                 eps.Resource,
		APIBase:              eps.BaseAPI,
		Chat:                 eps.Chat,
		Stream:               eps.Stream,
		Embed:                eps.Embed,
		Models:               eps.Models,
		OpenAIChat:           eps.OpenAIChat,
		AnthropicMessages:    eps.AnthropicMessages,
		AnthropicCountTokens: eps.AnthropicCountTokens,
	}, nil
}

func ResolveArgoBaseURL(argoEnv string) string {
	if isCustomArgoURL(argoEnv) {
		return argoEnv
	}
	if argoEnv == "dev" {
		return ArgoDevURL
	}
	if argoEnv == "test" {
		return ArgoTestURL
	}
	return ArgoProdURL
}

func resolveArgoEndpoints(providerURL, argoEnv string) (EndpointSet, error) {
	switch {
	case providerURL != "":
		return buildNormalizedArgoEndpoints(providerURL)
	default:
		return buildNormalizedArgoEndpoints(ResolveArgoBaseURL(argoEnv))
	}
}
