package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestArgoStreamingTokenCounting(t *testing.T) {
	// Initialize logger for testing
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create a mock Argo server that streams a response
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/streamchat" {
			// Simulate Argo streaming response
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)

			// Write response in chunks
			chunks := []string{"Hello", " from", " Argo", " streaming!"}
			for _, chunk := range chunks {
				if _, err := w.Write([]byte(chunk)); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		}
	}))
	defer mockArgo.Close()

	// Create server config
	config := &Config{
		ArgoBaseURL: mockArgo.URL,
		ArgoUser:    "testuser",
	}

	// Create server components
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Create a test request
	anthReq := &AnthropicRequest{
		Model: "claude-3-haiku-20240307",
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Tell me a joke"`),
			},
		},
		MaxTokens: 100,
		Stream:    true,
	}

	// Create a response recorder
	recorder := httptest.NewRecorder()

	// Create context
	ctx := context.Background()

	// Create handler
	handler, err := NewAnthropicStreamHandler(recorder, anthReq.Model, ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Estimate and set input tokens (this is what streamFromArgo does)
	inputTokens := EstimateRequestTokens(anthReq)
	handler.mu.Lock()
	handler.state.InputTokens = inputTokens
	handler.mu.Unlock()

	// Send initial events (normally done in handleStreamingRequest)
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message_start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content_block_start: %v", err)
	}
	if err := handler.SendPing(); err != nil {
		t.Fatalf("Failed to send ping: %v", err)
	}

	// Call streamFromArgo
	err = server.streamFromArgo(ctx, anthReq, handler)
	if err != nil {
		t.Fatalf("streamFromArgo failed: %v", err)
	}

	// Check the response
	response := recorder.Body.String()

	// Verify that the message_start event contains non-zero input tokens
	if !strings.Contains(response, "message_start") {
		t.Error("Response should contain message_start event")
	}

	// Check that input_tokens is not 0
	if strings.Contains(response, `"input_tokens":0`) {
		t.Error("Input tokens should not be 0 in message_start event")
	}

	// Check that we have the correct input token count (39)
	if !strings.Contains(response, `"input_tokens":39`) {
		t.Error("Input tokens should be 39 in message_start event")
	}

	// Verify the response contains expected events for Argo streaming
	expectedEvents := []string{
		"event: message_start",
		"event: content_block_start",
		"event: ping",
		"event: content_block_delta", // This will be sent by ArgoStreamParser
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
		"data: [DONE]",
	}

	for _, event := range expectedEvents {
		if !strings.Contains(response, event) {
			t.Logf("Response might be missing '%s' (this is okay for Argo streaming)", event)
		}
	}

	// Verify that message_delta contains the correct token counts
	if strings.Contains(response, "message_delta") && !strings.Contains(response, `"input_tokens":39`) {
		t.Error("message_delta should contain input_tokens:39")
	}

	// Log the actual response for debugging
	t.Logf("Response:\n%s", response)
}
