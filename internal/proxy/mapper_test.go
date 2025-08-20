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
				Model:        "gpt-4o",
				OpenAIAPIKey: "test-key",
			},
			inputModel:       "claude-3-haiku-20240307",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o-mini",
		},
		{
			name: "sonnet maps to model",
			config: &Config{
				Provider:     "openai",
				SmallModel:   "gpt-4o-mini",
				Model:        "gpt-4o",
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
				Model:        "gemini-2.5-pro-preview-03-25",
				GoogleAPIKey: "test-key",
			},
			inputModel:       "claude-3-haiku",
			expectedProvider: "google",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "sonnet with argo preference",
			config: &Config{
				Provider:   "argo",
				SmallModel: "gemini25flash",
				Model:      "claudesonnet4",
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
				GoogleAPIKey: "test-key",
			},
			inputModel:       "gemini-2.0-flash",
			expectedProvider: "google",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "remove provider prefix",
			config: &Config{
				Provider:     "openai",
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
				Model:        "gpt-4o",
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

func TestProviderURLAsCredential(t *testing.T) {
	tests := []struct {
		name             string
		config           *Config
		inputModel       string
		expectedProvider string
		expectedModel    string
		description      string
	}{
		{
			name: "ProviderURL with OpenAI allows selection without API key",
			config: &Config{
				Provider:        "openai",
				ProviderURL:     "http://localhost:11434/v1",
				Model:           "gpt-4o",
				AnthropicAPIKey: "valid-key", // Has another valid key
			},
			inputModel:       "claude-3-sonnet",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
			description:      "Should select OpenAI when ProviderURL is set, even without API key",
		},
		{
			name: "ProviderURL with Google allows selection without API key",
			config: &Config{
				Provider:     "google",
				ProviderURL:  "http://localhost:8080/gemini",
				SmallModel:   "gemini-flash",
				OpenAIAPIKey: "valid-key", // Has another valid key
			},
			inputModel:       "claude-3-haiku",
			expectedProvider: "google",
			expectedModel:    "gemini-flash",
			description:      "Should select Google when ProviderURL is set, even without API key",
		},
		{
			name: "ProviderURL with Anthropic allows selection without API key",
			config: &Config{
				Provider:     "anthropic",
				ProviderURL:  "http://localhost:8080/anthropic",
				Model:        "claude-3-opus",
				OpenAIAPIKey: "valid-key", // Has another valid key
			},
			inputModel:       "claude-3-opus",
			expectedProvider: "anthropic",
			expectedModel:    "claude-3-opus",
			description:      "Should select Anthropic when ProviderURL is set, even without API key",
		},
		{
			name: "No ProviderURL and no API key falls back to available provider",
			config: &Config{
				Provider:        "openai",
				Model:           "gpt-4o",
				AnthropicAPIKey: "valid-key", // Only Anthropic has key
			},
			inputModel:       "claude-3-sonnet",
			expectedProvider: "anthropic",
			expectedModel:    "gpt-4o",
			description:      "Should fallback to Anthropic when OpenAI has no credentials",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := NewModelMapper(tt.config)
			provider, model := mapper.MapModel(tt.inputModel)

			if provider != tt.expectedProvider {
				t.Errorf("%s: MapModel() provider = %v, want %v",
					tt.description, provider, tt.expectedProvider)
			}
			if model != tt.expectedModel {
				t.Errorf("%s: MapModel() model = %v, want %v",
					tt.description, model, tt.expectedModel)
			}
		})
	}
}

func TestGetAPIKey(t *testing.T) {
	config := &Config{
		OpenAIAPIKey:    "sk-openai-key",
		GoogleAPIKey:    "gemini-key",
		AnthropicAPIKey: "anthropic-key",
	}
	mapper := NewModelMapper(config)

	tests := []struct {
		provider string
		expected string
	}{
		{"openai", "sk-openai-key"},
		{"google", "gemini-key"},
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
