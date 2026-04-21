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

		// Check API key in header
		if r.Header.Get("x-goog-api-key") == "" {
			http.Error(w, "Missing API key", http.StatusUnauthorized)
			return
		}

		// Handle streaming
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			if got := r.URL.Query().Get("alt"); got != "sse" {
				http.Error(w, "Missing alt=sse", http.StatusBadRequest)
				return
			}
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

		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
			var req OpenAIRequest
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}

			if req.Stream {
				setSSEHeaders(w)
				w.WriteHeader(http.StatusOK)
				chunks := []string{
					`data: {"id":"chatcmpl-argo-1","choices":[{"delta":{"content":"Hello"},"index":0}]}`,
					`data: {"id":"chatcmpl-argo-1","choices":[{"delta":{"content":" from"},"index":0}]}`,
					`data: {"id":"chatcmpl-argo-1","choices":[{"delta":{"content":" Argo"},"index":0}]}`,
					`data: {"id":"chatcmpl-argo-1","choices":[{"delta":{},"finish_reason":"stop","index":0}]}`,
					`data: {"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
					`data: [DONE]`,
				}
				for _, chunk := range chunks {
					fmt.Fprintf(w, "%s\n\n", chunk)
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				return
			}

			resp := OpenAIResponse{
				ID:    "chatcmpl-argo-123",
				Model: req.Model,
				Choices: []OpenAIChoice{
					{
						Message: OpenAIMessage{
							Role:    "assistant",
							Content: "Hello from mock Argo OpenAI!",
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
			return

		case strings.HasSuffix(r.URL.Path, "/v1/messages"):
			var req AnthropicRequest
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}

			if req.Stream {
				setSSEHeaders(w)
				w.WriteHeader(http.StatusOK)
				events := []string{
					`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_argo_1","type":"message","role":"assistant","model":"` + req.Model + `","content":[]}}`,
					`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello from Argo Anthropic"}}`,
					`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
					`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":5}}`,
					`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
				}
				for _, event := range events {
					fmt.Fprintf(w, "%s\n\n", event)
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				return
			}

			resp := AnthropicResponse{
				ID:    "msg_argo_123",
				Type:  "message",
				Role:  "assistant",
				Model: req.Model,
				Content: []AnthropicContentBlock{
					{
						Type: "text",
						Text: "Hello from mock Argo Anthropic!",
					},
				},
				StopReason: "end_turn",
				Usage: &AnthropicUsage{
					InputTokens:  10,
					OutputTokens: 5,
				},
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Logf("Failed to encode response: %v", err)
			}
			return

		case strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens"):
			var req AnthropicTokenCountRequest
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(AnthropicTokenCountResponse{InputTokens: 10}); err != nil {
				t.Logf("Failed to encode count_tokens response: %v", err)
			}
			return

		default:
			var req ArgoChatRequest
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}

			// Handle legacy streaming endpoint
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
				return
			}

			// Legacy non-streaming response for /chat/ endpoint
			resp := ArgoChatResponse{
				Response: "Hello from mock Argo!",
			}

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Logf("Failed to encode response: %v", err)
			}
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
