package proxy

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strings"
	"time"
)

// Config holds the configuration for the API proxy
type Config struct {
	// API Keys
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GoogleAPIKey    string

	// Argo Configuration
	ArgoUser string
	ArgoEnv  string

	// Provider Configuration
	Provider    string
	ProviderURL string
	Model       string
	SmallModel  string

	// Security Configuration
	MaxRequestBodySize  int64 // Maximum request body size in bytes
	MaxResponseBodySize int64 // Maximum response body size in bytes

	// Streaming Configuration
	PingInterval time.Duration // Ping interval (0 = use default of 15 seconds)

	// API Endpoints
	OpenAIURL    string // OpenAI API endpoint
	GoogleURL    string // Google API endpoint
	AnthropicURL string // Anthropic API endpoint
	ArgoBaseURL  string // Argo API base URL (environment-specific)
}

// ApplyDynamicModelDefaults applies provider-specific model defaults
// when the user hasn't specified models
func (c *Config) ApplyDynamicModelDefaults() {
	// Use provider name as-is
	provider := strings.ToLower(c.Provider)

	// If Model not specified, use provider-specific default
	if c.Model == "" {
		c.Model = core.GetDefaultChatModel(provider)
	}

	// If SmallModel not specified, use provider-specific default
	if c.SmallModel == "" {
		c.SmallModel = core.GetDefaultSmallModel(provider)
	}
}

// InitializeURLs initializes the API URLs for each provider
func (c *Config) InitializeURLs() {
	// Normalize provider name
	c.Provider = strings.ToLower(c.Provider)

	// Initialize API URLs with defaults
	if c.OpenAIURL == "" {
		c.OpenAIURL = "https://api.openai.com/v1/chat/completions"
	}
	if c.GoogleURL == "" {
		c.GoogleURL = "https://generativelanguage.googleapis.com/v1beta/models"
	}
	if c.AnthropicURL == "" {
		c.AnthropicURL = "https://api.anthropic.com/v1/messages"
	}
	if c.ArgoBaseURL == "" {
		// Set default based on environment
		if c.ArgoEnv == "prod" {
			c.ArgoBaseURL = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
		} else {
			c.ArgoBaseURL = "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource"
		}
	}

	// Apply custom provider URL if specified
	if c.ProviderURL != "" {
		switch c.Provider {
		case constants.ProviderOpenAI:
			// Always treat ProviderURL as a base URL and append the endpoint
			c.OpenAIURL = strings.TrimRight(c.ProviderURL, "/") + "/chat/completions"
		case constants.ProviderGoogle:
			c.GoogleURL = c.ProviderURL
		case constants.ProviderAnthropic:
			c.AnthropicURL = c.ProviderURL
		case constants.ProviderArgo:
			c.ArgoBaseURL = c.ProviderURL
		}
	}

	// No longer maintaining hardcoded model lists
	// Models are queried dynamically from provider APIs
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Validate preferred provider
	validProviders := []string{constants.ProviderOpenAI, constants.ProviderGoogle, constants.ProviderAnthropic, constants.ProviderArgo}
	valid := false
	for _, p := range validProviders {
		if c.Provider == p {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid -provider: %s, must be one of: %s",
			c.Provider, strings.Join(validProviders, ", "))
	}

	// Check if required credentials are present based on the selected provider
	// With the unified -api-key-file flag, we only need the key for the selected provider
	switch c.Provider {
	case constants.ProviderOpenAI:
		if c.OpenAIAPIKey == "" && c.ProviderURL == "" {
			return fmt.Errorf("-api-key-file is required when -provider is 'openai' (unless using -provider-url)")
		}
	case constants.ProviderGoogle:
		if c.GoogleAPIKey == "" && c.ProviderURL == "" {
			return fmt.Errorf("-api-key-file is required when -provider is 'google' (unless using -provider-url)")
		}
	case constants.ProviderAnthropic:
		if c.AnthropicAPIKey == "" && c.ProviderURL == "" {
			return fmt.Errorf("-api-key-file is required when -provider is 'anthropic' (unless using -provider-url)")
		}
	case constants.ProviderArgo:
		if c.ArgoUser == "" {
			return fmt.Errorf("-argo-user is required when -provider is 'argo'")
		}
	}

	return nil
}

// GetArgoURL returns the full Argo URL for the given endpoint
func (c *Config) GetArgoURL(endpoint string) string {
	switch endpoint {
	case "chat":
		return c.ArgoBaseURL + "/chat/"
	case "streamchat":
		return c.ArgoBaseURL + "/streamchat/"
	case "embed":
		return c.ArgoBaseURL + "/embed/"
	default:
		return c.ArgoBaseURL
	}
}
