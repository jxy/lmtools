package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
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
	case constants.ProviderOpenAI:
		m.handleOpenAI(w, r, body)
	case constants.ProviderGoogle:
		m.handleGoogle(w, r, body)
	case constants.ProviderArgo:
		m.handleArgo(w, r, body)
	default:
		http.Error(w, "Unknown provider", http.StatusBadRequest)
	}
}

func (m *MockProvider) streamOpenAIResponse(w http.ResponseWriter, r *http.Request, configuredResp interface{}) {
	setSSEHeaders(w)
	w.WriteHeader(http.StatusOK)

	// Convert configured response to streaming format
	if resp, ok := configuredResp.(*OpenAIResponse); ok {
		// Stream the content from the configured response
		for _, choice := range resp.Choices {
			// Handle content based on its type
			var content string
			switch v := choice.Message.Content.(type) {
			case string:
				content = v
			case *string:
				if v != nil {
					content = *v
				}
			default:
				// Try to convert to string
				content = fmt.Sprintf("%v", v)
			}

			if content != "" {
				// Split content into words for streaming
				words := strings.Fields(content)
				for i, word := range words {
					if i > 0 {
						word = " " + word // Add space before all words except first
					}
					chunk := fmt.Sprintf(`data: {"id":"%s","choices":[{"delta":{"content":"%s"},"index":%d}]}`,
						resp.ID, word, choice.Index)
					fmt.Fprintf(w, "%s\n\n", chunk)
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}

					// Small delay between chunks
					timer := time.NewTimer(5 * time.Millisecond)
					select {
					case <-r.Context().Done():
						timer.Stop()
						return
					case <-timer.C:
						// Continue
					}
				}
			}

			// Handle tool calls if present
			for _, toolCall := range choice.Message.ToolCalls {
				chunk := fmt.Sprintf(`data: {"id":"%s","choices":[{"delta":{"tool_calls":[{"id":"%s","type":"function","function":{"name":"%s","arguments":"%s"}}]},"index":%d}]}`,
					resp.ID, toolCall.ID, toolCall.Function.Name, toolCall.Function.Arguments, choice.Index)
				fmt.Fprintf(w, "%s\n\n", chunk)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}

			// Send finish reason
			chunk := fmt.Sprintf(`data: {"id":"%s","choices":[{"delta":{},"finish_reason":"%s","index":%d}]}`,
				resp.ID, choice.FinishReason, choice.Index)
			fmt.Fprintf(w, "%s\n\n", chunk)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}

	// Send [DONE]
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
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

	// Check for configured response
	if configuredResp, ok := m.responses[r.URL.Path]; ok {
		m.t.Logf("Using configured response for %s", r.URL.Path)

		// Handle streaming if requested
		if req.Stream {
			// Stream the configured response
			m.streamOpenAIResponse(w, r, configuredResp)
			return
		}

		// Non-streaming: return configured response
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(configuredResp); err != nil {
			m.t.Logf("Failed to encode OpenAI response: %v", err)
		}
		return
	}

	// Default behavior when no response is configured
	// Handle streaming
	if req.Stream {
		setSSEHeaders(w)
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
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			// Use context-aware delay
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return // Client cancelled
			case <-timer.C:
				// Continue to next chunk
			}
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
		Usage: &OpenAIUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Logf("Failed to encode OpenAI response: %v", err)
	}
}

func (m *MockProvider) handleGoogle(w http.ResponseWriter, r *http.Request, body []byte) {
	// Check API key in query
	if r.URL.Query().Get("key") == "" {
		http.Error(w, "Invalid API key", http.StatusForbidden)
		return
	}

	var req GoogleRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Handle streaming
	if strings.Contains(r.URL.Path, "streamGenerateContent") {
		setSSEHeaders(w)
		w.WriteHeader(http.StatusOK)

		// Send Google streaming format
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
								{"text": " from Google"},
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
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			// Use context-aware delay
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return // Client cancelled
			case <-timer.C:
				// Continue to next chunk
			}
		}
		return
	}

	// Non-streaming response
	resp := GoogleResponse{
		Candidates: []GoogleCandidate{
			{
				Content: GoogleContent{
					Role: "model",
					Parts: []GooglePart{
						{Text: "Hello from mock Google!"},
					},
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: &GoogleUsage{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Logf("Failed to encode Google response: %v", err)
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
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			// Use context-aware delay
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return // Client cancelled
			case <-timer.C:
				// Continue to next character
			}
		}
		return
	}

	// Non-streaming response - check if tools are requested
	hasTools := false
	switch tools := req.Tools.(type) {
	case []interface{}:
		hasTools = len(tools) > 0
	case []ArgoTool:
		hasTools = len(tools) > 0
	case []map[string]interface{}:
		hasTools = len(tools) > 0
	}

	if hasTools || (len(req.Messages) > 0 && strings.Contains(fmt.Sprintf("%v", req.Messages[0].Content), "list")) {
		// Response with tool use
		resp := ArgoChatResponse{
			Response: "I'll help you list the directory contents.\n\n<tool>LS</tool>\n<args>{\"path\":\"/path/to/project\"}</args>",
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
