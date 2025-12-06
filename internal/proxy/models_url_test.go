package proxy

import (
	"encoding/json"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestModelsEndpointURLConstruction verifies that each provider constructs
// the correct URL for the /v1/models endpoint.
// This test was added to catch the Argo URL construction bug where
// /argoapi/api/v1/resource was incorrectly becoming /argoapi/api/v1/resource/api/v1/models/
// instead of /argoapi/api/v1/models/
func TestModelsEndpointURLConstruction(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		providerURL  string // Custom provider URL path
		argoEnv      string // Optional: Argo environment
		expectedPath string // Expected path in the request to mock server
		mockResponse string // Response from mock server
		expectModels int    // Expected number of models in response
	}{
		// OpenAI tests
		{
			name:         "openai_default",
			provider:     constants.ProviderOpenAI,
			providerURL:  "/v1", // Base URL, models will be appended
			expectedPath: "/v1/models",
			mockResponse: `{"object":"list","data":[{"id":"gpt-4","object":"model"}]}`,
			expectModels: 1,
		},
		{
			name:         "openai_with_custom_path",
			provider:     constants.ProviderOpenAI,
			providerURL:  "/custom/path/v1",
			expectedPath: "/custom/path/v1/models",
			mockResponse: `{"object":"list","data":[{"id":"gpt-4","object":"model"},{"id":"gpt-3.5-turbo","object":"model"}]}`,
			expectModels: 2,
		},

		// Anthropic tests
		{
			name:         "anthropic_default",
			provider:     constants.ProviderAnthropic,
			providerURL:  "/v1", // Base URL, models will be appended
			expectedPath: "/v1/models",
			mockResponse: `{"data":[{"id":"claude-3-opus"}]}`,
			expectModels: 1,
		},

		// Google tests - Google appends v1beta/models to the base URL
		{
			name:         "google_default",
			provider:     constants.ProviderGoogle,
			providerURL:  "/", // Base URL, v1beta/models will be appended
			expectedPath: "/v1beta/models",
			mockResponse: `{"models":[{"name":"models/gemini-pro"}]}`,
			expectModels: 1,
		},

		// Argo tests - these are critical for the URL construction bug
		{
			name:         "argo_with_resource_path",
			provider:     constants.ProviderArgo,
			providerURL:  "/argoapi/api/v1/resource",
			expectedPath: "/argoapi/api/v1/models/",
			mockResponse: `["gpt-4", "claude-3-opus"]`,
			expectModels: 2,
		},
		{
			name:         "argo_with_chat_completions_path",
			provider:     constants.ProviderArgo,
			providerURL:  "/argoapi/api/v1/chat/completions",
			expectedPath: "/argoapi/api/v1/models/",
			mockResponse: `["model1", "model2", "model3"]`,
			expectModels: 3,
		},
		{
			name:         "argo_with_trailing_slash",
			provider:     constants.ProviderArgo,
			providerURL:  "/argoapi/api/v1/resource/",
			expectedPath: "/argoapi/api/v1/models/",
			mockResponse: `{"models":["test-model"]}`,
			expectModels: 1,
		},
		{
			name:         "argo_provider_url_with_resource",
			provider:     constants.ProviderArgo,
			providerURL:  "/argoapi/api/v1/resource",
			expectedPath: "/argoapi/api/v1/models/",
			mockResponse: `["model-a"]`,
			expectModels: 1,
		},
		{
			name:         "argo_with_custom_prefix",
			provider:     constants.ProviderArgo,
			providerURL:  "/custom/prefix/api/v1/resource",
			expectedPath: "/custom/prefix/api/v1/models/",
			mockResponse: `["custom-model"]`,
			expectModels: 1,
		},
		{
			name:         "argo_base_url_ends_with_api_v1",
			provider:     constants.ProviderArgo,
			providerURL:  "/argoapi/api/v1",
			expectedPath: "/argoapi/api/v1/models/",
			mockResponse: `["model1"]`,
			expectModels: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Track the actual path that was requested
			var actualPath string

			// Create mock server that records the path and returns mock response
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actualPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.mockResponse))
			}))
			defer mockServer.Close()

			// Build provider URL with mock server
			providerURL := mockServer.URL + tt.providerURL

			// Create config
			config := &Config{
				Provider:           tt.provider,
				ProviderURL:        providerURL,
				ArgoEnv:            tt.argoEnv,
				ArgoUser:           "testuser", // Required for Argo
				MaxRequestBodySize: 1024 * 1024,
			}

			// Set API keys for providers that need them
			switch tt.provider {
			case constants.ProviderOpenAI:
				config.OpenAIAPIKey = "test-key"
			case constants.ProviderAnthropic:
				config.AnthropicAPIKey = "test-key"
			case constants.ProviderGoogle:
				config.GoogleAPIKey = "test-key"
			}

			// Create server (NewEndpoints is called internally)
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)

			// Make request to /v1/models
			req := httptest.NewRequest("GET", "/v1/models", nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			// Check status code
			if w.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d. Response: %s", w.Code, w.Body.String())
				return
			}

			// Verify the correct path was requested
			if actualPath != tt.expectedPath {
				t.Errorf("URL path mismatch:\n  got:  %s\n  want: %s", actualPath, tt.expectedPath)
			}

			// Parse response and verify model count
			var response struct {
				Object string      `json:"object"`
				Data   []ModelItem `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to parse response: %v", err)
			}

			if len(response.Data) != tt.expectModels {
				t.Errorf("Expected %d models, got %d", tt.expectModels, len(response.Data))
			}
		})
	}
}

// TestArgoModelsURLConstruction specifically tests the Argo models URL construction
// to ensure the URL is correctly constructed for various input configurations.
// This is a regression test for the bug where /api/v1/resource was becoming
// /api/v1/resource/api/v1/models/ instead of /api/v1/models/
func TestArgoModelsURLConstruction(t *testing.T) {
	tests := []struct {
		name        string
		providerURL string
		argoEnv     string
		expectedURL string
	}{
		// Bug case: resource path should be replaced with models
		{
			name:        "resource_path_replaced_with_models",
			providerURL: "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource",
			expectedURL: "https://apps-dev.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:        "chat_completions_path_replaced_with_models",
			providerURL: "https://apps.inside.anl.gov/argoapi/api/v1/chat/completions",
			expectedURL: "https://apps.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:        "embeddings_path_replaced_with_models",
			providerURL: "https://apps.inside.anl.gov/argoapi/api/v1/embeddings",
			expectedURL: "https://apps.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:        "stream_chat_path_replaced_with_models",
			providerURL: "https://apps.inside.anl.gov/argoapi/api/v1/stream_chat",
			expectedURL: "https://apps.inside.anl.gov/argoapi/api/v1/models/",
		},
		// Environment fallback
		{
			name:        "env_prod_fallback",
			argoEnv:     "prod",
			expectedURL: "https://apps.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:        "env_dev_fallback",
			argoEnv:     "dev",
			expectedURL: "https://apps-dev.inside.anl.gov/argoapi/api/v1/models/",
		},
		{
			name:        "env_empty_defaults_to_dev",
			argoEnv:     "",
			expectedURL: "https://apps-dev.inside.anl.gov/argoapi/api/v1/models/",
		},
		// Trailing slash handling
		{
			name:        "trailing_slash_preserved",
			providerURL: "https://host/argoapi/api/v1/resource/",
			expectedURL: "https://host/argoapi/api/v1/models/",
		},
		// Already at models path
		{
			name:        "already_at_models",
			providerURL: "https://host/argoapi/api/v1/models",
			expectedURL: "https://host/argoapi/api/v1/models/",
		},
		{
			name:        "already_at_models_with_slash",
			providerURL: "https://host/argoapi/api/v1/models/",
			expectedURL: "https://host/argoapi/api/v1/models/",
		},
		// Nested path with api/v1
		{
			name:        "nested_api_v1_in_path",
			providerURL: "https://host/prefix/api/v1/nested/api/v1/resource",
			expectedURL: "https://host/prefix/api/v1/models/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Provider:    constants.ProviderArgo,
				ProviderURL: tt.providerURL,
				ArgoEnv:     tt.argoEnv,
				ArgoUser:    "testuser", // Required for Argo
			}

			endpoints, err := NewEndpoints(config)
			if err != nil {
				t.Fatalf("NewEndpoints failed: %v", err)
			}

			if endpoints.ArgoModels != tt.expectedURL {
				t.Errorf("URL mismatch:\n  got:  %s\n  want: %s", endpoints.ArgoModels, tt.expectedURL)
			}

			// All Argo URLs must end with trailing slash
			if !strings.HasSuffix(endpoints.ArgoModels, "/") {
				t.Errorf("Argo models URL must end with trailing slash: %s", endpoints.ArgoModels)
			}
		})
	}
}

// TestModelsEndpointIntegration tests the full flow from HTTP request to response
// for all providers, ensuring the models endpoint works end-to-end.
func TestModelsEndpointIntegration(t *testing.T) {
	providers := []struct {
		name         string
		provider     string
		mockResponse string
		setupConfig  func(*Config, string)
	}{
		{
			name:         "openai",
			provider:     constants.ProviderOpenAI,
			mockResponse: `{"object":"list","data":[{"id":"gpt-4","object":"model","created":1234,"owned_by":"openai"}]}`,
			setupConfig: func(c *Config, url string) {
				c.ProviderURL = url + "/v1"
				c.OpenAIAPIKey = "test-key"
			},
		},
		{
			name:         "anthropic",
			provider:     constants.ProviderAnthropic,
			mockResponse: `{"data":[{"id":"claude-3-opus","display_name":"Claude 3 Opus"}]}`,
			setupConfig: func(c *Config, url string) {
				c.ProviderURL = url + "/v1"
				c.AnthropicAPIKey = "test-key"
			},
		},
		{
			name:         "google",
			provider:     constants.ProviderGoogle,
			mockResponse: `{"models":[{"name":"models/gemini-pro","displayName":"Gemini Pro"}]}`,
			setupConfig: func(c *Config, url string) {
				// Google appends v1beta/models to the base URL
				c.ProviderURL = url
				c.GoogleAPIKey = "test-key"
			},
		},
		{
			name:         "argo",
			provider:     constants.ProviderArgo,
			mockResponse: `["gpt-4", "claude-3-opus", "gemini-pro"]`,
			setupConfig: func(c *Config, url string) {
				c.ProviderURL = url + "/argoapi/api/v1/resource"
				c.ArgoUser = "testuser"
			},
		},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			// Create mock server
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify it's a GET request
				if r.Method != "GET" {
					t.Errorf("Expected GET request, got %s", r.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(p.mockResponse))
			}))
			defer mockServer.Close()

			// Create config
			config := &Config{
				Provider:           p.provider,
				MaxRequestBodySize: 1024 * 1024,
			}
			p.setupConfig(config, mockServer.URL)

			// Create server (NewEndpoints is called internally)
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)

			// Make request
			req := httptest.NewRequest("GET", "/v1/models", nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			// Check response
			if w.Code != http.StatusOK {
				t.Fatalf("Expected status 200, got %d. Response: %s", w.Code, w.Body.String())
			}

			// Parse response
			var response struct {
				Object string      `json:"object"`
				Data   []ModelItem `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("Failed to parse response: %v", err)
			}

			// Verify response format
			if response.Object != "list" {
				t.Errorf("Expected object 'list', got %q", response.Object)
			}

			if len(response.Data) == 0 {
				t.Error("Expected at least one model in response")
			}

			// Each model should have required fields
			for _, model := range response.Data {
				if model.ID == "" {
					t.Error("Model ID should not be empty")
				}
				if model.Object != "model" {
					t.Errorf("Model object should be 'model', got %q", model.Object)
				}
			}
		})
	}
}
