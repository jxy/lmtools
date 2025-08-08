package proxy

import (
	"testing"
)

func TestDynamicModelDefaults(t *testing.T) {
	tests := []struct {
		name               string
		preferredProvider  string
		inputBigModel      string
		inputSmallModel    string
		expectedBigModel   string
		expectedSmallModel string
	}{
		{
			name:               "OpenAI provider with default models",
			preferredProvider:  "openai",
			inputBigModel:      "claudeopus4",
			inputSmallModel:    "claudesonnet4",
			expectedBigModel:   "o3-mini",
			expectedSmallModel: "gpt-4o-mini",
		},
		{
			name:               "Google provider with default models",
			preferredProvider:  "google",
			inputBigModel:      "claudeopus4",
			inputSmallModel:    "claudesonnet4",
			expectedBigModel:   "gemini-2.5-pro-preview-03-25",
			expectedSmallModel: "gemini-2.0-flash",
		},
		{
			name:               "Argo provider keeps default models",
			preferredProvider:  "argo",
			inputBigModel:      "claudeopus4",
			inputSmallModel:    "claudesonnet4",
			expectedBigModel:   "claudeopus4",
			expectedSmallModel: "claudesonnet4",
		},
		{
			name:               "OpenAI with custom models",
			preferredProvider:  "openai",
			inputBigModel:      "gpt-4o",
			inputSmallModel:    "gpt-4o-mini",
			expectedBigModel:   "gpt-4o",
			expectedSmallModel: "gpt-4o-mini",
		},
		{
			name:               "Only big model changed",
			preferredProvider:  "openai",
			inputBigModel:      "gpt-4o",
			inputSmallModel:    "claudesonnet4",
			expectedBigModel:   "gpt-4o",
			expectedSmallModel: "claudesonnet4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create config and apply dynamic defaults
			config := &Config{
				Provider:   tt.preferredProvider,
				BigModel:   tt.inputBigModel,
				SmallModel: tt.inputSmallModel,
			}

			// Apply dynamic defaults using the actual method
			config.ApplyDynamicModelDefaults()

			if config.BigModel != tt.expectedBigModel {
				t.Errorf("Expected bigModel=%s, got %s", tt.expectedBigModel, config.BigModel)
			}
			if config.SmallModel != tt.expectedSmallModel {
				t.Errorf("Expected smallModel=%s, got %s", tt.expectedSmallModel, config.SmallModel)
			}
		})
	}
}

func TestProviderURLOverride(t *testing.T) {
	tests := []struct {
		name              string
		preferredProvider string
		providerURL       string
		expectedOpenAI    string
		expectedGemini    string
		expectedArgo      string
	}{
		{
			name:              "OpenAI custom URL",
			preferredProvider: "openai",
			providerURL:       "https://custom-openai.com/v1/chat",
			expectedOpenAI:    "https://custom-openai.com/v1/chat",
			expectedGemini:    "https://generativelanguage.googleapis.com/v1beta/models",
			expectedArgo:      "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource",
		},
		{
			name:              "Gemini custom URL",
			preferredProvider: "google",
			providerURL:       "https://custom-gemini.com/v1beta",
			expectedOpenAI:    "https://api.openai.com/v1/chat/completions",
			expectedGemini:    "https://custom-gemini.com/v1beta",
			expectedArgo:      "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource",
		},
		{
			name:              "Argo custom URL",
			preferredProvider: "argo",
			providerURL:       "https://custom-argo.com/api",
			expectedOpenAI:    "https://api.openai.com/v1/chat/completions",
			expectedGemini:    "https://generativelanguage.googleapis.com/v1beta/models",
			expectedArgo:      "https://custom-argo.com/api",
		},
		{
			name:              "No custom URL",
			preferredProvider: "openai",
			providerURL:       "",
			expectedOpenAI:    "https://api.openai.com/v1/chat/completions",
			expectedGemini:    "https://generativelanguage.googleapis.com/v1beta/models",
			expectedArgo:      "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Provider:    tt.preferredProvider,
				ProviderURL: tt.providerURL,
				ArgoEnv:     "dev",
			}

			config.InitializeModelLists()

			if config.OpenAIURL != tt.expectedOpenAI {
				t.Errorf("Expected OpenAIURL=%s, got %s", tt.expectedOpenAI, config.OpenAIURL)
			}
			if config.GeminiURL != tt.expectedGemini {
				t.Errorf("Expected GeminiURL=%s, got %s", tt.expectedGemini, config.GeminiURL)
			}
			if config.ArgoBaseURL != tt.expectedArgo {
				t.Errorf("Expected ArgoBaseURL=%s, got %s", tt.expectedArgo, config.ArgoBaseURL)
			}
		})
	}
}
