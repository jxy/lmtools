package apiproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// MockProvider simulates API provider responses
type MockProvider struct {
	t         *testing.T
	provider  string
	responses map[string]interface{}
}

func NewMockProvider(t *testing.T, provider string) *MockProvider {
	return &MockProvider{
		t:         t,
		provider:  provider,
		responses: make(map[string]interface{}),
	}
}

func (m *MockProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.t.Logf("Mock %s received: %s %s", m.provider, r.Method, r.URL.Path)

	// Read body
	body, _ := io.ReadAll(r.Body)
	m.t.Logf("Request body: %s", string(body))

	switch m.provider {
	case "openai":
		m.handleOpenAI(w, r, body)
	case "gemini":
		m.handleGemini(w, r, body)
	case "argo":
		m.handleArgo(w, r, body)
	default:
		http.Error(w, "Unknown provider", http.StatusBadRequest)
	}
}

func (m *MockProvider) handleOpenAI(w http.ResponseWriter, r *http.Request, body []byte) {
	// Check authorization
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
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
			time.Sleep(10 * time.Millisecond)
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
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Logf("Failed to encode OpenAI response: %v", err)
	}
}

func (m *MockProvider) handleGemini(w http.ResponseWriter, r *http.Request, body []byte) {
	// Check API key in query
	if !strings.Contains(r.URL.Query().Get("key"), "gemini-key") {
		http.Error(w, "Invalid API key", http.StatusForbidden)
		return
	}

	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Handle streaming
	if strings.Contains(r.URL.Path, "streamGenerateContent") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send Gemini streaming format
		chunks := []map[string]interface{}{
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": "Hello"},
							},
						},
					},
				},
			},
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": " from Gemini"},
							},
						},
						"finishReason": "STOP",
					},
				},
			},
		}

		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
		return
	}

	// Non-streaming response
	resp := GeminiResponse{
		Candidates: []GeminiCandidate{
			{
				Content: GeminiContent{
					Role: "model",
					Parts: []GeminiPart{
						{Text: "Hello from mock Gemini!"},
					},
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: GeminiUsage{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Logf("Failed to encode Gemini response: %v", err)
	}
}

func (m *MockProvider) handleArgo(w http.ResponseWriter, r *http.Request, body []byte) {
	var req ArgoChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Handle streaming
	if strings.Contains(r.URL.Path, "streamchat") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		// Stream plain text
		response := "Hello from mock Argo streaming!"
		for _, char := range response {
			fmt.Fprintf(w, "%c", char)
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
		return
	}

	// Non-streaming response - check if tools are requested
	if len(req.Tools) > 0 || (len(req.Messages) > 0 && strings.Contains(fmt.Sprintf("%v", req.Messages[0].Content), "list")) {
		// Response with tool use
		resp := ArgoChatResponse{
			Response: "I'll help you list the directory contents.\n\n<tool>LS</tool>\n<args>{\"path\":\"/usr/home/jin/K/W/P002/lmtools\"}</args>",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			m.t.Logf("Failed to encode Argo response with tools: %v", err)
		}
		return
	}

	// Regular non-streaming response
	resp := ArgoChatResponse{
		Response: "Hello from mock Argo!",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Logf("Failed to encode Argo response: %v", err)
	}
}
