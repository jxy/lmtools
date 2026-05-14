package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// SlowReader simulates a slow Argo stream with configurable delays
type SlowReader struct {
	chunks     []string
	currentIdx int
	chunkDelay time.Duration
}

func NewSlowReader(chunks []string, delay time.Duration) *SlowReader {
	return &SlowReader{
		chunks:     chunks,
		chunkDelay: delay,
	}
}

func (r *SlowReader) Read(p []byte) (n int, err error) {
	if r.currentIdx >= len(r.chunks) {
		return 0, io.EOF
	}

	// Simulate delay between chunks
	if r.currentIdx > 0 {
		time.Sleep(r.chunkDelay)
	}

	chunk := r.chunks[r.currentIdx]
	r.currentIdx++

	// Only copy what fits in the buffer
	n = copy(p, []byte(chunk))
	return n, nil
}

func TestArgoStreamingWithPings(t *testing.T) {
	tests := []struct {
		name             string
		chunks           []string
		chunkDelay       time.Duration
		pingInterval     time.Duration
		expectedMinPings int
		expectedMaxPings int
	}{
		{
			name:             "Quick response - no pings",
			chunks:           []string{"Hello ", "world!", " How ", "are ", "you?"},
			chunkDelay:       5 * time.Millisecond,
			pingInterval:     50 * time.Millisecond,
			expectedMinPings: 0,
			expectedMaxPings: 0,
		},
		{
			name:             "Slow response - should send pings",
			chunks:           []string{"Hello ", "world!"},
			chunkDelay:       40 * time.Millisecond, // Slightly increased for stability
			pingInterval:     30 * time.Millisecond,
			expectedMinPings: 1, // One ping between chunks
			expectedMaxPings: 2,
		},
		{
			name:             "Very slow response - multiple pings",
			chunks:           []string{"Start", "Middle", "End"},
			chunkDelay:       70 * time.Millisecond, // Reduced from 110ms, increased from 50ms for stability
			pingInterval:     30 * time.Millisecond,
			expectedMinPings: 3, // At least one ping between each chunk
			expectedMaxPings: 6, // Allow for timing variations
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test response writer
			w := httptest.NewRecorder()
			ctx := context.Background()

			// Create handler
			handler, err := NewAnthropicStreamHandler(w, "test-model", ctx)
			if err != nil {
				t.Fatalf("Failed to create handler: %v", err)
			}

			// Send initial events
			if err := handler.SendMessageStart(); err != nil {
				t.Fatalf("Failed to send message start: %v", err)
			}
			if err := handler.SendContentBlockStart(0, "text"); err != nil {
				t.Fatalf("Failed to send content block start: %v", err)
			}

			// Create slow reader
			reader := NewSlowReader(tt.chunks, tt.chunkDelay)

			// Parse with custom ping interval
			parser := NewArgoStreamParser(handler)
			err = parser.ParseWithPingInterval(reader, tt.pingInterval)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}

			// Count ping events in response
			response := w.Body.String()
			pingCount := strings.Count(response, `event: ping`)

			if pingCount < tt.expectedMinPings {
				t.Errorf("Expected at least %d pings, but got %d", tt.expectedMinPings, pingCount)
				t.Logf("Response:\n%s", response)
			}
			if pingCount > tt.expectedMaxPings {
				t.Errorf("Expected at most %d pings, but got %d", tt.expectedMaxPings, pingCount)
				t.Logf("Response:\n%s", response)
			}

			// Verify all chunks were sent
			for _, chunk := range tt.chunks {
				if !strings.Contains(response, chunk) {
					t.Errorf("Expected chunk %q not found in response", chunk)
				}
			}
		})
	}
}

