package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPingDuring30MillisecondDelay verifies ping behavior with a 30ms API delay
func TestPingDuring30MillisecondDelay(t *testing.T) {
	// Create test-controlled context for clean shutdown
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Create mock Argo server with 30ms delay (less than 50ms ping interval)
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Log("Mock Argo: simulating 30ms delay")

		// Use timer with proper cleanup instead of time.Sleep
		timer := time.NewTimer(30 * time.Millisecond)
		defer timer.Stop()

		select {
		case <-r.Context().Done():
			// Client cancelled request
			t.Log("Mock Argo: Client request cancelled")
			return
		case <-serverCtx.Done():
			// Test is ending, exit cleanly
			t.Log("Mock Argo: Test context cancelled, exiting cleanly")
			return
		case <-timer.C:
			// Timer expired, send response
			resp := ArgoChatResponse{
				Response: "Response after 30 milliseconds",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer func() {
		serverCancel()   // Signal handler to exit
		mockArgo.Close() // Now safe to close
	}()

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
		client:    retry.NewClient(10*time.Minute, nil),
	}

	// Create handler
	w := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(w, "gpt35", nil)
	if err != nil {
		t.Fatalf("Failed to create stream handler: %v", err)
	}

	// Send initial events
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message_start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content_block_start: %v", err)
	}
	if err := handler.SendPing(); err != nil {
		t.Fatalf("Failed to send initial ping: %v", err)
	}

	// Create request
	anthReq := &AnthropicRequest{
		Model:  "gpt35",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Test with 30 millisecond delay"`),
		}},
		MaxTokens: 50,
	}

	// Execute
	startTime := time.Now()
	err = server.simulateStreamingFromArgoWithInterval(context.Background(), anthReq, handler, 50*time.Millisecond)
	duration := time.Since(startTime)

	if err != nil {
		t.Fatalf("Error: %v", err)
	}

	t.Logf("Total duration: %v", duration)

	// Verify duration is at least 30ms
	if duration < 30*time.Millisecond {
		t.Errorf("Request completed too quickly: %v (expected >30ms)", duration)
	}

	// Check response
	body := w.Body.String()

	// Count pings
	pingCount := strings.Count(body, "event: ping")
	t.Logf("Ping count: %d", pingCount)

	// With 30ms delay, we should only have the initial ping
	// (50ms interval won't trigger)
	if pingCount < 1 || pingCount > 2 {
		t.Errorf("Expected 1-2 ping events (timing dependent), got %d", pingCount)
	}

	// Verify content (may be split across chunks)
	if !strings.Contains(body, "Response after 30 mi") && !strings.Contains(body, "lliseconds") {
		t.Error("Response missing expected content")
		t.Logf("Body:\n%s", body)
	}

	// Verify SSE structure
	expectedEvents := []string{
		"event: message_start",
		"event: content_block_start",
		"event: ping",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_stop",
	}

	for _, event := range expectedEvents {
		if !strings.Contains(body, event) {
			t.Errorf("Missing expected event: %s", event)
		}
	}
}
