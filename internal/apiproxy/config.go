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
	BigModel          string
	SmallModel        string

	// Model Lists
	OpenAIModels []string
	GeminiModels []string
	ArgoModels   []string

	// Security Configuration
	MaxRequestBodySize int64 // Maximum request body size in bytes
}

// InitializeModelLists initializes the model lists for each provider
func (c *Config) InitializeModelLists() {
	// Normalize provider name
	c.PreferredProvider = strings.ToLower(c.PreferredProvider)

	// Define supported models
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
