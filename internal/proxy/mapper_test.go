package proxy

import (
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
				Provider:   "openai",
				SmallModel: "gpt-4o-mini",
				Model:      "gpt-4o",
			},
			inputModel:       "claude-3-haiku-20240307",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o-mini",
		},
		{
			name: "sonnet maps to model",
			config: &Config{
				Provider:   "openai",
				SmallModel: "gpt-4o-mini",
				Model:      "gpt-4o",
			},
			inputModel:       "claude-3-sonnet-20240229",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "opus maps to model",
			config: &Config{
				Provider:   "google",
				SmallModel: "gemini-2.0-flash",
				Model:      "gemini-2.5-pro",
			},
			inputModel:       "claude-3-opus-20240229",
			expectedProvider: "google",
			expectedModel:    "gemini-2.5-pro",
		},
		{
			name: "haiku with google provider",
			config: &Config{
				Provider:   "google",
				SmallModel: "gemini-2.0-flash",
				Model:      "gemini-2.5-pro-preview-03-25",
			},
			inputModel:       "claude-3-haiku",
			expectedProvider: "google",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "sonnet with argo provider",
			config: &Config{
				Provider:   "argo",
				SmallModel: "gemini25flash",
				Model:      "claudesonnet4",
			},
			inputModel:       "claude-3-sonnet",
			expectedProvider: "argo",
			expectedModel:    "claudesonnet4",
		},
		{
			name: "non-claude model passes through unchanged",
			config: &Config{
				Provider:   "openai",
				SmallModel: "gpt-4o-mini",
				Model:      "gpt-4o",
			},
			inputModel:       "gpt-4o",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "gemini model passes through unchanged",
			config: &Config{
				Provider:   "google",
				SmallModel: "gemini-flash",
				Model:      "gemini-pro",
			},
			inputModel:       "gemini-2.0-flash",
			expectedProvider: "google",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "non-claude model passes through unchanged",
			config: &Config{
				Provider:   "openai",
				SmallModel: "small",
				Model:      "big",
			},
			inputModel:       "gpt-4o",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "unknown model passes through with configured provider",
			config: &Config{
				Provider:   "argo",
				SmallModel: "small",
				Model:      "big",
			},
			inputModel:       "unknown-model",
			expectedProvider: "argo",
			expectedModel:    "unknown-model",
		},
		{
			name: "haiku model maps to small model",
			config: &Config{
				Provider:   "anthropic",
				SmallModel: "claude-3-haiku-20240307",
				Model:      "claude-3-opus-20240229",
			},
			inputModel:       "claude-3-haiku-20240307",
			expectedProvider: "anthropic",
			expectedModel:    "claude-3-haiku-20240307",
		},
		{
			name: "provider always comes from config",
			config: &Config{
				Provider:   "argo",
				SmallModel: "small",
				Model:      "big",
			},
			inputModel:       "gpt-4",
			expectedProvider: "argo",
			expectedModel:    "gpt-4",
		},
		{
			name: "claude-haiku-3 also maps to small model",
			config: &Config{
				Provider:   "openai",
				SmallModel: "gpt-3.5-turbo",
				Model:      "gpt-4",
			},
			inputModel:       "claude-haiku-3",
			expectedProvider: "openai",
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
