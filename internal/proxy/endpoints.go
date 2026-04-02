package proxy

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/url"
)

// Endpoints holds all computed API endpoint URLs for a provider.
// It is constructed from Config and validates all URLs at creation time.
// This type is immutable after creation - all fields are read-only.
//
// Use NewEndpoints(cfg) to create an Endpoints instance.
type Endpoints struct {
	// Provider is the active provider for these endpoints
	Provider string

	// Chat/Completions endpoints (for request forwarding)
	OpenAI    string // OpenAI chat completions URL
	Google    string // Google Gemini base URL
	Anthropic string // Anthropic messages URL

	// Argo-specific endpoints
	ArgoBase       string // Argo base resource URL
	ArgoChat       string // Argo chat endpoint
	ArgoStreamChat string // Argo streaming chat endpoint
	ArgoEmbed      string // Argo embeddings endpoint

	// Models endpoints (for /v1/models proxy)
	OpenAIModels    string
	GoogleModels    string
	AnthropicModels string
	ArgoModels      string
}

// NewEndpoints constructs Endpoints from a Config.
// Returns an error if URL validation or derivation fails.
// This is the single point of URL initialization - call this once during server setup.
func NewEndpoints(cfg *Config) (*Endpoints, error) {
	if cfg.Provider == "" {
		return nil, fmt.Errorf("provider must be set")
	}

	// Provider is already normalized by Validate()
	provider := cfg.Provider

	// Validate ProviderURL if set
	if cfg.ProviderURL != "" {
		if err := validateProviderURL(cfg.ProviderURL, provider); err != nil {
			return nil, err
		}
	}

	resolved, err := providers.ResolveEndpoints(provider, cfg.ProviderURL, cfg.ArgoEnv)
	if err != nil {
		return nil, err
	}

	endpoints := &Endpoints{Provider: provider}
	switch provider {
	case constants.ProviderOpenAI:
		endpoints.OpenAI = resolved.Chat
		endpoints.OpenAIModels = resolved.Models
	case constants.ProviderGoogle:
		endpoints.Google = resolved.Base
		endpoints.GoogleModels = resolved.Models
	case constants.ProviderAnthropic:
		endpoints.Anthropic = resolved.Chat
		endpoints.AnthropicModels = resolved.Models
	case constants.ProviderArgo:
		endpoints.ArgoBase = resolved.Base
		endpoints.ArgoChat = resolved.Chat
		endpoints.ArgoStreamChat = resolved.Stream
		endpoints.ArgoEmbed = resolved.Embed
		endpoints.ArgoModels = resolved.Models
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}

	return endpoints, nil
}

// validateProviderURL validates a ProviderURL.
func validateProviderURL(providerURL, provider string) error {
	u, err := url.Parse(providerURL)
	if err != nil {
		return fmt.Errorf("invalid %s ProviderURL: %w", provider, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s ProviderURL must use http or https scheme", provider)
	}
	return nil
}

// GetArgoURL returns the appropriate Argo URL for the given endpoint type.
// Valid endpoints: "chat", "streamchat", "embed"
func (e *Endpoints) GetArgoURL(endpoint string) string {
	switch endpoint {
	case "chat":
		return e.ArgoChat
	case "streamchat":
		return e.ArgoStreamChat
	case "embed":
		return e.ArgoEmbed
	default:
		return e.ArgoBase
	}
}
