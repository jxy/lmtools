package apiproxy

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
				PreferredProvider: "openai",
				SmallModel:        "gpt-4o-mini",
				BigModel:          "gpt-4o",
				OpenAIModels:      []string{"gpt-4o", "gpt-4o-mini"},
				OpenAIAPIKey:      "test-key",
			},
			inputModel:       "claude-3-haiku-20240307",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o-mini",
		},
		{
			name: "sonnet maps to big model",
			config: &Config{
				PreferredProvider: "openai",
				SmallModel:        "gpt-4o-mini",
				BigModel:          "gpt-4o",
				OpenAIModels:      []string{"gpt-4o", "gpt-4o-mini"},
				OpenAIAPIKey:      "test-key",
			},
			inputModel:       "claude-3-sonnet-20240229",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "haiku with google preference",
			config: &Config{
				PreferredProvider: "google",
				SmallModel:        "gemini-2.0-flash",
				BigModel:          "gemini-2.5-pro-preview-03-25",
				GeminiModels:      []string{"gemini-2.0-flash", "gemini-2.5-pro-preview-03-25"},
				GeminiAPIKey:      "test-key",
			},
			inputModel:       "claude-3-haiku",
			expectedProvider: "gemini",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "sonnet with argo preference",
			config: &Config{
				PreferredProvider: "argo",
				SmallModel:        "gemini25flash",
				BigModel:          "claudesonnet4",
				ArgoModels:        []string{"gemini25flash", "claudesonnet4"},
				ArgoUser:          "testuser",
			},
			inputModel:       "claude-3-sonnet",
			expectedProvider: "argo",
			expectedModel:    "claudesonnet4",
		},
		{
			name: "direct openai model",
			config: &Config{
				PreferredProvider: "google",
				OpenAIModels:      []string{"gpt-4o", "gpt-4o-mini"},
				OpenAIAPIKey:      "test-key",
			},
			inputModel:       "gpt-4o",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "direct gemini model",
			config: &Config{
				PreferredProvider: "openai",
				GeminiModels:      []string{"gemini-2.0-flash"},
				GeminiAPIKey:      "test-key",
			},
			inputModel:       "gemini-2.0-flash",
			expectedProvider: "gemini",
			expectedModel:    "gemini-2.0-flash",
		},
		{
			name: "remove provider prefix",
			config: &Config{
				PreferredProvider: "openai",
				OpenAIModels:      []string{"gpt-4o"},
				OpenAIAPIKey:      "test-key",
			},
			inputModel:       "openai/gpt-4o",
			expectedProvider: "openai",
			expectedModel:    "gpt-4o",
		},
		{
			name: "unknown model uses preferred provider",
			config: &Config{
				PreferredProvider: "argo",
				ArgoUser:          "testuser",
			},
			inputModel:       "unknown-model",
			expectedProvider: "argo",
			expectedModel:    "unknown-model",
		},
		{
			name: "anthropic prefix removal",
			config: &Config{
				PreferredProvider: "openai",
				BigModel:          "gpt-4o",
				OpenAIModels:      []string{"gpt-4o"},
				OpenAIAPIKey:      "test-key",
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

func TestGetProviderURL(t *testing.T) {
	tests := []struct {
		provider string
		expected string
	}{
		{"openai", "https://api.openai.com/v1/chat/completions"},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/models"},
		{"anthropic", "https://api.anthropic.com/v1/messages"},
		{"argo", ""},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetProviderURL(tt.provider)
			if got != tt.expected {
				t.Errorf("GetProviderURL(%s) = %v, want %v", tt.provider, got, tt.expected)
			}
		})
	}
}

func TestGetArgoURL(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		endpoint string
		expected string
	}{
		{
			name:     "prod chat",
			env:      "prod",
			endpoint: "chat",
			expected: "https://apps.inside.anl.gov/argoapi/api/v1/resource/chat/",
		},
		{
			name:     "dev streamchat",
			env:      "dev",
			endpoint: "streamchat",
			expected: "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource/streamchat/",
		},
		{
			name:     "default embed",
			env:      "",
			endpoint: "embed",
			expected: "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource/embed/",
		},
		{
			name:     "custom url",
			env:      "http://localhost:8080",
			endpoint: "chat",
			expected: "http://localhost:8080/chat/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetArgoURL(tt.env, tt.endpoint)
			if got != tt.expected {
				t.Errorf("GetArgoURL(%s, %s) = %v, want %v", tt.env, tt.endpoint, got, tt.expected)
			}
		})
	}
}
