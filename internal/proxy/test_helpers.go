package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SetupTestLogger initializes logger for tests with standard options.
// Call this at the start of tests that need logging.
func SetupTestLogger(t *testing.T) {
	t.Helper()
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
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
			Usage: &OpenAIUsage{
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

// MockGoogle creates a mock Google server
func NewMockGoogle(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock Google: %s %s", r.Method, r.URL.Path)

		// Check API key in URL
		if !strings.Contains(r.URL.String(), "key=") {
			http.Error(w, "Missing API key", http.StatusUnauthorized)
			return
		}

		// Handle streaming
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			setSSEHeaders(w)
			w.WriteHeader(http.StatusOK)

			// Send streaming response
			chunks := []string{
				`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}`,
				`data: {"candidates":[{"content":{"parts":[{"text":" from Google"}]}}]}`,
				`data: {"candidates":[{"finishReason":"STOP"}]}`,
			}

			for _, chunk := range chunks {
				fmt.Fprintf(w, "%s\n\n", chunk)
				w.(http.Flusher).Flush()
			}
			return
		}

		// Non-streaming response
		resp := GoogleResponse{
			Candidates: []GoogleCandidate{
				{
					Content: GoogleContent{
						Parts: []GooglePart{
							{Text: "Hello from mock Google!"},
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
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)

			// Send character by character to simulate streaming
			message := "Hello from mock Argo streaming!"
			for _, char := range message {
				fmt.Fprintf(w, "%c", char)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			// The response will be closed when the handler returns
			return
		}

		// Non-streaming response for /chat/ endpoint
		resp := ArgoChatResponse{
			Response: "Hello from mock Argo!",
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Logf("Failed to encode response: %v", err)
		}
	})
}

// flushableRecorder wraps httptest.ResponseRecorder to implement http.Flusher.
// This is used in streaming tests where the handler expects a Flusher.
type flushableRecorder struct {
	*httptest.ResponseRecorder
}

// Flush implements http.Flusher. It's a no-op since ResponseRecorder buffers everything.
func (f *flushableRecorder) Flush() {}

// newFlushableRecorder creates a new flushableRecorder for streaming tests.
func newFlushableRecorder() *flushableRecorder {
	return &flushableRecorder{httptest.NewRecorder()}
}

// newTestAnthropicStreamHandler creates an Anthropic stream handler and sends
// initial events (message_start, content_block_start for text). This helper
// reduces boilerplate in streaming tests.
func newTestAnthropicStreamHandler(t *testing.T, w http.ResponseWriter, model string) *AnthropicStreamHandler {
	t.Helper()
	ctx := context.Background()
	h, err := NewAnthropicStreamHandler(w, model, ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}
	if err := h.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := h.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}
	return h
}

// newTestAnthropicStreamHandlerWithContext creates an Anthropic stream handler with a
// provided context and sends initial events (message_start, content_block_start for text).
func newTestAnthropicStreamHandlerWithContext(t *testing.T, w http.ResponseWriter, model string, ctx context.Context) *AnthropicStreamHandler {
	t.Helper()
	h, err := NewAnthropicStreamHandler(w, model, ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}
	if err := h.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := h.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}
	return h
}
