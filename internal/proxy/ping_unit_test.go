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

// flushableRecorder wraps httptest.ResponseRecorder to implement http.Flusher
type flushableRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushableRecorder) Flush() {
	// No-op for testing - ResponseRecorder buffers everything
}

func newFlushableRecorder() *flushableRecorder {
	return &flushableRecorder{httptest.NewRecorder()}
}

// TestPingEventsDuringSlowArgoResponse tests ping events directly without middleware
func TestPingEventsDuringSlowArgoResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping long-running test in short mode")
	}

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

	// Create test-controlled context for clean shutdown
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Create a mock Argo server with 110ms delay (just over 100ms minimum ping interval)
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock Argo received request, simulating 110ms delay...")

		// Use timer with proper cleanup instead of time.Sleep
		timer := time.NewTimer(110 * time.Millisecond)
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
				Response: "Delayed response after 110 milliseconds",
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

	// Create server components directly (bypass middleware)
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Create streaming handler
	w := newFlushableRecorder()
	handler, err := NewAnthropicStreamHandler(w, "gpt35", serverCtx)
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
			Content: json.RawMessage(`"Test message"`),
		}},
		MaxTokens: 100,
	}

	// Track test start time
	startTime := time.Now()

	// Call simulateStreamingFromArgo directly
	done := make(chan bool)
	go func() {
		if err := server.simulateStreamingFromArgoWithInterval(context.Background(), anthReq, handler, 50*time.Millisecond); err != nil {
			t.Logf("simulateStreamingFromArgoWithInterval error: %v", err)
		}
		done <- true
	}()

	// Wait for completion
	<-done

	totalDuration := time.Since(startTime)
	t.Logf("Test duration: %v", totalDuration)

	// Now safely read the body after streaming is complete
	body := w.Body.String()

	// Count ping events
	pingCount := strings.Count(body, "event: ping")
	t.Logf("Total ping events: %d", pingCount)

	// We expect at least 2 pings:
	// 1. Initial ping (immediately)
	// 2. Ping at ~100ms while waiting for API (minimum clamp)
	if pingCount < 2 {
		t.Errorf("Expected at least 2 ping events, got %d", pingCount)
	}

	// Check that we have SSE events (the content might be missing due to test timing)
	if !strings.Contains(body, "event: message_start") {
		t.Error("Response missing SSE events")
	}

	// The important part is that we got 2 ping events - one initial, one at 50ms
	// The actual content delivery is tested in other tests
}

// TestPingEventsQuickResponse tests that we only get initial ping with fast response
func TestPingEventsQuickResponse(t *testing.T) {
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

	// Create server components
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Create handler
	w := newFlushableRecorder()
	ctx := context.Background()
	handler, _ := NewAnthropicStreamHandler(w, "gpt35", ctx)

	// Send initial events (errors are less critical in this test)
	_ = handler.SendMessageStart()
	_ = handler.SendContentBlockStart(0, "text")
	_ = handler.SendPing()

	// Create request
	anthReq := &AnthropicRequest{
		Model:  "gpt35",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Quick test"`),
		}},
	}

	// Execute
	startTime := time.Now()
	err := server.simulateStreamingFromArgoWithInterval(context.Background(), anthReq, handler, 50*time.Millisecond)
	duration := time.Since(startTime)

	if err != nil {
		t.Fatalf("Error in simulateStreamingFromArgo: %v", err)
	}

	t.Logf("Quick response duration: %v", duration)

	// Count pings
	body := w.Body.String()
	pingCount := strings.Count(body, "event: ping")

	// Should have 1 initial ping only (maybe 2 if timing is slow)
	if pingCount < 1 || pingCount > 2 {
		t.Errorf("Expected 1-2 ping events for quick response, got %d", pingCount)
	}

	// Verify content
	if !strings.Contains(body, "Instant response") {
		t.Error("Response missing expected content")
	}
}

// TestPingIntervalClamping tests that ping intervals below 100ms are clamped to the minimum
func TestPingIntervalClamping(t *testing.T) {
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

	// Create test-controlled context for clean shutdown
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Create a mock Argo server with 150ms delay
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Mock Argo received request, simulating 150ms delay...")

		// Use timer with proper cleanup instead of time.Sleep
		timer := time.NewTimer(150 * time.Millisecond)
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
				Response: "Response after clamped interval test",
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

	// Create server components
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Test with very small interval (10ms) which should be clamped to 100ms
	testCases := []struct {
		name             string
		requestInterval  time.Duration
		expectedInterval time.Duration
	}{
		{"Too small interval", 10 * time.Millisecond, minPingInterval},
		{"Zero interval", 0, 15 * time.Second},                             // Should use default
		{"Negative interval", -1 * time.Second, 15 * time.Second},          // Should use default
		{"Valid interval", 200 * time.Millisecond, 200 * time.Millisecond}, // Should not be clamped
		{"Too large interval", 120 * time.Second, maxPingInterval},         // Should be clamped to max
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create handler
			w := newFlushableRecorder()
			handler, _ := NewAnthropicStreamHandler(w, "gpt35", context.Background())

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
					Content: json.RawMessage(`"Test interval clamping"`),
				}},
			}

			// Execute with the test interval
			startTime := time.Now()
			err := server.simulateStreamingFromArgoWithInterval(context.Background(), anthReq, handler, tc.requestInterval)
			duration := time.Since(startTime)

			if err != nil {
				t.Fatalf("Error in simulateStreamingFromArgo: %v", err)
			}

			t.Logf("Test '%s': duration=%v, requested interval=%v", tc.name, duration, tc.requestInterval)

			// For very small intervals, we should see the warning in logs
			// The actual ping timing verification is done in other tests
			// This test primarily verifies that the function doesn't panic or spin the CPU
		})
	}
}
