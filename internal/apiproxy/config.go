package apiproxy

import (
	"fmt"
	"strings"
)

// Config holds the configuration for the API proxy
type Config struct {
	// API Keys
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string

	// Argo Configuration
	ArgoUser string
	ArgoEnv  string

	// Provider Configuration
	PreferredProvider string
	ProviderURL       string
	BigModel          string
	SmallModel        string

	// Model Lists
	OpenAIModels []string
	GeminiModels []string
	ArgoModels   []string

	// Security Configuration
	MaxRequestBodySize int64 // Maximum request body size in bytes

	// Streaming Configuration

	// API Endpoints
	OpenAIURL    string // OpenAI API endpoint
	GeminiURL    string // Google Gemini API endpoint
	AnthropicURL string // Anthropic API endpoint
	ArgoBaseURL  string // Argo API base URL (environment-specific)
}

// ApplyDynamicModelDefaults applies provider-specific model defaults
// when the user hasn't changed the default model values
func (c *Config) ApplyDynamicModelDefaults() {
	// If user hasn't changed the defaults, set provider-specific defaults
	if c.BigModel == "claudeopus4" && c.SmallModel == "claudesonnet4" {
		switch c.PreferredProvider {
		case "openai":
			c.BigModel = "o3-mini"
			c.SmallModel = "gpt-4o-mini"
		case "google":
			c.BigModel = "gemini-2.5-pro-preview-03-25"
			c.SmallModel = "gemini-2.0-flash"
			// case "argo": keep the current defaults
		}
	}
}

// InitializeModelLists initializes the model lists for each provider
func (c *Config) InitializeModelLists() {
	// Normalize provider name
	c.PreferredProvider = strings.ToLower(c.PreferredProvider)

	// Initialize API URLs with defaults
	if c.OpenAIURL == "" {
		c.OpenAIURL = "https://api.openai.com/v1/chat/completions"
	}
	if c.GeminiURL == "" {
		c.GeminiURL = "https://generativelanguage.googleapis.com/v1beta/models"
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
		switch c.PreferredProvider {
		case "openai":
			c.OpenAIURL = c.ProviderURL
		case "google":
			c.GeminiURL = c.ProviderURL
		case "argo":
			c.ArgoBaseURL = c.ProviderURL
		}
	}

	// Define supported models
	// MODEL LIST MAINTENANCE:
	// - Update these lists when new models are released by providers
	// - Official model names can be found at:
	//   OpenAI: https://platform.openai.com/docs/models
	//   Gemini: https://ai.google.dev/gemini-api/docs/models
	//   Argo: Internal documentation
	// - Test new models with the proxy before adding to ensure compatibility
	// - Keep deprecated models for backward compatibility unless officially removed
	// Last updated: 2025-07-31
	c.OpenAIModels = []string{
		"o3-mini",
		"o1",
		"o1-mini",
		"o1-pro",
		"gpt-4.5-preview",
		"gpt-4o",
		"gpt-4o-audio-preview",
		"chatgpt-4o-latest",
		"gpt-4o-mini",
		"gpt-4o-mini-audio-preview",
		"gpt-4.1",
		"gpt-4.1-mini",
	}
	c.GeminiModels = []string{
		"gemini-2.5-pro-preview-03-25",
		"gemini-2.0-flash",
	}
	c.ArgoModels = []string{
		"gpt35",
		"gpt35large",
		"gpt4",
		"gpt4large",
		"gpt4turbo",
		"gpt4o",
		"gpt4olatest",
		"gpto1mini",
		"gpto3mini",
		"gpto1",
		"gpto3",
		"gpto4mini",
		"gpt41",
		"gpt41mini",
		"gpt41nano",
		"gemini25pro",
		"gemini25flash",
		"claudeopus4",
		"claudesonnet4",
		"claudesonnet37",
		"claudesonnet35v2",
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Validate preferred provider
	validProviders := []string{"openai", "google", "argo"}
	valid := false
	for _, p := range validProviders {
		if c.PreferredProvider == p {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid --preferred-provider: %s, must be one of: %s",
			c.PreferredProvider, strings.Join(validProviders, ", "))
	}

	// Check if required API keys are present based on configuration
	if c.PreferredProvider == "openai" && c.OpenAIAPIKey == "" {
		return fmt.Errorf("--openai-api-key-file is required when --preferred-provider is 'openai'")
	}
	if c.PreferredProvider == "google" && c.GeminiAPIKey == "" {
		return fmt.Errorf("--gemini-api-key-file is required when --preferred-provider is 'google'")
	}
	if c.PreferredProvider == "argo" && c.ArgoUser == "" {
		return fmt.Errorf("--argo-user is required when --preferred-provider is 'argo'")
	}

	// Check if models exist in their respective lists
	if c.isOpenAIModel(c.BigModel) && c.OpenAIAPIKey == "" {
		return fmt.Errorf("--openai-api-key-file is required when --big-model is an OpenAI model")
	}
	if c.isGeminiModel(c.BigModel) && c.GeminiAPIKey == "" {
		return fmt.Errorf("--gemini-api-key-file is required when --big-model is a Gemini model")
	}
	if c.isArgoModel(c.BigModel) && c.ArgoUser == "" {
		return fmt.Errorf("--argo-user is required when --big-model is an Argo model")
	}

	if c.isOpenAIModel(c.SmallModel) && c.OpenAIAPIKey == "" {
		return fmt.Errorf("--openai-api-key-file is required when --small-model is an OpenAI model")
	}
	if c.isGeminiModel(c.SmallModel) && c.GeminiAPIKey == "" {
		return fmt.Errorf("--gemini-api-key-file is required when --small-model is a Gemini model")
	}
	if c.isArgoModel(c.SmallModel) && c.ArgoUser == "" {
		return fmt.Errorf("--argo-user is required when --small-model is an Argo model")
	}

	return nil
}

// isOpenAIModel checks if a model is in the OpenAI models list
func (c *Config) isOpenAIModel(model string) bool {
	for _, m := range c.OpenAIModels {
		if m == model {
			return true
		}
	}
	return false
}

// isGeminiModel checks if a model is in the Gemini models list
func (c *Config) isGeminiModel(model string) bool {
	for _, m := range c.GeminiModels {
		if m == model {
			return true
		}
	}
	return false
}

// isArgoModel checks if a model is in the Argo models list
func (c *Config) isArgoModel(model string) bool {
	for _, m := range c.ArgoModels {
		if m == model {
			return true
		}
	}
	return false
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
