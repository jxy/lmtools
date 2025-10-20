package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestModelsEndpointErrorPropagation tests that provider errors are properly propagated
func TestModelsEndpointErrorPropagation(t *testing.T) {
	tests := []struct {
		name           string
		mockStatusCode int
		mockResponse   string
		expectedCode   int
		expectedError  string
	}{
		{
			name:           "401 Unauthorized",
			mockStatusCode: http.StatusUnauthorized,
			mockResponse:   `{"error": "Invalid API key"}`,
			expectedCode:   http.StatusBadGateway,
			expectedError:  "HTTP 401",
		},
		{
			name:           "403 Forbidden",
			mockStatusCode: http.StatusForbidden,
			mockResponse:   `{"error": "Access denied"}`,
			expectedCode:   http.StatusBadGateway,
			expectedError:  "HTTP 403",
		},
		{
			name:           "500 Internal Server Error",
			mockStatusCode: http.StatusInternalServerError,
			mockResponse:   `{"error": "Internal server error"}`,
			expectedCode:   http.StatusBadGateway,
			expectedError:  "HTTP 500",
		},
		{
			name:           "404 Not Found",
			mockStatusCode: http.StatusNotFound,
			mockResponse:   `{"error": "Endpoint not found"}`,
			expectedCode:   http.StatusBadGateway,
			expectedError:  "HTTP 404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server that returns an error
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.mockStatusCode)
				_, _ = w.Write([]byte(tt.mockResponse))
			}))
			defer mockServer.Close()

			// Create proxy config
			config := &Config{
				Provider:           "openai",
				ProviderURL:        mockServer.URL + "/v1",
				MaxRequestBodySize: 100,
			}

			// Create proxy server with reduced retry delays for faster testing
			server := NewServerForErrorTests(config)

			// Create test request
			req := httptest.NewRequest("GET", "/v1/models", nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			// Check that we got an error response
			if w.Code != tt.expectedCode {
				t.Errorf("Expected status %d, got %d", tt.expectedCode, w.Code)
			}

			// Parse error response
			var errorResp map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &errorResp); err != nil {
				t.Fatalf("Failed to parse error response: %v", err)
			}

			// Check error message contains expected text
			if errorObj, ok := errorResp["error"].(map[string]interface{}); ok {
				if message, ok := errorObj["message"].(string); ok {
					if !strings.Contains(message, tt.expectedError) {
						t.Errorf("Expected error message to contain %q, got %q", tt.expectedError, message)
					}
				} else {
					t.Error("Error response missing message field")
				}
			} else {
				t.Error("Response missing error field")
			}
		})
	}
}

