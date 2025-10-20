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

// MapModel maps an incoming model name to the appropriate model for the configured provider.
// For Anthropic models: haiku -> SmallModel, others -> Model
// For all other models: pass through unchanged
// Note: Provider is always taken from config, not returned here (KISS principle)
func (m *ModelMapper) MapModel(model string) string {
	// Map Anthropic models to big/small models
	if strings.HasPrefix(strings.ToLower(model), "claude-") {
		if strings.Contains(strings.ToLower(model), "haiku") {
			// Haiku models map to SmallModel
			return m.config.SmallModel
		}
		// All non-haiku Claude models (opus, sonnet, etc.) map to Model
		return m.config.Model
	}
	// Pass through all other models unchanged
	return model
}
