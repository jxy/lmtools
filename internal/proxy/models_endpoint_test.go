package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestModelsEndpointErrorPropagation tests that provider errors are properly propagated
// The proxy passes through the original error status codes from the upstream provider
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
			expectedCode:   http.StatusUnauthorized,
			expectedError:  "Upstream openai error",
		},
		{
			name:           "403 Forbidden",
			mockStatusCode: http.StatusForbidden,
			mockResponse:   `{"error": "Access denied"}`,
			expectedCode:   http.StatusForbidden,
			expectedError:  "Upstream openai error",
		},
		{
			name:           "500 Internal Server Error",
			mockStatusCode: http.StatusInternalServerError,
			mockResponse:   `{"error": "Internal server error"}`,
			expectedCode:   http.StatusInternalServerError,
			expectedError:  "Upstream openai error",
		},
		{
			name:           "404 Not Found",
			mockStatusCode: http.StatusNotFound,
			mockResponse:   `{"error": "Endpoint not found"}`,
			expectedCode:   http.StatusNotFound,
			expectedError:  "Upstream openai error",
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
				Provider:           constants.ProviderOpenAI,
				ProviderURL:        mockServer.URL + "/v1",
				MaxRequestBodySize: 100,
			}

			// Create proxy server with reduced retry delays for faster testing
			server := NewTestServerWithFastRetries(t, config)

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
	server := NewMinimalTestServer(t, &Config{})

	tests := []struct {
		name           string
		input          string
		expectedCount  int
		expectedIDs    []string
		expectedOwner  string
		expectedOwners []string
		expectError    bool
	}{
		{
			name:          "Array format",
			input:         `["gpt-4", "gpt-3.5-turbo", "claude-3-opus"]`,
			expectedCount: 3,
			expectedIDs:   []string{"gpt-4", "gpt-3.5-turbo", "claude-3-opus"},
			expectedOwner: "argo",
			expectError:   false,
		},
		{
			name:          "Object with models field",
			input:         `{"models": ["model1", "model2"]}`,
			expectedCount: 2,
			expectedIDs:   []string{"model1", "model2"},
			expectedOwner: "argo",
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
			expectedOwner: "argo",
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
			expectedOwner: "argo",
			expectError:   false,
		},
		{
			name: "Live Argo format with internal_id and upstream owner",
			input: `{
				"data": [
					{"id": "GPT-5-mini", "internal_id": "gpt5mini", "owned_by": "openai"},
					{"id": "Claude Haiku 4.5", "internal_id": "claudehaiku45", "owned_by": "anthropic"}
				]
			}`,
			expectedCount:  2,
			expectedIDs:    []string{"gpt5mini", "claudehaiku45"},
			expectedOwners: []string{"openai", "anthropic"},
			expectError:    false,
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
				wantOwner := tt.expectedOwner
				if i < len(tt.expectedOwners) {
					wantOwner = tt.expectedOwners[i]
				}
				if models[i].OwnedBy != wantOwner {
					t.Errorf("Expected owned_by %q, got %q", wantOwner, models[i].OwnedBy)
				}
			}
		})
	}
}

// TestParseOpenAIModels tests the OpenAI models parsing function
func TestParseOpenAIModels(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{})

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
	server := NewMinimalTestServer(t, &Config{})

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
	server := NewMinimalTestServer(t, &Config{})

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
			provider:       constants.ProviderOpenAI,
			apiKey:         "sk-test123",
			expectedHeader: "Authorization",
			expectedValue:  "Bearer sk-test123",
		},
		{
			name:           "Anthropic API Key",
			provider:       constants.ProviderAnthropic,
			apiKey:         "sk-ant-test456",
			expectedHeader: "X-Api-Key",
			expectedValue:  "sk-ant-test456",
		},
		{
			name:           "Google API Key in Header",
			provider:       constants.ProviderGoogle,
			apiKey:         "AIzaTest789",
			checkURL:       false,
			expectedHeader: "x-goog-api-key",
			expectedValue:  "AIzaTest789",
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
				if tt.provider == constants.ProviderAnthropic {
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
			case constants.ProviderOpenAI:
				config.OpenAIAPIKey = tt.apiKey
			case constants.ProviderAnthropic:
				config.AnthropicAPIKey = tt.apiKey
			case constants.ProviderGoogle:
				config.GoogleAPIKey = tt.apiKey
			}

			// Create proxy server (NewEndpoints is called internally)
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)

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
				Provider:           constants.ProviderOpenAI,
				ProviderURL:        mockServer.URL + "/v1",
				MaxRequestBodySize: 100,
			}

			// Create proxy server (NewEndpoints is called internally)
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)

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

