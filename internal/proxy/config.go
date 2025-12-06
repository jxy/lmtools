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
}

// ApplyDynamicModelDefaults applies provider-specific model defaults
// when the user hasn't specified models
func (c *Config) ApplyDynamicModelDefaults() {
	// Provider is already normalized by Validate()
	provider := c.Provider

	// If Model not specified, use provider-specific default
	if c.Model == "" {
		c.Model = core.GetDefaultChatModel(provider)
	}

	// If SmallModel not specified, use provider-specific default
	if c.SmallModel == "" {
		c.SmallModel = core.GetDefaultSmallModel(provider)
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Normalize provider name once at validation
	c.Provider = strings.ToLower(c.Provider)

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
