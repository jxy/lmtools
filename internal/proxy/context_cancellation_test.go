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

// TestContextCancellationDuringPing tests that context cancellation properly stops the streaming
func TestContextCancellationDuringPing(t *testing.T) {
	// Initialize logger for testing
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create a context we control for the mock server handler
	// This allows us to cleanly shutdown the handler before closing the server
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel() // Ensure handler exits before test ends

	// Create a mock Argo server that simulates a slow response
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use a timer that can be properly cleaned up
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()

		// Wait for either request cancellation, test cancellation, or timeout
		select {
		case <-r.Context().Done():
			// Client request was cancelled (may not happen with httptest)
			t.Log("Mock server: Request context cancelled")
			return
		case <-serverCtx.Done():
			// Test is ending, exit cleanly
			t.Log("Mock server: Test context cancelled, exiting cleanly")
			return
		case <-timer.C:
			// This should never happen in the test
			t.Error("Mock server: Timer expired without cancellation")
			resp := ArgoChatResponse{
				Response: "This response should never be sent",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer func() {
		serverCancel()   // Signal handler to exit
		mockArgo.Close() // Now safe to close since handler will exit
	}()

	// Create config
	config := &Config{
		ArgoUser: "testuser",
		ArgoEnv:  mockArgo.URL,
	}

	// Set mock URL in config
	config.ArgoBaseURL = mockArgo.URL

	// Create server with shorter timeout for testing
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(5*time.Second, logger.GetLogger()), // Shorter timeout for test responsiveness
	}

	// Create handler
	w := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(w, "gpt35", nil)
	if err != nil {
		t.Fatalf("Failed to create stream handler: %v", err)
	}

	// Send initial events
	_ = handler.SendMessageStart()
	_ = handler.SendContentBlockStart(0, "text")
	_ = handler.SendPing()

	// Create request
	anthReq := &AnthropicRequest{
		Model:  "gpt35",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Test context cancellation"`),
		}},
		MaxTokens: 50,
	}

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Start streaming in a goroutine
	errChan := make(chan error, 1)
	go func() {
		err := server.simulateStreamingFromArgoWithInterval(ctx, anthReq, handler, 50*time.Millisecond)
		errChan <- err
	}()

	// Wait for at least two ping intervals
	time.Sleep(150 * time.Millisecond)

	// Cancel the context
	cancel()

	// Wait for the streaming to finish (should be immediate)
	select {
	case err := <-errChan:
		if err == nil {
			t.Error("Expected error from cancelled context, got nil")
		} else {
			t.Logf("Got expected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond): // Reduced from 2s since cancellation should be quick
		t.Error("Streaming did not terminate within 500ms after context cancellation")
	}

	// Verify we got at least two pings (initial + at least one during wait)
	body := w.Body.String()
	pingCount := strings.Count(body, "event: ping")

	if pingCount < 2 {
		t.Errorf("Expected at least 2 pings, got %d", pingCount)
		t.Logf("Response body:\n%s", body)
	}
}

// TestFastResponseNoPingDuringWait tests that fast responses don't trigger pings while waiting
func TestFastResponseNoPingDuringWait(t *testing.T) {
	// Initialize logger for testing
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create a mock Argo server that responds immediately
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ArgoChatResponse{
			Response: "Instant response",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockArgo.Close()

	// Create config
	config := &Config{
		ArgoUser: "testuser",
		ArgoEnv:  mockArgo.URL,
	}

	// Set mock URL in config
	config.ArgoBaseURL = mockArgo.URL

	// Create server
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Second, logger.GetLogger()),
	}

	// Create handler
	w := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(w, "gpt35", nil)
	if err != nil {
		t.Fatalf("Failed to create stream handler: %v", err)
	}

	// Send initial events
	_ = handler.SendMessageStart()
	_ = handler.SendContentBlockStart(0, "text")
	_ = handler.SendPing()

	// Create request
	anthReq := &AnthropicRequest{
		Model:  "gpt35",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Test fast response"`),
		}},
		MaxTokens: 50,
	}

	// Execute with 1 second ping interval (should never trigger)
	startTime := time.Now()
	err = server.simulateStreamingFromArgoWithInterval(context.Background(), anthReq, handler, 1*time.Second)
	duration := time.Since(startTime)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should complete quickly (allow extra margin for slow CI systems)
	if duration > 200*time.Millisecond {
		t.Errorf("Fast response took too long: %v", duration)
	}

	// Count pings - should only have initial ping
	body := w.Body.String()
	pingCount := strings.Count(body, "event: ping")

	if pingCount < 1 || pingCount > 2 {
		t.Errorf("Expected 1-2 ping events for fast response (timing dependent), got %d", pingCount)
	}

	// Verify response content
	if !strings.Contains(body, "Instant response") {
		t.Error("Response missing expected content")
	}
}