// TestParseArgoModels tests the Argo models parsing function
func TestParseArgoModels(t *testing.T) {
	server := &Server{config: &Config{}}

	tests := []struct {
		name          string
		input         string
		expectedCount int
		expectedIDs   []string
		expectError   bool
	}{
		{
			name:          "Array format",
			input:         `["gpt-4", "gpt-3.5-turbo", "claude-3-opus"]`,
			expectedCount: 3,
			expectedIDs:   []string{"gpt-4", "gpt-3.5-turbo", "claude-3-opus"},
			expectError:   false,
		},
		{
			name:          "Object with models field",
			input:         `{"models": ["model1", "model2"]}`,
			expectedCount: 2,
			expectedIDs:   []string{"model1", "model2"},
			expectError:   false,
		},
		{
			name: "Object with data field",
			input: `{
				"data": [
					{"id": "model-a", "name": "Model A"},
					{"id": "model-b", "name": "Model B"}
				]
			}`,
			expectedCount: 2,
			expectedIDs:   []string{"model-a", "model-b"},
			expectError:   false,
		},
		{
			name: "Object with models field containing objects",
			input: `{
				"models": [
					{"id": "model-x", "name": "Model X"},
					{"id": "model-y", "name": "Model Y"}
				]
			}`,
			expectedCount: 2,
			expectedIDs:   []string{"model-x", "model-y"},
			expectError:   false,
		},
		{
			name:        "Invalid JSON",
			input:       `{invalid json}`,
			expectError: true,
		},
		{
			name:        "Empty response",
			input:       `{}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models, err := server.parseArgoModels([]byte(tt.input))

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(models) != tt.expectedCount {
				t.Errorf("Expected %d models, got %d", tt.expectedCount, len(models))
			}

			// Check model IDs
			for i, expectedID := range tt.expectedIDs {
				if i >= len(models) {
					break
				}
				if models[i].ID != expectedID {
					t.Errorf("Expected model ID %q at index %d, got %q", expectedID, i, models[i].ID)
				}
				if models[i].Object != "model" {
					t.Errorf("Expected object type 'model', got %q", models[i].Object)
				}
				if models[i].OwnedBy != "argo" {
					t.Errorf("Expected owned_by 'argo', got %q", models[i].OwnedBy)
				}
			}
		})
	}
}

// TestParseOpenAIModels tests the OpenAI models parsing function
func TestParseOpenAIModels(t *testing.T) {
	server := &Server{config: &Config{}}

	tests := []struct {
		name          string
		input         string
		expectedCount int
		expectedIDs   []string
		expectError   bool
	}{
		{
			name: "Standard OpenAI format",
			input: `{
				"object": "list",
				"data": [
					{"id": "gpt-4", "object": "model", "created": 1234567890, "owned_by": "openai"},
					{"id": "gpt-3.5-turbo", "object": "model", "created": 1234567890, "owned_by": "openai"}
				]
			}`,
			expectedCount: 2,
			expectedIDs:   []string{"gpt-4", "gpt-3.5-turbo"},
			expectError:   false,
		},
		{
			name:        "Invalid JSON",
			input:       `{invalid}`,
			expectError: true,
		},
		{
			name:          "Missing data field",
			input:         `{"object": "list"}`,
			expectedCount: 0,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models, err := server.parseOpenAIModels([]byte(tt.input))

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(models) != tt.expectedCount {
				t.Errorf("Expected %d models, got %d", tt.expectedCount, len(models))
			}

			// Check model IDs
			for i, expectedID := range tt.expectedIDs {
				if i >= len(models) {
					break
				}
				if models[i].ID != expectedID {
					t.Errorf("Expected model ID %q at index %d, got %q", expectedID, i, models[i].ID)
				}
			}
		})
	}
}

// TestParseGoogleModels tests the Google models parsing function
func TestParseGoogleModels(t *testing.T) {
	server := &Server{config: &Config{}}

	tests := []struct {
		name          string
		input         string
		expectedCount int
		expectedIDs   []string
		expectError   bool
	}{
		{
			name: "Google format with models/ prefix",
			input: `{
				"models": [
					{"name": "models/gemini-pro", "displayName": "Gemini Pro"},
					{"name": "models/gemini-pro-vision", "displayName": "Gemini Pro Vision"}
				]
			}`,
			expectedCount: 2,
			expectedIDs:   []string{"gemini-pro", "gemini-pro-vision"},
			expectError:   false,
		},
		{
			name: "Google format without prefix",
			input: `{
				"models": [
					{"name": "gemini-1.5-pro", "displayName": "Gemini 1.5 Pro"}
				]
			}`,
			expectedCount: 1,
			expectedIDs:   []string{"gemini-1.5-pro"},
			expectError:   false,
		},
		{
			name:        "Invalid JSON",
			input:       `{invalid}`,
			expectError: true,
		},
		{
			name:          "Empty models list",
			input:         `{"models": []}`,
			expectedCount: 0,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models, err := server.parseGoogleModels([]byte(tt.input))

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(models) != tt.expectedCount {
				t.Errorf("Expected %d models, got %d", tt.expectedCount, len(models))
			}

			// Check model IDs
			for i, expectedID := range tt.expectedIDs {
				if i >= len(models) {
					break
				}
				if models[i].ID != expectedID {
					t.Errorf("Expected model ID %q at index %d, got %q", expectedID, i, models[i].ID)
				}
				if models[i].OwnedBy != "google" {
					t.Errorf("Expected owned_by 'google', got %q", models[i].OwnedBy)
				}
			}
		})
	}
}

// TestParseAnthropicModels tests the Anthropic models parsing function
func TestParseAnthropicModels(t *testing.T) {
	server := &Server{config: &Config{}}

	tests := []struct {
		name          string
		input         string
		expectedCount int
		expectedIDs   []string
		expectError   bool
	}{
		{
			name: "Anthropic format with models field",
			input: `{
				"models": [
					{"id": "claude-3-opus", "display_name": "Claude 3 Opus"},
					{"id": "claude-3-sonnet", "display_name": "Claude 3 Sonnet"}
				]
			}`,
			expectedCount: 2,
			expectedIDs:   []string{"claude-3-opus", "claude-3-sonnet"},
			expectError:   false,
		},
		{
			name: "Anthropic format with data field",
			input: `{
				"data": [
					{"id": "claude-3-haiku", "display_name": "Claude 3 Haiku"}
				]
			}`,
			expectedCount: 1,
			expectedIDs:   []string{"claude-3-haiku"},
			expectError:   false,
		},
		{
			name:        "Invalid JSON",
			input:       `{invalid}`,
			expectError: true,
		},
		{
			name:        "Empty response",
			input:       `{}`,
			expectError: true,
		},
		{
			name:        "Empty models and data",
			input:       `{"models": [], "data": []}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models, err := server.parseAnthropicModels([]byte(tt.input))

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(models) != tt.expectedCount {
				t.Errorf("Expected %d models, got %d", tt.expectedCount, len(models))
			}

			// Check model IDs
			for i, expectedID := range tt.expectedIDs {
				if i >= len(models) {
					break
				}
				if models[i].ID != expectedID {
					t.Errorf("Expected model ID %q at index %d, got %q", expectedID, i, models[i].ID)
				}
				if models[i].OwnedBy != "anthropic" {
					t.Errorf("Expected owned_by 'anthropic', got %q", models[i].OwnedBy)
				}
			}
		})
	}
}

