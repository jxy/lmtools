package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// mockRequestConfig implements RequestConfig for testing
type mockRequestConfig struct {
	user        string
	model       string
	system      string
	env         string
	embed       bool
	streamChat  bool
	provider    string
	providerURL string
	apiKeyFile  string
}

func (m mockRequestConfig) GetUser() string        { return m.user }
func (m mockRequestConfig) GetModel() string       { return m.model }
func (m mockRequestConfig) GetSystem() string      { return m.system }
func (m mockRequestConfig) GetEnv() string         { return m.env }
func (m mockRequestConfig) IsEmbed() bool          { return m.embed }
func (m mockRequestConfig) IsStreamChat() bool     { return m.streamChat }
func (m mockRequestConfig) GetProvider() string    { return m.provider }
func (m mockRequestConfig) GetProviderURL() string { return m.providerURL }
func (m mockRequestConfig) GetAPIKeyFile() string  { return m.apiKeyFile }

// mockSession implements Session for testing
type mockSession struct {
	path string
}

func (m mockSession) GetPath() string { return m.path }

// mockGetLineage returns test messages
func mockGetLineage(path string) ([]Message, error) {
	return []Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "How are you?"},
	}, nil
}

func TestBuildRequestWithSession_Providers(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		apiKeyFile     string
		expectedURL    string
		expectedFormat string // "argo", "openai", "google", "anthropic"
	}{
		{
			name:           "Argo provider",
			provider:       "argo",
			expectedURL:    "apps.inside.anl.gov/argoapi",
			expectedFormat: "argo",
		},
		{
			name:           "OpenAI provider",
			provider:       "openai",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "api.openai.com",
			expectedFormat: "openai",
		},
		{
			name:           "Google provider",
			provider:       "google",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "generativelanguage.googleapis.com",
			expectedFormat: "google",
		},
		{
			name:           "Anthropic provider",
			provider:       "anthropic",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "api.anthropic.com",
			expectedFormat: "anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip non-Argo tests if API key file doesn't exist
			if tt.provider != "argo" && tt.apiKeyFile != "" {
				t.Skip("Skipping test that requires API key file")
			}

			cfg := mockRequestConfig{
				user:       "testuser",
				model:      "",
				system:     "You are a helpful assistant",
				env:        "prod",
				provider:   tt.provider,
				apiKeyFile: tt.apiKeyFile,
			}

			sess := mockSession{path: "/test/session"}

			req, body, err := BuildRequestWithSession(cfg, sess, mockGetLineage)
			if err != nil {
				t.Fatalf("BuildRequestWithSession failed: %v", err)
			}

			// Check URL contains expected domain
			if !strings.Contains(req.URL.String(), tt.expectedURL) {
				t.Errorf("Expected URL to contain %s, got %s", tt.expectedURL, req.URL.String())
			}

			// Check request format
			var requestData map[string]interface{}
			if err := json.Unmarshal(body, &requestData); err != nil {
				t.Fatalf("Failed to unmarshal request body: %v", err)
			}

			switch tt.expectedFormat {
			case "argo":
				// Argo format should have "user" field
				if _, ok := requestData["user"]; !ok {
					t.Error("Argo request should have 'user' field")
				}
			case "openai":
				// OpenAI format should have "model" and "messages" at top level
				if _, ok := requestData["model"]; !ok {
					t.Error("OpenAI request should have 'model' field")
				}
				if _, ok := requestData["messages"]; !ok {
					t.Error("OpenAI request should have 'messages' field")
				}
				if _, ok := requestData["user"]; ok {
					t.Error("OpenAI request should not have 'user' field")
				}
			case "google":
				// Google format should have "contents" field
				if _, ok := requestData["contents"]; !ok {
					t.Error("Google request should have 'contents' field")
				}
				if _, ok := requestData["user"]; ok {
					t.Error("Google request should not have 'user' field")
				}
			case "anthropic":
				// Anthropic format should have "messages" and "system" at top level
				if _, ok := requestData["messages"]; !ok {
					t.Error("Anthropic request should have 'messages' field")
				}
				if _, ok := requestData["system"]; !ok {
					t.Error("Anthropic request should have 'system' field")
				}
				if _, ok := requestData["user"]; ok {
					t.Error("Anthropic request should not have 'user' field")
				}
			}
		})
	}
}

func TestBuildRegenerationRequest_Providers(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		apiKeyFile     string
		expectedURL    string
		expectedFormat string
	}{
		{
			name:           "Argo provider",
			provider:       "argo",
			expectedURL:    "apps.inside.anl.gov/argoapi",
			expectedFormat: "argo",
		},
		{
			name:           "OpenAI provider",
			provider:       "openai",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "api.openai.com",
			expectedFormat: "openai",
		},
		{
			name:           "Google provider",
			provider:       "google",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "generativelanguage.googleapis.com",
			expectedFormat: "google",
		},
		{
			name:           "Anthropic provider",
			provider:       "anthropic",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "api.anthropic.com",
			expectedFormat: "anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip non-Argo tests if API key file doesn't exist
			if tt.provider != "argo" && tt.apiKeyFile != "" {
				t.Skip("Skipping test that requires API key file")
			}

			cfg := mockRequestConfig{
				user:       "testuser",
				model:      "",
				system:     "You are a helpful assistant",
				env:        "prod",
				provider:   tt.provider,
				apiKeyFile: tt.apiKeyFile,
			}

			sess := mockSession{path: "/test/session"}

			req, body, err := BuildRegenerationRequest(cfg, sess, mockGetLineage)
			if err != nil {
				t.Fatalf("BuildRegenerationRequest failed: %v", err)
			}

			// Check URL contains expected domain
			if !strings.Contains(req.URL.String(), tt.expectedURL) {
				t.Errorf("Expected URL to contain %s, got %s", tt.expectedURL, req.URL.String())
			}

			// Check request format
			var requestData map[string]interface{}
			if err := json.Unmarshal(body, &requestData); err != nil {
				t.Fatalf("Failed to unmarshal request body: %v", err)
			}

			switch tt.expectedFormat {
			case "argo":
				if _, ok := requestData["user"]; !ok {
					t.Error("Argo request should have 'user' field")
				}
			case "openai":
				if _, ok := requestData["model"]; !ok {
					t.Error("OpenAI request should have 'model' field")
				}
				if _, ok := requestData["user"]; ok {
					t.Error("OpenAI request should not have 'user' field")
				}
			case "google":
				if _, ok := requestData["contents"]; !ok {
					t.Error("Google request should have 'contents' field")
				}
				if _, ok := requestData["user"]; ok {
					t.Error("Google request should not have 'user' field")
				}
			case "anthropic":
				if _, ok := requestData["messages"]; !ok {
					t.Error("Anthropic request should have 'messages' field")
				}
				if _, ok := requestData["system"]; !ok {
					t.Error("Anthropic request should have 'system' field")
				}
				if _, ok := requestData["user"]; ok {
					t.Error("Anthropic request should not have 'user' field")
				}
			}
		})
	}
}