func TestArgoStreamingSlowFirstToken(t *testing.T) {
	// Test that pings are sent while waiting for the first token
	tests := []struct {
		name             string
		firstTokenDelay  time.Duration
		pingInterval     time.Duration
		expectedMinPings int
		expectedMaxPings int
	}{
		{
			name:             "First token delayed 60ms with 25ms ping interval",
			firstTokenDelay:  60 * time.Millisecond, // Reduced from 120ms
			pingInterval:     25 * time.Millisecond, // Reduced from 50ms
			expectedMinPings: 2,                     // Should get at least 2 pings before first token
			expectedMaxPings: 3,                     // But no more than 3
		},
		{
			name:             "First token delayed 130ms with 25ms ping interval",
			firstTokenDelay:  130 * time.Millisecond, // Reduced from 260ms
			pingInterval:     25 * time.Millisecond,  // Reduced from 50ms
			expectedMinPings: 5,                      // Should get at least 5 pings
			expectedMaxPings: 6,
		},
		{
			name:             "First token delayed 40ms with 50ms ping interval",
			firstTokenDelay:  40 * time.Millisecond, // Reduced from 80ms
			pingInterval:     50 * time.Millisecond, // Reduced from 100ms
			expectedMinPings: 0,                     // No pings expected - first token arrives before ping interval
			expectedMaxPings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a pipe for controlled streaming
			pr, pw := io.Pipe()

			// Create a context with timeout for the entire test
			testCtx, testCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer testCancel()

			// Ensure pipe is closed when test ends
			defer func() {
				pr.Close()
				pw.Close()
			}()

			// Simulate Argo's slow first response in a goroutine
			writeDone := make(chan struct{})
			go func() {
				defer close(writeDone)

				// Wait before sending first token (simulating slow model startup)
				select {
				case <-time.After(tt.firstTokenDelay):
					// Continue with writing
				case <-testCtx.Done():
					// Test cancelled, exit early
					pw.Close()
					return
				}

				// Send first chunk
				_, _ = pw.Write([]byte("Hello"))

				// Send remaining chunks quickly
				time.Sleep(10 * time.Millisecond)
				_, _ = pw.Write([]byte(" world"))
				time.Sleep(10 * time.Millisecond)
				_, _ = pw.Write([]byte("!"))

				// Close the stream
				pw.Close()
			}()

			w := httptest.NewRecorder()
			ctx := context.Background()
			handler, err := NewAnthropicStreamHandler(w, "test-model", ctx)
			if err != nil {
				t.Fatalf("Failed to create handler: %v", err)
			}

			// Send initial events
			if err := handler.SendMessageStart(); err != nil {
				t.Fatalf("Failed to send message start: %v", err)
			}
			if err := handler.SendContentBlockStart(0, "text"); err != nil {
				t.Fatalf("Failed to send content block start: %v", err)
			}

			// Parse with ping interval
			parser := NewArgoStreamParser(handler)
			startTime := time.Now()

			// Run parser with timeout protection
			parseDone := make(chan error, 1)
			go func() {
				parseDone <- parser.ParseWithPingInterval(pr, tt.pingInterval)
			}()

			// Wait for parser to complete or timeout
			var parseErr error
			select {
			case parseErr = <-parseDone:
				// Parser completed
			case <-testCtx.Done():
				t.Fatal("Test timeout: parser did not complete within 2 seconds")
			}

			elapsed := time.Since(startTime)

			// Wait for writer goroutine to complete
			select {
			case <-writeDone:
				// Writer completed
			case <-time.After(100 * time.Millisecond):
				// Give it a bit more time but don't fail the test
			}

			if parseErr != nil {
				t.Fatalf("Parse error: %v", parseErr)
			}

			// Count ping events in response
			response := w.Body.String()
			pingCount := strings.Count(response, `event: ping`)

			// Verify ping count is within expected range
			if pingCount < tt.expectedMinPings {
				t.Errorf("Expected at least %d pings, but got %d (elapsed: %v)",
					tt.expectedMinPings, pingCount, elapsed)
				t.Logf("Response:\n%s", response)
			}
			if pingCount > tt.expectedMaxPings {
				t.Errorf("Expected at most %d pings, but got %d (elapsed: %v)",
					tt.expectedMaxPings, pingCount, elapsed)
				t.Logf("Response:\n%s", response)
			}

			// Verify the actual content was streamed
			if !strings.Contains(response, "Hello") || !strings.Contains(response, "world") {
				t.Error("Expected content not found in response")
			}

			// Log timing info for debugging
			t.Logf("First token delay: %v, Ping interval: %v, Elapsed: %v, Pings sent: %d",
				tt.firstTokenDelay, tt.pingInterval, elapsed, pingCount)
		})
	}
}

