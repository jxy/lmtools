package proxy

import (
	"strings"
	"testing"
)

func TestDynamicModelDefaults(t *testing.T) {
	tests := []struct {
		name               string
		preferredProvider  string
		inputModel         string
		inputSmallModel    string
		expectedModel      string
		expectedSmallModel string
	}{
		{
			name:               "OpenAI provider with default models",
			preferredProvider:  "openai",
			inputModel:         "",
			inputSmallModel:    "",
			expectedModel:      "gpt-5",
			expectedSmallModel: "gpt-5-mini",
		},
		{
			name:               "Google provider with default models",
			preferredProvider:  "google",
			inputModel:         "",
			inputSmallModel:    "",
			expectedModel:      "gemini-2.5-pro",
			expectedSmallModel: "gemini-2.5-flash",
		},
		{
			name:               "Argo provider keeps default models",
			preferredProvider:  "argo",
			inputModel:         "",
			inputSmallModel:    "",
			expectedModel:      "gpt5",
			expectedSmallModel: "gpt5mini",
		},
		{
			name:               "OpenAI with custom models",
			preferredProvider:  "openai",
			inputModel:         "gpt-4o",
			inputSmallModel:    "gpt-4o-mini",
			expectedModel:      "gpt-4o",
			expectedSmallModel: "gpt-4o-mini",
		},
		{
			name:               "Only big model changed",
			preferredProvider:  "openai",
			inputModel:         "gpt-4o",
			inputSmallModel:    "claudesonnet4",
			expectedModel:      "gpt-4o",
			expectedSmallModel: "gpt-5-mini", // Should use OpenAI default since claudesonnet4 matches old default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create config and apply dynamic defaults
			config := &Config{
				Provider:   tt.preferredProvider,
				Model:      tt.inputModel,
				SmallModel: tt.inputSmallModel,
			}

			// Apply dynamic defaults using the actual method
			config.ApplyDynamicModelDefaults()

			if config.Model != tt.expectedModel {
				t.Errorf("Expected model=%s, got %s", tt.expectedModel, config.Model)
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

			config.InitializeURLs()

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

func TestUnifiedAPIKeyValidation(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		openAIKey     string
		geminiKey     string
		argoUser      string
		providerURL   string
		expectError   bool
		errorContains string
	}{
		{
			name:        "OpenAI provider with API key",
			provider:    "openai",
			openAIKey:   "test-key",
			expectError: false,
		},
		{
			name:          "OpenAI provider without API key",
			provider:      "openai",
			expectError:   true,
			errorContains: "-api-key-file is required when -provider is 'openai'",
		},
		{
			name:        "OpenAI provider with custom URL (no key needed)",
			provider:    "openai",
			providerURL: "http://localhost:11434/v1",
			expectError: false,
		},
		{
			name:        "Google provider with API key",
			provider:    "google",
			geminiKey:   "test-key",
			expectError: false,
		},
		{
			name:          "Google provider without API key",
			provider:      "google",
			expectError:   true,
			errorContains: "-api-key-file is required when -provider is 'google'",
		},
		{
			name:        "Argo provider with user",
			provider:    "argo",
			argoUser:    "testuser",
			expectError: false,
		},
		{
			name:          "Argo provider without user",
			provider:      "argo",
			expectError:   true,
			errorContains: "-argo-user is required when -provider is 'argo'",
		},
		{
			name:          "Invalid provider",
			provider:      "invalid",
			expectError:   true,
			errorContains: "invalid -provider: invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Provider:     tt.provider,
				OpenAIAPIKey: tt.openAIKey,
				GeminiAPIKey: tt.geminiKey,
				ArgoUser:     tt.argoUser,
				ProviderURL:  tt.providerURL,
			}

			err := config.Validate()
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}
