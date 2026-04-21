package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// mockGetMessagesWithTools returns test messages as TypedMessage
func mockGetMessagesWithTools(ctx context.Context, path string) ([]TypedMessage, error) {
	return []TypedMessage{
		NewTextMessage("user", "Hello"),
		NewTextMessage("assistant", "Hi there!"),
		NewTextMessage("user", "How are you?"),
	}, nil
}

func TestBuildRequestWithToolInteractions_Providers(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		model          string
		apiKeyFile     string
		expectedURL    string
		expectedFormat string // "openai", "google", "anthropic"
	}{
		{
			name:           "Argo provider defaults to native OpenAI format",
			provider:       "argo",
			model:          "gpt-4o-mini",
			expectedURL:    "apps.inside.anl.gov/argoapi",
			expectedFormat: "openai",
		},
		{
			name:           "Argo Claude model uses native Anthropic format",
			provider:       "argo",
			model:          "claude-3-5-sonnet",
			expectedURL:    "apps.inside.anl.gov/argoapi",
			expectedFormat: "anthropic",
		},
		{
			name:           "OpenAI provider",
			provider:       "openai",
			model:          "gpt-4o-mini",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "api.openai.com",
			expectedFormat: "openai",
		},
		{
			name:           "Google provider",
			provider:       "google",
			model:          "gemini-2.5-pro",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "generativelanguage.googleapis.com",
			expectedFormat: "google",
		},
		{
			name:           "Anthropic provider",
			provider:       "anthropic",
			model:          "claude-3-opus-20240229",
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

			cfg := &TestRequestConfig{
				User:       "testuser",
				Model:      tt.model,
				System:     "You are a helpful assistant",
				Env:        "prod",
				Provider:   tt.provider,
				APIKeyFile: tt.apiKeyFile,
			}

			sess := NewTestSession("/test/session")

			rb, err := BuildRequestWithToolInteractions(context.Background(), cfg, sess, mockGetMessagesWithTools)
			if err != nil {
				t.Fatalf("BuildRequestWithToolInteractions failed: %v", err)
			}

			// Check URL contains expected domain
			if !strings.Contains(rb.Request.URL.String(), tt.expectedURL) {
				t.Errorf("Expected URL to contain %s, got %s", tt.expectedURL, rb.Request.URL.String())
			}

			// Check request format
			var requestData map[string]interface{}
			if err := json.Unmarshal(rb.Body, &requestData); err != nil {
				t.Fatalf("Failed to unmarshal request body: %v", err)
			}

			switch tt.expectedFormat {
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

func TestBuildRequestWithToolInteractions_Regeneration(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		model          string
		apiKeyFile     string
		expectedURL    string
		expectedFormat string
	}{
		{
			name:           "Argo provider defaults to native OpenAI format",
			provider:       "argo",
			model:          "gpt-4o-mini",
			expectedURL:    "apps.inside.anl.gov/argoapi",
			expectedFormat: "openai",
		},
		{
			name:           "Argo Claude model uses native Anthropic format",
			provider:       "argo",
			model:          "claude-3-5-sonnet",
			expectedURL:    "apps.inside.anl.gov/argoapi",
			expectedFormat: "anthropic",
		},
		{
			name:           "OpenAI provider",
			provider:       "openai",
			model:          "gpt-4o-mini",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "api.openai.com",
			expectedFormat: "openai",
		},
		{
			name:           "Google provider",
			provider:       "google",
			model:          "gemini-2.5-pro",
			apiKeyFile:     "testdata/fake-key.txt",
			expectedURL:    "generativelanguage.googleapis.com",
			expectedFormat: "google",
		},
		{
			name:           "Anthropic provider",
			provider:       "anthropic",
			model:          "claude-3-opus-20240229",
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

			cfg := &TestRequestConfig{
				User:       "testuser",
				Model:      tt.model,
				System:     "You are a helpful assistant",
				Env:        "prod",
				Provider:   tt.provider,
				APIKeyFile: tt.apiKeyFile,
			}

			sess := NewTestSession("/test/session")

			rb, err := BuildRequestWithToolInteractions(context.Background(), cfg, sess, mockGetMessagesWithTools)
			if err != nil {
				t.Fatalf("BuildRequestWithToolInteractions failed: %v", err)
			}

			// Check URL contains expected domain
			if !strings.Contains(rb.Request.URL.String(), tt.expectedURL) {
				t.Errorf("Expected URL to contain %s, got %s", tt.expectedURL, rb.Request.URL.String())
			}

			// Check request format
			var requestData map[string]interface{}
			if err := json.Unmarshal(rb.Body, &requestData); err != nil {
				t.Fatalf("Failed to unmarshal request body: %v", err)
			}

			switch tt.expectedFormat {
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
