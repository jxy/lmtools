package proxy

import (
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/providerconfig"
	"time"
)

type ProviderKeySet = auth.ProviderKeySet

// Config holds the configuration for the API proxy
type Config struct {
	ProviderKeySet auth.ProviderKeySet

	// Argo Configuration
	ArgoUser   string
	ArgoDev    bool
	ArgoTest   bool
	ArgoLegacy bool
	ArgoEnv    string

	// Provider Configuration
	Provider      string
	ProviderURL   string
	ModelMapRules []ModelMapRule

	// OpenAIResponses forwards /v1/responses to the provider's own Responses API
	// instead of converting it. Valid for openai and native argo only.
	OpenAIResponses bool

	// StripEncryptedReasoning removes opaque encrypted reasoning state from
	// direct Responses requests as an opt-in recovery measure.
	StripEncryptedReasoning bool

	// Security Configuration
	MaxRequestBodySize  int64 // Maximum request body size in bytes
	MaxResponseBodySize int64 // Maximum response body size in bytes

	// Streaming Configuration
	PingInterval time.Duration // Ping interval (0 = use default of 15 seconds)

	// Stateful Responses API storage. Empty means ~/.apiproxy/sessions.
	SessionsDir string
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if err := providerconfig.ValidateArgoEnvironmentFlags(c.ArgoDev, c.ArgoTest); err != nil {
		return err
	}

	if c.ArgoEnv == "" {
		c.ArgoEnv = providerconfig.ResolveArgoEnvironment(c.ArgoDev, c.ArgoTest)
	}

	c.Provider = constants.NormalizeProvider(c.Provider)
	if !constants.IsValidProvider(c.Provider) {
		return fmt.Errorf("invalid -provider: %s, must be one of: %s",
			c.Provider, constants.JoinedProviders())
	}

	if err := providerconfig.ValidateResponsesAPIFlag(c.Provider, c.OpenAIResponses, c.ArgoLegacy); err != nil {
		return err
	}

	if ok, _ := evaluateProviderCredentials(c.Provider, newProviderCredentialState(c)); !ok {
		return fmt.Errorf("%s", providerValidationError(c.Provider))
	}

	if err := ValidateModelMapRules(c.ModelMapRules); err != nil {
		return err
	}

	return nil
}
