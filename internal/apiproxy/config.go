package apiproxy

import (
	"fmt"
	"os"
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
	BigModel          string
	SmallModel        string

	// Model Lists
	OpenAIModels []string
	GeminiModels []string
	ArgoModels   []string

	// Security Configuration
	MaxRequestBodySize int64 // Maximum request body size in bytes
}

// LoadConfig loads configuration from environment variables
func LoadConfig() (*Config, error) {
	config := &Config{
		// Load API keys
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),

		// Load Argo configuration
		ArgoUser: os.Getenv("ARGO_USER"),
		ArgoEnv:  getEnvOrDefault("ARGO_ENV", "dev"),

		// Load provider configuration
		PreferredProvider: strings.ToLower(getEnvOrDefault("PREFERRED_PROVIDER", "openai")),
		BigModel:          getEnvOrDefault("BIG_MODEL", "gpt-4.1"),
		SmallModel:        getEnvOrDefault("SMALL_MODEL", "gpt-4.1-mini"),

		// Load security configuration
		MaxRequestBodySize: getMaxRequestBodySize(),

		// Define supported models
		OpenAIModels: []string{
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
		},
		GeminiModels: []string{
			"gemini-2.5-pro-preview-03-25",
			"gemini-2.0-flash",
		},
		ArgoModels: []string{
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
		},
	}

	// Validate configuration
	if err := config.validate(); err != nil {
		return nil, err
	}

	return config, nil
}

// validate checks if the configuration is valid
func (c *Config) validate() error {
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
		return fmt.Errorf("invalid PREFERRED_PROVIDER: %s, must be one of: %s",
			c.PreferredProvider, strings.Join(validProviders, ", "))
	}

	// Check if required API keys are present based on configuration
	if c.PreferredProvider == "openai" && c.OpenAIAPIKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is required when PREFERRED_PROVIDER is 'openai'")
	}
	if c.PreferredProvider == "google" && c.GeminiAPIKey == "" {
		return fmt.Errorf("GEMINI_API_KEY is required when PREFERRED_PROVIDER is 'google'")
	}
	if c.PreferredProvider == "argo" && c.ArgoUser == "" {
		return fmt.Errorf("ARGO_USER is required when PREFERRED_PROVIDER is 'argo'")
	}

	// Check if models exist in their respective lists
	if c.isOpenAIModel(c.BigModel) && c.OpenAIAPIKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is required when BIG_MODEL is an OpenAI model")
	}
	if c.isGeminiModel(c.BigModel) && c.GeminiAPIKey == "" {
		return fmt.Errorf("GEMINI_API_KEY is required when BIG_MODEL is a Gemini model")
	}
	if c.isArgoModel(c.BigModel) && c.ArgoUser == "" {
		return fmt.Errorf("ARGO_USER is required when BIG_MODEL is an Argo model")
	}

	if c.isOpenAIModel(c.SmallModel) && c.OpenAIAPIKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is required when SMALL_MODEL is an OpenAI model")
	}
	if c.isGeminiModel(c.SmallModel) && c.GeminiAPIKey == "" {
		return fmt.Errorf("GEMINI_API_KEY is required when SMALL_MODEL is a Gemini model")
	}
	if c.isArgoModel(c.SmallModel) && c.ArgoUser == "" {
		return fmt.Errorf("ARGO_USER is required when SMALL_MODEL is an Argo model")
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

// getEnvOrDefault returns the environment variable value or a default
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getMaxRequestBodySize returns the max request body size from env or default (10MB)
func getMaxRequestBodySize() int64 {
	if value := os.Getenv("MAX_REQUEST_BODY_SIZE"); value != "" {
		// Parse as MB and convert to bytes
		var mb int64
		if _, err := fmt.Sscanf(value, "%d", &mb); err == nil && mb > 0 {
			return mb * 1024 * 1024
		}
	}
	// Default to 10MB
	return 10 * 1024 * 1024
}
