package providers

import (
	"lmtools/internal/constants"
	"sort"
)

const (
	ArgoProdURL = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
	ArgoDevURL  = "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource"
	ArgoTestURL = "https://apps-test.inside.anl.gov/argoapi/api/v1/resource"

	DefaultArgoChatModel      = "gpt5"
	DefaultOpenAIChatModel    = "gpt-5"
	DefaultAnthropicChatModel = "claude-opus-4-1-20250805"
	DefaultGoogleChatModel    = "gemini-2.5-pro"

	DefaultArgoSmallModel      = "gpt5mini"
	DefaultOpenAISmallModel    = "gpt-5-mini"
	DefaultAnthropicSmallModel = "claude-3-haiku-20240307"
	DefaultGoogleSmallModel    = "gemini-2.5-flash"
)

type CredentialKind string

const (
	CredentialKindNone     CredentialKind = ""
	CredentialKindAPIKey   CredentialKind = "api_key"
	CredentialKindArgoUser CredentialKind = "argo_user"
)

type Info struct {
	ID                        string
	DisplayName               string
	DefaultChatModel          string
	DefaultSmallModel         string
	UsesOutOfBandSystemPrompt bool
	SupportsEmbeddings        bool
	SupportsModelsEndpoint    bool
	CredentialKind            CredentialKind
}

func InfoFor(provider string) (Info, bool) {
	descriptor, ok := descriptorFor(provider)
	if !ok {
		return Info{}, false
	}
	return descriptor.Info, true
}

func ProviderIDs() []string {
	ids := make([]string, 0, len(providerDescriptors))
	for id := range providerDescriptors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func DefaultProvider() string {
	return constants.ProviderArgo
}

func ResolveProviderWithFallback(provider, fallback string) string {
	if normalized := constants.NormalizeProvider(provider); normalized != "" {
		return normalized
	}
	if normalizedFallback := constants.NormalizeProvider(fallback); normalizedFallback != "" {
		return normalizedFallback
	}
	return DefaultProvider()
}

func ResolveProvider(provider string) string {
	return ResolveProviderWithFallback(provider, DefaultProvider())
}

func DisplayName(provider string) string {
	if info, ok := InfoFor(provider); ok {
		return info.DisplayName
	}
	if normalized := constants.NormalizeProvider(provider); normalized != "" {
		return normalized
	}
	return "unknown"
}

func DefaultChatModel(provider string) string {
	if info, ok := InfoFor(provider); ok {
		return info.DefaultChatModel
	}
	return ""
}

func DefaultSmallModel(provider string) string {
	if info, ok := InfoFor(provider); ok {
		return info.DefaultSmallModel
	}
	return ""
}

func RequiresAPIKey(provider string) bool {
	return CredentialKindFor(provider) == CredentialKindAPIKey
}

func UsesOutOfBandSystemPrompt(provider string) bool {
	if info, ok := InfoFor(provider); ok {
		return info.UsesOutOfBandSystemPrompt
	}
	return false
}

func SupportsEmbeddings(provider string) bool {
	if info, ok := InfoFor(provider); ok {
		return info.SupportsEmbeddings
	}
	return false
}

func SupportsModelsEndpoint(provider string) bool {
	if info, ok := InfoFor(provider); ok {
		return info.SupportsModelsEndpoint
	}
	return false
}

func CredentialKindFor(provider string) CredentialKind {
	if info, ok := InfoFor(provider); ok {
		return info.CredentialKind
	}
	return CredentialKindNone
}
