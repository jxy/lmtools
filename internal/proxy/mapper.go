package proxy

import (
	"strings"
)

// ModelMapper handles model name mapping between different providers
type ModelMapper struct {
	config *Config
}

// NewModelMapper creates a new model mapper
func NewModelMapper(config *Config) *ModelMapper {
	return &ModelMapper{
		config: config,
	}
}

// MapModel maps an incoming model name to the appropriate provider and model
func (m *ModelMapper) MapModel(model string) (provider, mappedModel string) {
	// Remove any existing provider prefix
	cleanModel := m.cleanModelName(model)

	// For Anthropic model names (claude-*), use configurable mapping
	if strings.HasPrefix(strings.ToLower(cleanModel), "claude-") {
		if strings.Contains(strings.ToLower(cleanModel), "haiku") {
			return m.mapToSmallModel()
		} else {
			// All non-haiku Claude models map to Model
			return m.mapToModel()
		}
	}

	// For all other models, use the provider flag to determine routing
	// The provider flag takes precedence over model name patterns
	return m.mapToProvider(cleanModel)
}

// cleanModelName removes provider prefixes from model names
func (m *ModelMapper) cleanModelName(model string) string {
	prefixes := []string{"anthropic/", "openai/", "google/", "argo/"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(model, prefix) {
			return strings.TrimPrefix(model, prefix)
		}
	}
	return model
}

// mapToSmallModel maps to the configured small model
func (m *ModelMapper) mapToSmallModel() (provider, model string) {
	smallModel := m.config.SmallModel

	// Use preferred provider if credentials are available
	switch m.config.Provider {
	case "anthropic":
		if m.config.AnthropicAPIKey != "" || m.config.ProviderURL != "" {
			return "anthropic", smallModel
		}
	case "google":
		if m.config.GoogleAPIKey != "" || m.config.ProviderURL != "" {
			return "google", smallModel
		}
	case "argo":
		if m.config.ArgoUser != "" {
			return "argo", smallModel
		}
	default: // "openai" or any other value defaults to OpenAI
		if m.config.OpenAIAPIKey != "" || m.config.ProviderURL != "" {
			return "openai", smallModel
		}
	}

	// Fallback to any available provider
	if m.config.AnthropicAPIKey != "" {
		return "anthropic", smallModel
	}
	if m.config.OpenAIAPIKey != "" {
		return "openai", smallModel
	}
	if m.config.GoogleAPIKey != "" {
		return "google", smallModel
	}
	if m.config.ArgoUser != "" {
		return "argo", smallModel
	}

	// No credentials available
	return "", ""
}

// mapToModel maps to the configured model
func (m *ModelMapper) mapToModel() (provider, model string) {
	model = m.config.Model

	// Use preferred provider if credentials are available
	switch m.config.Provider {
	case "anthropic":
		if m.config.AnthropicAPIKey != "" || m.config.ProviderURL != "" {
			return "anthropic", model
		}
	case "google":
		if m.config.GoogleAPIKey != "" || m.config.ProviderURL != "" {
			return "google", model
		}
	case "argo":
		if m.config.ArgoUser != "" {
			return "argo", model
		}
	default: // "openai" or any other value defaults to OpenAI
		if m.config.OpenAIAPIKey != "" || m.config.ProviderURL != "" {
			return "openai", model
		}
	}

	// Fallback to any available provider
	if m.config.AnthropicAPIKey != "" {
		return "anthropic", model
	}
	if m.config.OpenAIAPIKey != "" {
		return "openai", model
	}
	if m.config.GoogleAPIKey != "" {
		return "google", model
	}
	if m.config.ArgoUser != "" {
		return "argo", model
	}

	// No credentials available
	return "", ""
}

// mapToProvider maps a model to the configured provider
func (m *ModelMapper) mapToProvider(model string) (provider, mappedModel string) {
	// Use the configured provider if credentials are available
	switch m.config.Provider {
	case "anthropic":
		if m.config.AnthropicAPIKey != "" || m.config.ProviderURL != "" {
			return "anthropic", model
		}
	case "google":
		if m.config.GoogleAPIKey != "" || m.config.ProviderURL != "" {
			return "google", model
		}
	case "argo":
		if m.config.ArgoUser != "" {
			return "argo", model
		}
	default: // "openai" or any other value defaults to OpenAI
		if m.config.OpenAIAPIKey != "" || m.config.ProviderURL != "" {
			return "openai", model
		}
	}

	// Fallback to any available provider if preferred provider has no credentials
	if m.config.AnthropicAPIKey != "" {
		return "anthropic", model
	}
	if m.config.OpenAIAPIKey != "" {
		return "openai", model
	}
	if m.config.GoogleAPIKey != "" {
		return "google", model
	}
	if m.config.ArgoUser != "" {
		return "argo", model
	}

	// No credentials available
	return "", ""
}

// GetAPIKey returns the appropriate API key for a provider
func (m *ModelMapper) GetAPIKey(provider string) string {
	switch provider {
	case "openai":
		return m.config.OpenAIAPIKey
	case "google":
		return m.config.GoogleAPIKey
	case "anthropic":
		return m.config.AnthropicAPIKey
	default:
		return ""
	}
}