// TestModelsEndpointAuthentication tests that proper authentication headers are sent
func TestModelsEndpointAuthentication(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		apiKey         string
		expectedHeader string
		expectedValue  string
		checkURL       bool
	}{
		{
			name:           "OpenAI Bearer Token",
			provider:       "openai",
			apiKey:         "sk-test123",
			expectedHeader: "Authorization",
			expectedValue:  "Bearer sk-test123",
		},
		{
			name:           "Anthropic API Key",
			provider:       "anthropic",
			apiKey:         "sk-ant-test456",
			expectedHeader: "X-Api-Key",
			expectedValue:  "sk-ant-test456",
		},
		{
			name:     "Google API Key in URL",
			provider: "google",
			apiKey:   "AIzaTest789",
			checkURL: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server that checks headers
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.checkURL {
					// For Google, check API key in URL
					if key := r.URL.Query().Get("key"); key != tt.apiKey {
						t.Errorf("Expected API key %q in URL, got %q", tt.apiKey, key)
					}
				} else {
					// For others, check header
					if value := r.Header.Get(tt.expectedHeader); value != tt.expectedValue {
						t.Errorf("Expected header %s=%q, got %q", tt.expectedHeader, tt.expectedValue, value)
					}
				}

				// Return valid models response
				response := map[string]interface{}{
					"object": "list",
					"data":   []interface{}{},
				}
				// For Anthropic, return models in their format
				if tt.provider == "anthropic" {
					response = map[string]interface{}{
						"models": []map[string]interface{}{
							{"id": "claude-3-opus", "display_name": "Claude 3 Opus"},
						},
					}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer mockServer.Close()

			// Create proxy config
			config := &Config{
				Provider:           tt.provider,
				ProviderURL:        mockServer.URL + "/v1",
				MaxRequestBodySize: 100,
			}

			// Set API key based on provider
			switch tt.provider {
			case "openai":
				config.OpenAIAPIKey = tt.apiKey
			case "anthropic":
				config.AnthropicAPIKey = tt.apiKey
			case "google":
				config.GoogleAPIKey = tt.apiKey
			}

			// Create proxy server
			server := NewServer(config)

			// Create test request
			req := httptest.NewRequest("GET", "/v1/models", nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			// Check response is successful
			if w.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", w.Code)
				t.Logf("Response: %s", w.Body.String())
			}
		})
	}
}

// TestModelsEndpointMalformedResponse tests handling of malformed provider responses
func TestModelsEndpointMalformedResponse(t *testing.T) {
	tests := []struct {
		name         string
		mockResponse string
		expectError  bool
	}{
		{
			name:         "Invalid JSON",
			mockResponse: `{this is not valid json}`,
			expectError:  true,
		},
		{
			name:         "HTML response",
			mockResponse: `<html><body>Error</body></html>`,
			expectError:  true,
		},
		{
			name:         "Empty response",
			mockResponse: ``,
			expectError:  true,
		},
		{
			name:         "Null response",
			mockResponse: `null`,
			expectError:  false, // null parses as valid JSON with empty data
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server that returns malformed response
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.mockResponse))
			}))
			defer mockServer.Close()

			// Create proxy config
			config := &Config{
				Provider:           "openai",
				ProviderURL:        mockServer.URL + "/v1",
				MaxRequestBodySize: 100,
			}

			// Create proxy server
			server := NewServer(config)

			// Create test request
			req := httptest.NewRequest("GET", "/v1/models", nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			if tt.expectError {
				if w.Code == http.StatusOK {
					t.Error("Expected error but got success")
				}
			} else {
				if w.Code != http.StatusOK {
					t.Errorf("Expected success but got status %d", w.Code)
				}
			}
		})
	}
}