func TestArgoStreamingPingOnTimeout(t *testing.T) {
	// Create a context with timeout for the entire test
	testCtx, testCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer testCancel()

	// Create a reader that blocks indefinitely
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	// Write initial data then block
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_, _ = pw.Write([]byte("Initial data"))
		// Don't close immediately - simulate hanging connection
		select {
		case <-time.After(100 * time.Millisecond): // Reduced from 250ms
			pw.Close()
		case <-testCtx.Done():
			pw.Close()
		}
	}()

	w := httptest.NewRecorder()
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(w, "test-model", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Send initial events
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}

	// Use short ping interval for testing
	parser := NewArgoStreamParser(handler)

	// Run parser in goroutine
	done := make(chan error, 1)
	go func() {
		done <- parser.ParseWithPingInterval(pr, 20*time.Millisecond) // Reduced from 30ms
	}()

	// Wait for parser to complete or timeout
	select {
	case <-done:
		// Parser completed - now safe to read the buffer
		response := w.Body.String()
		pingCount := strings.Count(response, `event: ping`)

		// We should have gotten at least 2 pings (100ms wait / 20ms interval = 5 pings max)
		// but since we closed the pipe after 100ms, we check after completion
		if pingCount < 2 {
			t.Errorf("Expected at least 2 pings with 20ms interval, got %d", pingCount)
			t.Logf("Response:\n%s", response)
		}
	case <-testCtx.Done():
		t.Fatal("Test timeout: parser did not complete within 2 seconds")
	}

	// Wait for writer goroutine to complete
	select {
	case <-writeDone:
		// Writer completed
	case <-time.After(100 * time.Millisecond):
		// Give it a bit more time but don't fail the test
	}
}

