package proxy

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"net/url"
	"strings"
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

	endpoints := &Endpoints{Provider: provider}

	// Initialize only the endpoints for the selected provider
	switch provider {
	case constants.ProviderOpenAI:
		if err := endpoints.initOpenAI(cfg); err != nil {
			return nil, err
		}
	case constants.ProviderGoogle:
		endpoints.initGoogle(cfg)
	case constants.ProviderAnthropic:
		endpoints.initAnthropic(cfg)
	case constants.ProviderArgo:
		if err := endpoints.initArgo(cfg); err != nil {
			return nil, err
		}
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

// initOpenAI initializes OpenAI endpoints.
func (e *Endpoints) initOpenAI(cfg *Config) error {
	// Use ProviderURL or default
	base := cfg.ProviderURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}

	// Check if URL already ends with chat/completions
	if strings.HasSuffix(strings.TrimRight(base, "/"), "/chat/completions") {
		e.OpenAI = strings.TrimRight(base, "/")
		modelsBase := strings.TrimSuffix(e.OpenAI, "/chat/completions")
		e.OpenAIModels = modelsBase + "/models"
		return nil
	}

	chatURL, err := buildProviderURL(base, "chat/completions")
	if err != nil {
		return fmt.Errorf("invalid OpenAI ProviderURL: %w", err)
	}
	e.OpenAI = chatURL

	modelsURL, err := buildProviderURL(base, "models")
	if err != nil {
		return fmt.Errorf("invalid OpenAI ProviderURL for models: %w", err)
	}
	e.OpenAIModels = modelsURL
	return nil
}

// initGoogle initializes Google endpoints.
func (e *Endpoints) initGoogle(cfg *Config) {
	// Use ProviderURL or default
	base := cfg.ProviderURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta/models"
	}

	e.Google = base
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1beta/models") {
		e.GoogleModels = base + "/v1beta/models"
	} else {
		e.GoogleModels = base
	}
}

// initAnthropic initializes Anthropic endpoints.
func (e *Endpoints) initAnthropic(cfg *Config) {
	// Use ProviderURL or default
	base := cfg.ProviderURL
	if base == "" {
		base = "https://api.anthropic.com/v1/messages"
	}

	e.Anthropic = base
	// Derive models URL from base
	modelsBase := strings.TrimSuffix(base, "/messages")
	modelsBase = strings.TrimRight(modelsBase, "/")
	e.AnthropicModels = modelsBase + "/models"
}

// initArgo initializes Argo endpoints using the argoEndpoints helper.
func (e *Endpoints) initArgo(cfg *Config) error {
	baseURL := getArgoBaseURL(cfg)
	eps, err := newArgoEndpoints(baseURL)
	if err != nil {
		return err
	}
	e.ArgoBase = eps.resource
	e.ArgoChat = eps.chat
	e.ArgoStreamChat = eps.stream
	e.ArgoEmbed = eps.embed
	e.ArgoModels = eps.models
	return nil
}

// getArgoBaseURL returns the base URL for Argo.
// Priority: ProviderURL > ArgoEnv > default (dev)
func getArgoBaseURL(cfg *Config) string {
	if cfg.ProviderURL != "" {
		return cfg.ProviderURL
	}
	if cfg.ArgoEnv == "prod" {
		return core.ArgoProdURL
	}
	return core.ArgoDevURL
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
