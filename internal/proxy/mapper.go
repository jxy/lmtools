package proxy

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

// MapModel maps an incoming model name using the configured model-map rules.
// The first matching rule wins. Models with no matching rule pass through unchanged.
func (m *ModelMapper) MapModel(model string) string {
	if m == nil || m.config == nil {
		return model
	}

	for _, rule := range m.config.ModelMapRules {
		if rule.matches(model) {
			return rule.Model
		}
	}

	return model
}