func TestArgoSimulatedStreamingSlowResponse(t *testing.T) {
	// Argo now supports native streaming with tools, so the proxy should not
	// simulate ping-based streaming for these requests.

	tests := []struct {
		name             string
		responseDelay    time.Duration
		expectedMinPings int
		expectedMaxPings int
	}{
		{
			name:             "Response delayed 20ms - no pings expected",
			responseDelay:    20 * time.Millisecond, // Reduced from 50ms
			expectedMinPings: 0,
			expectedMaxPings: 0,
		},
		{
			name:             "Response delayed 100ms - still no simulated pings",
			responseDelay:    100 * time.Millisecond, // Reduced from 250ms
			expectedMinPings: 0,
			expectedMaxPings: 0,
		},
		{
			name:             "Response delayed 200ms - still no simulated pings",
			responseDelay:    200 * time.Millisecond, // Reduced from 450ms
			expectedMinPings: 0,
			expectedMaxPings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock Argo server with custom delay
			argoMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Logf("Mock Argo with delay: %s %s", r.Method, r.URL.Path)

				// Read request body
				body, _ := io.ReadAll(r.Body)
				var req OpenAIRequest
				if err := json.Unmarshal(body, &req); err != nil {
					http.Error(w, "Bad request", http.StatusBadRequest)
					return
				}
				if !req.Stream {
					http.Error(w, "expected streaming request", http.StatusBadRequest)
					return
				}

				// Simulate delay before responding
				time.Sleep(tt.responseDelay)

				setSSEHeaders(w)
				w.WriteHeader(http.StatusOK)
				chunks := []string{
					`data: {"id":"chatcmpl-delay-1","choices":[{"delta":{"content":"Hello from delayed mock Argo!"},"index":0}]}`,
					`data: {"id":"chatcmpl-delay-1","choices":[{"delta":{},"finish_reason":"stop","index":0}]}`,
					`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
					`data: [DONE]`,
				}
				for _, chunk := range chunks {
					if _, err := io.WriteString(w, chunk+"\n\n"); err != nil {
						t.Logf("Failed to write SSE chunk: %v", err)
						return
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
			}))
			defer argoMock.Close()

			// Create config
			config := &Config{
				Provider:           constants.ProviderArgo,
				ArgoUser:           "testuser",
				ArgoEnv:            "test",
				MaxRequestBodySize: 10 * 1024 * 1024,
				ProviderURL:        argoMock.URL,
				PingInterval:       100 * time.Millisecond, // Reduced from 200ms for fast testing
			}

			// Create server (NewEndpoints is called internally)
			server, cleanup := NewTestServer(t, config)
			t.Cleanup(cleanup)
			proxyServer := httptest.NewServer(server)
			defer proxyServer.Close()

			// Create request with tools. Native Argo streaming should handle this
			// directly without the proxy synthesizing ping events.
			req := AnthropicRequest{
				Model:     "gpto3",
				MaxTokens: 100,
				Stream:    true,
				Messages: []AnthropicMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"Test message"`),
					},
				},
				Tools: []AnthropicTool{
					{
						Name:        "test_tool",
						Description: "A test tool",
						InputSchema: json.RawMessage(`{"type":"object"}`),
					},
				},
			}

			// Make streaming request
			reqBody, _ := json.Marshal(req)
			resp, err := http.Post(
				proxyServer.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			// Read the entire response
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Error reading response: %v", err)
			}

			response := string(body)
			pingCount := strings.Count(response, `event: ping`)

			// Verify ping count is within expected range
			if pingCount < tt.expectedMinPings {
				t.Errorf("Expected at least %d pings, but got %d (response delay: %v)",
					tt.expectedMinPings, pingCount, tt.responseDelay)
			}
			if pingCount > tt.expectedMaxPings {
				t.Errorf("Expected at most %d pings, but got %d (response delay: %v)",
					tt.expectedMaxPings, pingCount, tt.responseDelay)
			}

			// Verify the response contains proper SSE format
			if !strings.Contains(response, "event: message_start") {
				t.Error("Missing message_start event")
			}
			// Anthropic format ends with message_stop, not [DONE]
			if !strings.Contains(response, "event: message_stop") {
				t.Error("Missing message_stop event")
			}

			// Log timing info for debugging
			t.Logf("Response delay: %v, Test ping interval: 200ms, Pings sent: %d",
				tt.responseDelay, pingCount)
		})
	}
}

func TestArgoStreamingContextCancellation(t *testing.T) {
	// This test verifies that the parser respects context cancellation
	// even though it doesn't have explicit context support

	// Create a slow reader that would normally take a long time
	chunks := []string{"Start", "Middle", "End"}
	reader := NewSlowReader(chunks, 50*time.Millisecond) // Reduced from 100ms

	w := httptest.NewRecorder()
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(w, "test-model", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Send initial events
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}

	parser := NewArgoStreamParser(handler)

	// Create a context-aware reader wrapper
	ctx, cancel := context.WithCancel(context.Background())
	ctxReader := &contextReader{
		ctx: ctx,
		r:   reader,
	}

	// Start parsing
	done := make(chan error)
	go func() {
		done <- parser.ParseWithPingInterval(ctxReader, 30*time.Millisecond)
	}()

	// Cancel after short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Parser should complete quickly after cancellation
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("Expected context canceled error, got: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Parser did not respond to context cancellation")
	}
}

// contextReader wraps a reader and respects context cancellation
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr *contextReader) Read(p []byte) (n int, err error) {
	select {
	case <-cr.ctx.Done():
		return 0, cr.ctx.Err()
	default:
		return cr.r.Read(p)
	}
}
