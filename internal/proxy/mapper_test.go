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
				Provider:     "openai",
				SmallModel:   "gpt-4o-mini",
				BigModel:     "gpt-4o",
				OpenAIModels: []string{"gpt-4o", "gpt-4o-mini"},
				OpenAIAPIKey: "test-key",
			},
			inputModel:       "claude-3-haiku-20240307",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o-mini",
		},
		{
			name: "sonnet maps to big model",
			config: &Config{
				Provider:     "openai",
				SmallModel:   "gpt-4o-mini",
				BigModel:     "gpt-4o",
				OpenAIModels: []string{"gpt-4o", "gpt-4o-mini"},
				OpenAIAPIKey: "test-key",
			},
			inputModel:       "claude-3-sonnet-20240229",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "haiku with google preference",
			config: &Config{
				Provider:     "google",
				SmallModel:   "gemini-2.0-flash",
				BigModel:     "gemini-2.5-pro-preview-03-25",
				GeminiModels: []string{"gemini-2.0-flash", "gemini-2.5-pro-preview-03-25"},
				GeminiAPIKey: "test-key",
			},
			inputModel:       "claude-3-haiku",
			expectedProvider: "gemini",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "sonnet with argo preference",
			config: &Config{
				Provider:   "argo",
				SmallModel: "gemini25flash",
				BigModel:   "claudesonnet4",
				ArgoModels: []string{"gemini25flash", "claudesonnet4"},
				ArgoUser:   "testuser",
			},
			inputModel:       "claude-3-sonnet",
			expectedProvider: "argo",
			expectedModel:    "claudesonnet4",
		},
		{
			name: "direct openai model",
			config: &Config{
				Provider:     "google",
				OpenAIModels: []string{"gpt-4o", "gpt-4o-mini"},
				OpenAIAPIKey: "test-key",
			},
			inputModel:       "gpt-4o",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "direct gemini model",
			config: &Config{
				Provider:     "openai",
				GeminiModels: []string{"gemini-2.0-flash"},
				GeminiAPIKey: "test-key",
			},
			inputModel:       "gemini-2.0-flash",
			expectedProvider: "gemini",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "remove provider prefix",
			config: &Config{
				Provider:     "openai",
				OpenAIModels: []string{"gpt-4o"},
				OpenAIAPIKey: "test-key",
			},
			inputModel:       "openai/gpt-4o",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "unknown model uses preferred provider",
			config: &Config{
				Provider: "argo",
				ArgoUser: "testuser",
			},
			inputModel:       "unknown-model",
			expectedProvider: "argo",
			expectedModel:    "unknown-model",
		},
		{
			name: "anthropic prefix removal",
			config: &Config{
				Provider:     "openai",
				BigModel:     "gpt-4o",
				OpenAIModels: []string{"gpt-4o"},
				OpenAIAPIKey: "test-key",
			},
			inputModel:       "anthropic/claude-3-sonnet",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := NewModelMapper(tt.config)
			provider, model := mapper.MapModel(tt.inputModel)

			if provider != tt.expectedProvider {
				t.Errorf("MapModel() provider = %v, want %v", provider, tt.expectedProvider)
			}
			if model != tt.expectedModel {
				t.Errorf("MapModel() model = %v, want %v", model, tt.expectedModel)
			}
		})
	}
}

func TestGetAPIKey(t *testing.T) {
	config := &Config{
		OpenAIAPIKey:    "sk-openai-key",
		GeminiAPIKey:    "gemini-key",
		AnthropicAPIKey: "anthropic-key",
	}
	mapper := NewModelMapper(config)

	tests := []struct {
		provider string
		expected string
	}{
		{"openai", "sk-openai-key"},
		{"gemini", "gemini-key"},
		{"anthropic", "anthropic-key"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := mapper.GetAPIKey(tt.provider)
			if got != tt.expected {
				t.Errorf("GetAPIKey(%s) = %v, want %v", tt.provider, got, tt.expected)
			}
		})
	}
}
