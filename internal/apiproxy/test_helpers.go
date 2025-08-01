package apiproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SetupTestServer creates a test server with mock providers
func SetupTestServer(t *testing.T) (*httptest.Server, *httptest.Server, *httptest.Server, *httptest.Server) {
	t.Helper()
	// Create mock providers
	openAIMock := httptest.NewServer(NewMockOpenAI(t))
	geminiMock := httptest.NewServer(NewMockGemini(t))
	argoMock := httptest.NewServer(NewMockArgo(t))

	// Create config
	config := &Config{
		OpenAIAPIKey:       "test-openai-key",
		GeminiAPIKey:       "test-gemini-key",
		ArgoUser:           "testuser",
		ArgoEnv:            "test",
		PreferredProvider:  "openai",
		SmallModel:         "gpt-4o-mini",
		BigModel:           "gpt-4o",
		OpenAIModels:       []string{"gpt-4o", "gpt-4o-mini"},
		GeminiModels:       []string{"gemini-2.0-flash", "gemini-1.5-pro"},
		ArgoModels:         []string{"gpt4", "gpt35", "claude"},
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
		// Set mock URLs
		OpenAIURL:   openAIMock.URL + "/v1/chat/completions",
		GeminiURL:   geminiMock.URL + "/v1beta/models",
		ArgoBaseURL: argoMock.URL,
	}

	// Initialize model lists
	config.InitializeModelLists()

	// Create server
	server := NewServer(config)
	proxyServer := httptest.NewServer(server)

	return proxyServer, openAIMock, geminiMock, argoMock
}

// MockOpenAI creates a mock OpenAI server
func NewMockOpenAI(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock OpenAI: %s %s", r.Method, r.URL.Path)

		// Check authorization
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Logf("Mock OpenAI: Missing Bearer token")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Read request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Logf("Mock OpenAI: Failed to read body: %v", err)
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		t.Logf("Mock OpenAI: Request body: %s", string(body))

		var req OpenAIRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Logf("Mock OpenAI: Failed to unmarshal: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		// Handle streaming
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			// Send streaming chunks
			chunks := []string{
				`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"Hello"},"index":0}]}`,
				`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" from"},"index":0}]}`,
				`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" OpenAI"},"index":0}]}`,
				`data: {"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"stop","index":0}]}`,
				`data: [DONE]`,
			}

			for _, chunk := range chunks {
				fmt.Fprintf(w, "%s\n\n", chunk)
				w.(http.Flusher).Flush()
			}
			return
		}

		// Non-streaming response
		resp := OpenAIResponse{
			ID:    "chatcmpl-123",
			Model: req.Model,
			Choices: []OpenAIChoice{
				{
					Message: OpenAIMessage{
						Role:    "assistant",
						Content: "Hello from mock OpenAI!",
					},
					FinishReason: "stop",
				},
			},
			Usage: OpenAIUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("Failed to encode response: %v", err)
		}
	})
}

// MockGemini creates a mock Gemini server
func NewMockGemini(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock Gemini: %s %s", r.Method, r.URL.Path)

		// Check API key in URL
		if !strings.Contains(r.URL.String(), "key=") {
			http.Error(w, "Missing API key", http.StatusUnauthorized)
			return
		}

		// Handle streaming
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			// Send streaming response
			chunks := []string{
				`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}`,
				`data: {"candidates":[{"content":{"parts":[{"text":" from Gemini"}]}}]}`,
				`data: {"candidates":[{"finishReason":"STOP"}]}`,
			}

			for _, chunk := range chunks {
				fmt.Fprintf(w, "%s\n\n", chunk)
				w.(http.Flusher).Flush()
			}
			return
		}

		// Non-streaming response
		resp := GeminiResponse{
			Candidates: []GeminiCandidate{
				{
					Content: GeminiContent{
						Parts: []GeminiPart{
							{Text: "Hello from mock Gemini!"},
						},
					},
					FinishReason: "STOP",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("Failed to encode response: %v", err)
		}
	})
}

// MockArgo creates a mock Argo server
func NewMockArgo(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock Argo: %s %s", r.Method, r.URL.Path)

		// Read request body
		body, _ := io.ReadAll(r.Body)
		var req ArgoChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		// Handle streaming
		if strings.Contains(r.URL.Path, "streamchat") {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)

			// Send character by character
			message := "Hello from mock Argo streaming!"
			for _, char := range message {
				fmt.Fprintf(w, "%c", char)
				w.(http.Flusher).Flush()
			}
			return
		}

		// Non-streaming response
		resp := ArgoChatResponse{
			Response: "Hello from mock Argo!",
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("Failed to encode response: %v", err)
		}
	})
}
