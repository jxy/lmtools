package proxy

import (
	"lmtools/internal/constants"
	"strings"
	"testing"
)

func TestValidateModelMapRules(t *testing.T) {
	tests := []struct {
		name      string
		rules     []ModelMapRule
		wantError bool
	}{
		{
			name: "valid rules",
			rules: []ModelMapRule{
				{Pattern: "^claude-.*", Model: "claude-opus-4-1"},
				{Pattern: "^gpt-4o$", Model: "gpt-5"},
			},
		},
		{
			name:      "empty regex",
			rules:     []ModelMapRule{{Pattern: "", Model: "gpt-5"}},
			wantError: true,
		},
		{
			name:      "empty model",
			rules:     []ModelMapRule{{Pattern: "^gpt-.*", Model: ""}},
			wantError: true,
		},
		{
			name:      "invalid regex",
			rules:     []ModelMapRule{{Pattern: "^(bad", Model: "gpt-5"}},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModelMapRules(tt.rules)
			if (err != nil) != tt.wantError {
				t.Fatalf("ValidateModelMapRules() error = %v, wantError %v", err, tt.wantError)
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
		expectedGoogle    string
		expectedArgoBase  string // Only checked when provider is argo
	}{
		{
			name:              "OpenAI custom URL",
			preferredProvider: constants.ProviderOpenAI,
			providerURL:       "https://custom-openai.com/v1/chat",
			expectedOpenAI:    "https://custom-openai.com/v1/chat/chat/completions",
			expectedGoogle:    "", // Only selected provider URLs are initialized
			expectedArgoBase:  "", // Not set for non-Argo providers
		},
		{
			name:              "Google custom URL",
			preferredProvider: constants.ProviderGoogle,
			providerURL:       "https://custom-google.com/v1beta",
			expectedOpenAI:    "", // Only selected provider URLs are initialized
			expectedGoogle:    "https://custom-google.com/v1beta/models",
			expectedArgoBase:  "", // Not set for non-Argo providers
		},
		{
			name:              "Argo custom URL",
			preferredProvider: constants.ProviderArgo,
			providerURL:       "https://custom-argo.com/api",
			expectedOpenAI:    "",                                            // Only selected provider URLs are initialized
			expectedGoogle:    "",                                            // Only selected provider URLs are initialized
			expectedArgoBase:  "https://custom-argo.com/api/api/v1/resource", // argoResourcePrefix adds /api/v1/resource when not present
		},
		{
			name:              "No custom URL",
			preferredProvider: constants.ProviderOpenAI,
			providerURL:       "",
			expectedOpenAI:    "https://api.openai.com/v1/chat/completions",
			expectedGoogle:    "", // Only selected provider URLs are initialized
			expectedArgoBase:  "", // Not set for non-Argo providers
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Provider:    tt.preferredProvider,
				ProviderURL: tt.providerURL,
				ArgoEnv:     "dev",
			}

			endpoints, err := NewEndpoints(config)
			if err != nil {
				t.Fatalf("Failed to create endpoints: %v", err)
			}

			if endpoints.OpenAI != tt.expectedOpenAI {
				t.Errorf("Expected OpenAI=%s, got %s", tt.expectedOpenAI, endpoints.OpenAI)
			}
			if endpoints.Google != tt.expectedGoogle {
				t.Errorf("Expected Google=%s, got %s", tt.expectedGoogle, endpoints.Google)
			}
			// Only check ArgoBase when testing Argo provider
			if tt.preferredProvider == constants.ProviderArgo && endpoints.ArgoBase != tt.expectedArgoBase {
				t.Errorf("Expected ArgoBase=%s, got %s", tt.expectedArgoBase, endpoints.ArgoBase)
			}
			// For non-Argo providers, ArgoBase should be empty
			if tt.preferredProvider != constants.ProviderArgo && endpoints.ArgoBase != "" {
				t.Errorf("Expected ArgoBase to be empty for non-Argo provider, got %s", endpoints.ArgoBase)
			}
		})
	}
}

