package proxy

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"time"
)

// Config holds the configuration for the API proxy
type Config struct {
	// API Keys
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GoogleAPIKey    string
	ArgoAPIKey      string

	// Argo Configuration
	ArgoUser   string
	ArgoDev    bool
	ArgoTest   bool
	ArgoLegacy bool
	ArgoEnv    string

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
	if c.ArgoDev && c.ArgoTest {
		return fmt.Errorf("invalid flag combination: -argo-dev and -argo-test cannot be used together")
	}

	if c.ArgoEnv == "" {
		if c.ArgoDev {
			c.ArgoEnv = "dev"
		} else if c.ArgoTest {
			c.ArgoEnv = "test"
		} else {
			c.ArgoEnv = "prod"
		}
	}

	c.Provider = constants.NormalizeProvider(c.Provider)
	if !constants.IsValidProvider(c.Provider) {
		return fmt.Errorf("invalid -provider: %s, must be one of: %s",
			c.Provider, constants.JoinedProviders())
	}

	if ok, _ := evaluateProviderCredentials(c.Provider, newProviderCredentialState(c)); !ok {
		return fmt.Errorf("%s", providerValidationError(c.Provider))
	}

	return nil
}
