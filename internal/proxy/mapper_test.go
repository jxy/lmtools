package proxy

import (
	"lmtools/internal/constants"
	"testing"
)

func TestModelMapper(t *testing.T) {
	tests := []struct {
		name             string
		config           *Config
		inputModel       string
		expectedProvider string
		expectedModel    string
	}{
		{
			name: "haiku maps to small model",
			config: &Config{
				Provider:   constants.ProviderOpenAI,
				SmallModel: "gpt-4o-mini",
				Model:      "gpt-4o",
			},
			inputModel:       "claude-3-haiku-20240307",
			expectedProvider: constants.ProviderOpenAI,
			expectedModel:    "gpt-4o-mini",
		},
		{
			name: "sonnet maps to model",
			config: &Config{
				Provider:   constants.ProviderOpenAI,
				SmallModel: "gpt-4o-mini",
				Model:      "gpt-4o",
			},
			inputModel:       "claude-3-sonnet-20240229",
			expectedProvider: constants.ProviderOpenAI,
			expectedModel:    "gpt-4o",
		},
		{
			name: "opus maps to model",
			config: &Config{
				Provider:   constants.ProviderGoogle,
				SmallModel: "gemini-2.0-flash",
				Model:      "gemini-2.5-pro",
			},
			inputModel:       "claude-3-opus-20240229",
			expectedProvider: constants.ProviderGoogle,
			expectedModel:    "gemini-2.5-pro",
		},
		{
			name: "haiku with google provider",
			config: &Config{
				Provider:   constants.ProviderGoogle,
				SmallModel: "gemini-2.0-flash",
				Model:      "gemini-2.5-pro-preview-03-25",
			},
			inputModel:       "claude-3-haiku",
			expectedProvider: constants.ProviderGoogle,
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "sonnet with argo provider",
			config: &Config{
				Provider:   constants.ProviderArgo,
				SmallModel: "gemini25flash",
				Model:      "claudesonnet4",
			},
			inputModel:       "claude-3-sonnet",
			expectedProvider: constants.ProviderArgo,
			expectedModel:    "claudesonnet4",
		},
		{
			name: "non-claude model passes through unchanged",
			config: &Config{
				Provider:   constants.ProviderOpenAI,
				SmallModel: "gpt-4o-mini",
				Model:      "gpt-4o",
			},
			inputModel:       "gpt-4o",
			expectedProvider: constants.ProviderOpenAI,
			expectedModel:    "gpt-4o",
		},
		{
			name: "gemini model passes through unchanged",
			config: &Config{
				Provider:   constants.ProviderGoogle,
				SmallModel: "gemini-flash",
				Model:      "gemini-pro",
			},
			inputModel:       "gemini-2.0-flash",
			expectedProvider: constants.ProviderGoogle,
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "non-claude model passes through unchanged",
			config: &Config{
				Provider:   constants.ProviderOpenAI,
				SmallModel: "small",
				Model:      "big",
			},
			inputModel:       "gpt-4o",
			expectedProvider: constants.ProviderOpenAI,
			expectedModel:    "gpt-4o",
		},
		{
			name: "unknown model passes through with configured provider",
			config: &Config{
				Provider:   constants.ProviderArgo,
				SmallModel: "small",
				Model:      "big",
			},
			inputModel:       "unknown-model",
			expectedProvider: constants.ProviderArgo,
			expectedModel:    "unknown-model",
		},
		{
			name: "haiku model maps to small model",
			config: &Config{
				Provider:   constants.ProviderAnthropic,
				SmallModel: "claude-3-haiku-20240307",
				Model:      "claude-3-opus-20240229",
			},
			inputModel:       "claude-3-haiku-20240307",
			expectedProvider: constants.ProviderAnthropic,
			expectedModel:    "claude-3-haiku-20240307",
		},
		{
			name: "provider always comes from config",
			config: &Config{
				Provider:   constants.ProviderArgo,
				SmallModel: "small",
				Model:      "big",
			},
			inputModel:       "gpt-4",
			expectedProvider: constants.ProviderArgo,
			expectedModel:    "gpt-4",
		},
		{
			name: "claude-haiku-3 also maps to small model",
			config: &Config{
				Provider:   constants.ProviderOpenAI,
				SmallModel: "gpt-3.5-turbo",
				Model:      "gpt-4",
			},
			inputModel:       "claude-haiku-3",
			expectedProvider: constants.ProviderOpenAI,
			expectedModel:    "gpt-3.5-turbo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := NewModelMapper(tt.config)
			model := mapper.MapModel(tt.inputModel)
			provider := tt.config.Provider

			if provider != tt.expectedProvider {
				t.Errorf("MapModel() provider = %v, want %v", provider, tt.expectedProvider)
			}
			if model != tt.expectedModel {
				t.Errorf("MapModel() model = %v, want %v", model, tt.expectedModel)
			}
		})
	}
}