func TestUnifiedAPIKeyValidation(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		openAIKey     string
		googleKey     string
		argoUser      string
		providerURL   string
		expectError   bool
		errorContains string
	}{
		{
			name:        "OpenAI provider with API key",
			provider:    constants.ProviderOpenAI,
			openAIKey:   "test-key",
			expectError: false,
		},
		{
			name:          "OpenAI provider without API key",
			provider:      constants.ProviderOpenAI,
			expectError:   true,
			errorContains: "-api-key-file is required when -provider is 'openai'",
		},
		{
			name:        "OpenAI provider with custom URL (no key needed)",
			provider:    constants.ProviderOpenAI,
			providerURL: "http://localhost:11434/v1",
			expectError: false,
		},
		{
			name:        "Google provider with API key",
			provider:    constants.ProviderGoogle,
			googleKey:   "test-key",
			expectError: false,
		},
		{
			name:        "Google provider with custom URL (no key needed)",
			provider:    constants.ProviderGoogle,
			providerURL: "http://localhost:11434/v1beta",
			expectError: false,
		},
		{
			name:          "Google provider without API key",
			provider:      constants.ProviderGoogle,
			expectError:   true,
			errorContains: "-api-key-file is required when -provider is 'google'",
		},
		{
			name:        "Argo provider with user",
			provider:    constants.ProviderArgo,
			argoUser:    "testuser",
			expectError: false,
		},
		{
			name:          "Argo provider without user",
			provider:      constants.ProviderArgo,
			expectError:   true,
			errorContains: "-argo-user or -api-key-file is required when -provider is 'argo'",
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
				GoogleAPIKey: tt.googleKey,
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

func TestConfigValidateArgoTestEnvironment(t *testing.T) {
	config := &Config{
		Provider: constants.ProviderArgo,
		ArgoUser: "testuser",
		ArgoTest: true,
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if config.ArgoEnv != "test" {
		t.Fatalf("ArgoEnv = %q, want %q", config.ArgoEnv, "test")
	}
}

func TestConfigValidateArgoDevAndTestConflict(t *testing.T) {
	config := &Config{
		Provider: constants.ProviderArgo,
		ArgoUser: "testuser",
		ArgoDev:  true,
		ArgoTest: true,
	}

	err := config.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "-argo-dev and -argo-test cannot be used together") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIProviderURLNormalization(t *testing.T) {
	tests := []struct {
		name        string
		providerURL string
		expectedURL string
	}{
		{
			name:        "Base URL without trailing slash",
			providerURL: "http://localhost:11434",
			expectedURL: "http://localhost:11434/chat/completions",
		},
		{
			name:        "Base URL with trailing slash",
			providerURL: "http://localhost:11434/",
			expectedURL: "http://localhost:11434/chat/completions",
		},
		{
			name:        "URL ending with /v1",
			providerURL: "http://localhost:11434/v1",
			expectedURL: "http://localhost:11434/v1/chat/completions",
		},
		{
			name:        "URL ending with /v1/",
			providerURL: "http://localhost:11434/v1/",
			expectedURL: "http://localhost:11434/v1/chat/completions",
		},
		{
			name:        "URL with custom path",
			providerURL: "http://localhost:11434/custom/endpoint",
			expectedURL: "http://localhost:11434/custom/endpoint/chat/completions",
		},
		{
			name:        "URL already with chat/completions",
			providerURL: "http://localhost:11434/v1/chat/completions",
			expectedURL: "http://localhost:11434/v1/chat/completions",
		},
		{
			name:        "HTTPS URL ending with /v1",
			providerURL: "https://api.custom.com/v1",
			expectedURL: "https://api.custom.com/v1/chat/completions",
		},
		{
			name:        "URL with multiple trailing slashes",
			providerURL: "http://localhost:11434///",
			expectedURL: "http://localhost:11434/chat/completions",
		},
		{
			name:        "URL with path ending in /v1 but not base URL",
			providerURL: "https://api.custom.com/api/v1",
			expectedURL: "https://api.custom.com/api/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Provider:    constants.ProviderOpenAI,
				ProviderURL: tt.providerURL,
			}

			endpoints, err := NewEndpoints(config)
			if err != nil {
				t.Fatalf("Failed to create endpoints: %v", err)
			}

			if endpoints.OpenAI != tt.expectedURL {
				t.Errorf("Expected OpenAI=%s, got %s", tt.expectedURL, endpoints.OpenAI)
			}
		})
	}
}

func TestNewEndpointsErrorHandling(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		providerURL   string
		expectError   bool
		errorContains string
	}{
		{
			name:        "Valid OpenAI provider URL",
			provider:    constants.ProviderOpenAI,
			providerURL: "https://api.example.com",
			expectError: false,
		},
		{
			name:          "Invalid OpenAI provider URL",
			provider:      constants.ProviderOpenAI,
			providerURL:   "://invalid-url",
			expectError:   true,
			errorContains: "invalid openai ProviderURL",
		},
		{
			name:        "Valid Google provider URL",
			provider:    constants.ProviderGoogle,
			providerURL: "https://api.example.com",
			expectError: false,
		},
		{
			name:          "Invalid Google provider URL",
			provider:      constants.ProviderGoogle,
			providerURL:   "not-a-url\x00",
			expectError:   true,
			errorContains: "invalid google ProviderURL",
		},
		{
			name:        "Valid Anthropic provider URL",
			provider:    constants.ProviderAnthropic,
			providerURL: "https://api.example.com",
			expectError: false,
		},
		{
			name:        "Valid Argo provider URL",
			provider:    constants.ProviderArgo,
			providerURL: "https://api.example.com",
			expectError: false,
		},
		{
			name:          "Invalid URL with null bytes",
			provider:      constants.ProviderOpenAI,
			providerURL:   "https://example.com\x00/path",
			expectError:   true,
			errorContains: "invalid openai ProviderURL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Provider:    tt.provider,
				ProviderURL: tt.providerURL,
			}

			_, err := NewEndpoints(config)
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
