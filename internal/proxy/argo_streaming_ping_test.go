package proxy

import (
	"context"
	"io"
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
		name          string
		chunks        []string
		chunkDelay    time.Duration
		pingInterval  time.Duration
		expectedPings int
	}{
		{
			name:          "Quick response - no pings",
			chunks:        []string{"Hello ", "world!", " How ", "are ", "you?"},
			chunkDelay:    10 * time.Millisecond,
			pingInterval:  100 * time.Millisecond,
			expectedPings: 0,
		},
		{
			name:          "Slow response - should send pings",
			chunks:        []string{"Hello ", "world!"},
			chunkDelay:    150 * time.Millisecond,
			pingInterval:  100 * time.Millisecond,
			expectedPings: 1, // One ping between chunks
		},
		{
			name:          "Very slow response - multiple pings",
			chunks:        []string{"Start", "Middle", "End"},
			chunkDelay:    220 * time.Millisecond,
			pingInterval:  100 * time.Millisecond,
			expectedPings: 3, // At least one ping between each chunk
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test response writer
			w := httptest.NewRecorder()

			// Create handler
			handler, err := NewAnthropicStreamHandler(w, "test-model", nil)
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

			if pingCount != tt.expectedPings {
				t.Errorf("Expected %d pings, but got %d", tt.expectedPings, pingCount)
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

func TestArgoStreamingPingOnTimeout(t *testing.T) {
	// Create a reader that blocks indefinitely
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	// Write initial data then block
	go func() {
		_, _ = pw.Write([]byte("Initial data"))
		// Don't close - simulate hanging connection
		time.Sleep(500 * time.Millisecond)
		pw.Close()
	}()

	w := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(w, "test-model", nil)
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
	done := make(chan error)
	go func() {
		done <- parser.ParseWithPingInterval(pr, 50*time.Millisecond)
	}()

	// Wait for parser to complete
	select {
	case <-done:
		// Parser completed - now safe to read the buffer
		response := w.Body.String()
		pingCount := strings.Count(response, `event: ping`)

		// We should have gotten at least 2 pings (150ms wait / 50ms interval)
		// but since we closed the pipe after 500ms, we check after completion
		if pingCount < 2 {
			t.Errorf("Expected at least 2 pings with 50ms interval, got %d", pingCount)
			t.Logf("Response:\n%s", response)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Parser did not complete within timeout")
	}
}

func TestArgoStreamingContextCancellation(t *testing.T) {
	// This test verifies that the parser respects context cancellation
	// even though it doesn't have explicit context support

	// Create a slow reader that would normally take a long time
	chunks := []string{"Start", "Middle", "End"}
	reader := NewSlowReader(chunks, 200*time.Millisecond)

	w := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(w, "test-model", nil)
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
		done <- parser.ParseWithPingInterval(ctxReader, 50*time.Millisecond)
	}()

	// Cancel after short delay
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Parser should complete quickly after cancellation
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("Expected context canceled error, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
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
