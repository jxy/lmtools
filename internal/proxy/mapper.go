package proxy

import (
	"strings"
	"sync"
)

// ModelMapper handles model name mapping between different providers
type ModelMapper struct {
	config *Config
	// Pre-computed maps for O(1) lookups
	openAIModels map[string]bool
	geminiModels map[string]bool
	argoModels   map[string]bool
	mu           sync.RWMutex
}

// NewModelMapper creates a new model mapper
func NewModelMapper(config *Config) *ModelMapper {
	m := &ModelMapper{
		config:       config,
		openAIModels: make(map[string]bool),
		geminiModels: make(map[string]bool),
		argoModels:   make(map[string]bool),
	}

	// Pre-compute model maps
	for _, model := range config.OpenAIModels {
		m.openAIModels[model] = true
	}
	for _, model := range config.GeminiModels {
		m.geminiModels[model] = true
	}
	for _, model := range config.ArgoModels {
		m.argoModels[model] = true
	}

	return m
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
			// All non-haiku Claude models map to BIG_MODEL
			return m.mapToBigModel()
		}
	}

	// Check if the model exists in our known lists (and API key is available)
	if m.isOpenAIModel(cleanModel) && m.config.OpenAIAPIKey != "" {
		return "openai", cleanModel
	}
	if m.isGeminiModel(cleanModel) && m.config.GeminiAPIKey != "" {
		return "gemini", cleanModel
	}
	if m.isArgoModel(cleanModel) && m.config.ArgoUser != "" {
		return "argo", cleanModel
	}

	// If model is not recognized, use preferred provider with BIG_MODEL as fallback
	// If BIG_MODEL is not set, use the original model name
	switch m.config.Provider {
	case "google":
		if m.config.GeminiAPIKey != "" {
			if m.config.BigModel != "" {
				return "gemini", m.config.BigModel
			}
			return "gemini", cleanModel
		}
	case "argo":
		if m.config.ArgoUser != "" {
			if m.config.BigModel != "" {
				return "argo", m.config.BigModel
			}
			return "argo", cleanModel
		}
	default:
		if m.config.OpenAIAPIKey != "" {
			if m.config.BigModel != "" {
				return "openai", m.config.BigModel
			}
			return "openai", cleanModel
		}
	}

	// No valid provider with API key found
	return "", ""
}

// cleanModelName removes provider prefixes from model names
func (m *ModelMapper) cleanModelName(model string) string {
	prefixes := []string{"anthropic/", "openai/", "gemini/", "google/", "argo/"}
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

	// Determine provider based on model and API key availability
	if m.config.Provider == "google" && m.isGeminiModel(smallModel) && m.config.GeminiAPIKey != "" {
		return "gemini", smallModel
	}
	if m.config.Provider == "argo" && m.isArgoModel(smallModel) && m.config.ArgoUser != "" {
		return "argo", smallModel
	}
	if m.isGeminiModel(smallModel) && m.config.GeminiAPIKey != "" {
		return "gemini", smallModel
	}
	if m.isArgoModel(smallModel) && m.config.ArgoUser != "" {
		return "argo", smallModel
	}
	// Default to OpenAI if API key is available
	if m.config.OpenAIAPIKey != "" {
		return "openai", smallModel
	}
	// No API keys available
	return "", ""
}

// mapToBigModel maps to the configured big model
func (m *ModelMapper) mapToBigModel() (provider, model string) {
	bigModel := m.config.BigModel

	// Determine provider based on model and API key availability
	if m.config.Provider == "google" && m.isGeminiModel(bigModel) && m.config.GeminiAPIKey != "" {
		return "gemini", bigModel
	}
	if m.config.Provider == "argo" && m.isArgoModel(bigModel) && m.config.ArgoUser != "" {
		return "argo", bigModel
	}
	if m.isGeminiModel(bigModel) && m.config.GeminiAPIKey != "" {
		return "gemini", bigModel
	}
	if m.isArgoModel(bigModel) && m.config.ArgoUser != "" {
		return "argo", bigModel
	}
	// Default to OpenAI if API key is available
	if m.config.OpenAIAPIKey != "" {
		return "openai", bigModel
	}
	// No API keys available
	return "", ""
}

// isOpenAIModel checks if a model is in the OpenAI models list
func (m *ModelMapper) isOpenAIModel(model string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.openAIModels[model]
}

// isGeminiModel checks if a model is in the Gemini models list
func (m *ModelMapper) isGeminiModel(model string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.geminiModels[model]
}

// isArgoModel checks if a model is in the Argo models list
func (m *ModelMapper) isArgoModel(model string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.argoModels[model]
}

// GetAPIKey returns the appropriate API key for a provider
func (m *ModelMapper) GetAPIKey(provider string) string {
	switch provider {
	case "openai":
		return m.config.OpenAIAPIKey
	case "gemini":
		return m.config.GeminiAPIKey
	case "anthropic":
		return m.config.AnthropicAPIKey
	default:
		return ""
	}
}