func TestModelsEndpointFetchesAnthropicPagination(t *testing.T) {
	requests := make([]string, 0, 2)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, want 1000", got)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = w.Write([]byte(`{
				"data": [{
					"id": "claude-first",
					"display_name": "Claude First",
					"created_at": "2026-01-02T00:00:00Z",
					"max_input_tokens": 1000,
					"max_tokens": 2000,
					"type": "model",
					"capabilities": {"thinking": {"supported": true}}
				}],
				"first_id": "claude-first",
				"has_more": true,
				"last_id": "claude-first"
			}`))
		case "claude-first":
			_, _ = w.Write([]byte(`{
				"data": [{
					"id": "claude-second",
					"display_name": "Claude Second",
					"created_at": "2026-01-03T00:00:00Z",
					"max_input_tokens": 3000,
					"max_tokens": 4000,
					"type": "model"
				}],
				"first_id": "claude-second",
				"has_more": false,
				"last_id": "claude-second"
			}`))
		default:
			t.Fatalf("unexpected after_id %q", r.URL.Query().Get("after_id"))
		}
	}))
	defer mockServer.Close()

	config := &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        mockServer.URL + "/v1",
		AnthropicAPIKey:    "sk-ant-test",
		MaxRequestBodySize: 1024 * 1024,
	}
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var response struct {
		Object string      `json:"object"`
		Data   []ModelItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("models = %d, want 2: %#v", len(response.Data), response.Data)
	}
	if response.Data[0].ID != "claude-first" || response.Data[1].ID != "claude-second" {
		t.Fatalf("model ids = %#v", response.Data)
	}
	if response.Data[0].Created == 0 || response.Data[0].CreatedAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("first model creation metadata not preserved: %#v", response.Data[0])
	}
	if response.Data[0].MaxInputTokens != 1000 || response.Data[0].MaxOutputTokens != 2000 {
		t.Fatalf("first model token metadata not preserved: %#v", response.Data[0])
	}
	if response.Data[0].Capabilities["thinking"] == nil {
		t.Fatalf("first model capabilities not preserved: %#v", response.Data[0].Capabilities)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2: %v", len(requests), requests)
	}
}

func TestModelsEndpointFetchesGooglePagination(t *testing.T) {
	requests := make([]string, 0, 2)
	thinking := true
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if got := r.URL.Query().Get("pageSize"); got != "1000" {
			t.Errorf("pageSize = %q, want 1000", got)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("pageToken") {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"models": []map[string]interface{}{
					{
						"name":                       "models/gemini-first",
						"displayName":                "Gemini First",
						"description":                "first model",
						"inputTokenLimit":            1000,
						"outputTokenLimit":           2000,
						"supportedGenerationMethods": []string{"generateContent", "countTokens"},
						"thinking":                   thinking,
					},
				},
				"nextPageToken": "next-page",
			})
		case "next-page":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"models": []map[string]interface{}{
					{
						"name":             "models/gemini-second",
						"displayName":      "Gemini Second",
						"inputTokenLimit":  3000,
						"outputTokenLimit": 4000,
					},
				},
			})
		default:
			t.Fatalf("unexpected pageToken %q", r.URL.Query().Get("pageToken"))
		}
	}))
	defer mockServer.Close()

	config := &Config{
		Provider:           constants.ProviderGoogle,
		ProviderURL:        mockServer.URL,
		GoogleAPIKey:       "google-key",
		MaxRequestBodySize: 1024 * 1024,
	}
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var response struct {
		Object string      `json:"object"`
		Data   []ModelItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("models = %d, want 2: %#v", len(response.Data), response.Data)
	}
	if response.Data[0].ID != "gemini-first" || response.Data[1].ID != "gemini-second" {
		t.Fatalf("model ids = %#v", response.Data)
	}
	if response.Data[0].DisplayName != "Gemini First" {
		t.Fatalf("display_name = %q, want Gemini First", response.Data[0].DisplayName)
	}
	if response.Data[0].MaxInputTokens != 1000 || response.Data[0].MaxOutputTokens != 2000 {
		t.Fatalf("first model token metadata not preserved: %#v", response.Data[0])
	}
	if got := strings.Join(response.Data[0].SupportedGenerationMethods, ","); got != "generateContent,countTokens" {
		t.Fatalf("supported_generation_methods = %q", got)
	}
	if response.Data[0].Metadata["description"] != "first model" || response.Data[0].Metadata["thinking"] != true {
		t.Fatalf("google metadata not preserved: %#v", response.Data[0].Metadata)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2: %v", len(requests), requests)
	}
}

func TestModelsEndpointWarnsOnUnknownModelFields(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{})

	logs := captureWarnLogs(t, func() {
		_, err := server.parseGoogleModelsForProvider(context.Background(), []byte(`{
			"models": [{
				"name": "models/gemini-test",
				"displayName": "Gemini Test",
				"newModelField": true
			}],
			"newTopLevel": true
		}`))
		if err != nil {
			t.Fatalf("parseGoogleModelsForProvider() error = %v", err)
		}
	})

	for _, want := range []string{
		`Unknown JSON fields in Google models response (ignored):`,
		`models[].newModelField`,
		`newTopLevel`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}
