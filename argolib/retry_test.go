package argo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSendRequestWithRetry(t *testing.T) {
	t.Run("retry preserves request body", func(t *testing.T) {
		// Counter to track attempts
		var attempts int32
		expectedBody := []byte(`{"test": "data"}`)

		// Create test server that fails first 2 attempts
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Read request body
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("Failed to read request body: %v", err)
			}

			// Verify body is preserved across retries
			if !bytes.Equal(body, expectedBody) {
				t.Errorf("Request body mismatch. Got %s, want %s", body, expectedBody)
			}

			attemptNum := atomic.AddInt32(&attempts, 1)
			if attemptNum < 3 {
				// Fail first 2 attempts with retryable error
				w.WriteHeader(http.StatusInternalServerError)
				if _, err := w.Write([]byte("Server error")); err != nil {
					t.Logf("Failed to write error response: %v", err)
				}
				return
			}

			// Success on 3rd attempt
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(`{"result": "success"}`)); err != nil {
				t.Logf("Failed to write success response: %v", err)
			}
		}))
		defer server.Close()

		// Create request
		req, err := http.NewRequest("POST", server.URL, bytes.NewReader(expectedBody))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		// Configure retry
		retryConfig := RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 10 * time.Millisecond,
			MaxDelay:     100 * time.Millisecond,
			Multiplier:   2.0,
		}

		// Send request with retry
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)
		resp, cancel, err := SendRequestWithRetry(ctx, client, req, expectedBody, retryConfig)
		if cancel != nil {
			defer cancel()
		}
		if err != nil {
			t.Fatalf("SendRequestWithRetry failed: %v", err)
		}
		defer resp.Body.Close()

		// Verify success
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		// Verify all 3 attempts were made
		if atomic.LoadInt32(&attempts) != 3 {
			t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
		}
	})

	t.Run("retry with empty body", func(t *testing.T) {
		// Test GET request (no body)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, err := http.NewRequest("GET", server.URL, nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		retryConfig := DefaultRetryConfig()
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)

		// Should work with nil body
		resp, cancel, err := SendRequestWithRetry(ctx, client, req, nil, retryConfig)
		if cancel != nil {
			defer cancel()
		}
		if err != nil {
			t.Fatalf("SendRequestWithRetry failed: %v", err)
		}
		resp.Body.Close()
	})

	t.Run("non-retryable error", func(t *testing.T) {
		// Test that 400 errors are not retried
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.WriteHeader(http.StatusBadRequest)
			if _, err := w.Write([]byte("Bad request")); err != nil {
				t.Logf("Failed to write bad request response: %v", err)
			}
		}))
		defer server.Close()

		req, err := http.NewRequest("POST", server.URL, nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		retryConfig := DefaultRetryConfig()
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)

		resp, cancel, err := SendRequestWithRetry(ctx, client, req, []byte("test"), retryConfig)
		if cancel != nil {
			defer cancel()
		}
		if err != nil {
			t.Fatalf("SendRequestWithRetry failed: %v", err)
		}
		resp.Body.Close()

		// Should only attempt once
		if atomic.LoadInt32(&attempts) != 1 {
			t.Errorf("Expected 1 attempt for non-retryable error, got %d", atomic.LoadInt32(&attempts))
		}
	})

	t.Run("retry with Retry-After header (seconds)", func(t *testing.T) {
		var attempts int32
		retryAfterSeconds := 2

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attemptNum := atomic.AddInt32(&attempts, 1)
			if attemptNum == 1 {
				// First attempt: return 429 with Retry-After
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("Rate limited"))
				return
			}
			// Success on second attempt
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": "success"}`))
		}))
		defer server.Close()

		req, _ := http.NewRequest("POST", server.URL, nil)
		retryConfig := RetryConfig{
			MaxAttempts:       2,
			InitialDelay:      100 * time.Millisecond,
			MaxDelay:          5 * time.Second,
			Multiplier:        2.0,
			RespectRetryAfter: true,
			Timeout:           30 * time.Second,
		}

		start := time.Now()
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)
		resp, cancel, err := SendRequestWithRetry(ctx, client, req, nil, retryConfig)
		if cancel != nil {
			defer cancel()
		}
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("SendRequestWithRetry failed: %v", err)
		}
		resp.Body.Close()

		// Verify delay was respected (should be around 2 seconds)
		if elapsed < time.Duration(retryAfterSeconds)*time.Second {
			t.Errorf("Expected delay of at least %d seconds, but got %v", retryAfterSeconds, elapsed)
		}
	})

	t.Run("retry with Retry-After header (HTTP date)", func(t *testing.T) {
		var attempts int32
		futureTime := time.Now().Add(1 * time.Second)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attemptNum := atomic.AddInt32(&attempts, 1)
			if attemptNum == 1 {
				// First attempt: return 503 with Retry-After as HTTP date
				w.Header().Set("Retry-After", futureTime.Format(http.TimeFormat))
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("Service unavailable"))
				return
			}
			// Success on second attempt
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result": "success"}`))
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		retryConfig := RetryConfig{
			MaxAttempts:       2,
			InitialDelay:      100 * time.Millisecond,
			MaxDelay:          5 * time.Second,
			Multiplier:        2.0,
			RespectRetryAfter: true,
		}

		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)
		resp, cancel, err := SendRequestWithRetry(ctx, client, req, nil, retryConfig)
		if cancel != nil {
			defer cancel()
		}
		if err != nil {
			t.Fatalf("SendRequestWithRetry failed: %v", err)
		}
		resp.Body.Close()

		if atomic.LoadInt32(&attempts) != 2 {
			t.Errorf("Expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
		}
	})

	t.Run("jitter in retry delay", func(t *testing.T) {
		// Test that jitter adds randomness to delays
		delays := make([]time.Duration, 0)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		// Run multiple times to check for variation
		for i := 0; i < 3; i++ {
			start := time.Now()
			req, _ := http.NewRequest("GET", server.URL, nil)
			retryConfig := RetryConfig{
				MaxAttempts:  2,
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     1 * time.Second,
				Multiplier:   1.0, // Keep multiplier at 1 to isolate jitter effect
				JitterFactor: 0.5, // 50% jitter
			}

			ctx := context.Background()
			client := NewHTTPClient(5 * time.Second)
			_, cancel, _ := SendRequestWithRetry(ctx, client, req, nil, retryConfig)
			if cancel != nil {
				cancel()
			}

			delays = append(delays, time.Since(start))
		}

		// With jitter, delays should vary
		allSame := true
		for i := 1; i < len(delays); i++ {
			if delays[i] != delays[i-1] {
				allSame = false
				break
			}
		}

		if allSame {
			t.Error("Expected delays to vary with jitter, but all were the same")
		}
	})

	t.Run("context cancellation during retry", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		retryConfig := RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 1 * time.Second,
			MaxDelay:     5 * time.Second,
			Multiplier:   2.0,
		}

		ctx, cancel := context.WithCancel(context.Background())
		client := NewHTTPClient(5 * time.Second)

		// Cancel context after a short delay
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		_, cancel2, err := SendRequestWithRetry(ctx, client, req, nil, retryConfig)
		if cancel2 != nil {
			cancel2()
		}
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("Expected error due to context cancellation")
		}

		if elapsed > 500*time.Millisecond {
			t.Errorf("Expected quick cancellation, but took %v", elapsed)
		}
	})
}

func TestExtractRetryInfo(t *testing.T) {
	tests := []struct {
		name           string
		retryAfter     string
		expectedAfter  time.Duration
		expectedReason string
		hasRetryAfter  bool
	}{
		{
			name:           "seconds format",
			retryAfter:     "120",
			expectedAfter:  120 * time.Second,
			expectedReason: "server requested 120 second delay",
			hasRetryAfter:  true,
		},
		{
			name:           "empty header",
			retryAfter:     "",
			expectedAfter:  0,
			expectedReason: "",
			hasRetryAfter:  false,
		},
		{
			name:           "invalid format",
			retryAfter:     "invalid",
			expectedAfter:  0,
			expectedReason: "",
			hasRetryAfter:  false,
		},
		{
			name:           "zero seconds",
			retryAfter:     "0",
			expectedAfter:  0,
			expectedReason: "",
			hasRetryAfter:  false,
		},
		{
			name:           "negative seconds",
			retryAfter:     "-5",
			expectedAfter:  0,
			expectedReason: "",
			hasRetryAfter:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				Header: http.Header{},
			}
			if tt.retryAfter != "" {
				resp.Header.Set("Retry-After", tt.retryAfter)
			}

			info := extractRetryInfo(resp)

			if tt.hasRetryAfter {
				if info.After != tt.expectedAfter {
					t.Errorf("Expected After=%v, got %v", tt.expectedAfter, info.After)
				}
				if info.Reason != tt.expectedReason {
					t.Errorf("Expected Reason=%q, got %q", tt.expectedReason, info.Reason)
				}
			} else {
				if info.After != 0 || info.Reason != "" {
					t.Errorf("Expected empty RetryInfo, got After=%v, Reason=%q", info.After, info.Reason)
				}
			}
		})
	}

	t.Run("HTTP date format", func(t *testing.T) {
		futureTime := time.Now().Add(30 * time.Second).UTC()
		resp := &http.Response{
			Header: http.Header{
				"Retry-After": []string{futureTime.Format(http.TimeFormat)},
			},
		}

		info := extractRetryInfo(resp)

		// Should be approximately 30 seconds
		if info.After < 29*time.Second || info.After > 31*time.Second {
			t.Errorf("Expected After to be around 30s, got %v", info.After)
		}

		expectedReason := fmt.Sprintf("server requested retry after %s", futureTime.Format(time.RFC3339))
		if info.Reason != expectedReason {
			t.Errorf("Expected Reason=%q, got %q", expectedReason, info.Reason)
		}
	})
}

func TestSendRequestWithTimeout(t *testing.T) {
	t.Run("adds timeout when context has no deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulate slow response
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		ctx := context.Background() // No deadline
		client := NewHTTPClient(5 * time.Second)

		// Use a short timeout
		_, cancel, err := SendRequestWithTimeout(ctx, client, req, 100*time.Millisecond)
		if cancel != nil {
			defer cancel()
		}

		if err == nil {
			t.Fatal("Expected timeout error")
		}

		if !isTimeoutError(err) {
			t.Errorf("Expected timeout error, got %v", err)
		}
	})

	t.Run("preserves existing context deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)

		// Context with existing deadline
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		client := NewHTTPClient(5 * time.Second)

		start := time.Now()
		_, cancel2, err := SendRequestWithTimeout(ctx, client, req, 1*time.Second) // Try to set longer timeout
		if cancel2 != nil {
			defer cancel2()
		}
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("Expected timeout error")
		}

		// Should timeout at ~50ms, not 1s
		if elapsed > 100*time.Millisecond {
			t.Errorf("Expected timeout at ~50ms, but took %v", elapsed)
		}
	})

	t.Run("no timeout when zero duration", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)

		resp, cancel, err := SendRequestWithTimeout(ctx, client, req, 0)
		if cancel != nil {
			defer cancel()
		}
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		resp.Body.Close()
	})
}

func TestCloseResponse(t *testing.T) {
	t.Run("handles nil response", func(t *testing.T) {
		// Should not panic
		closeResponse(nil)
	})

	t.Run("drains and closes response body", func(t *testing.T) {
		// Test with small body (should drain all)
		t.Run("small body", func(t *testing.T) {
			closed := false
			bodySize := 10 * 1024 // 10KB
			reader := &testReader{
				Reader: bytes.NewReader(bytes.Repeat([]byte("x"), bodySize)),
				onClose: func() {
					closed = true
				},
			}

			resp := &http.Response{
				Body:          reader,
				ContentLength: int64(bodySize),
			}

			closeResponse(resp)

			if !closed {
				t.Error("Expected body to be closed")
			}

			// Should drain all 10KB since it's less than 512KB
			if reader.bytesRead != bodySize {
				t.Errorf("Expected %d bytes to be drained, but got %d bytes", bodySize, reader.bytesRead)
			}
		})

		// Test with large body (should drain up to 512KB)
		t.Run("large body", func(t *testing.T) {
			closed := false
			bodySize := 1024 * 1024 // 1MB
			reader := &testReader{
				Reader: bytes.NewReader(bytes.Repeat([]byte("x"), bodySize)),
				onClose: func() {
					closed = true
				},
			}

			resp := &http.Response{
				Body:          reader,
				ContentLength: int64(bodySize),
			}

			closeResponse(resp)

			if !closed {
				t.Error("Expected body to be closed")
			}

			// Should drain up to 512KB
			maxDrain := 512 * 1024
			if reader.bytesRead != maxDrain {
				t.Errorf("Expected %d bytes to be drained, but got %d bytes", maxDrain, reader.bytesRead)
			}
		})

		// Test with unknown content length
		t.Run("unknown content length", func(t *testing.T) {
			closed := false
			bodySize := 100 * 1024 // 100KB
			reader := &testReader{
				Reader: bytes.NewReader(bytes.Repeat([]byte("x"), bodySize)),
				onClose: func() {
					closed = true
				},
			}

			resp := &http.Response{
				Body:          reader,
				ContentLength: -1, // Unknown
			}

			closeResponse(resp)

			if !closed {
				t.Error("Expected body to be closed")
			}

			// Should drain all 100KB since it's less than 512KB
			if reader.bytesRead != bodySize {
				t.Errorf("Expected %d bytes to be drained, but got %d bytes", bodySize, reader.bytesRead)
			}
		})

		// Test with zero content length (e.g., HEAD response)
		t.Run("zero content length", func(t *testing.T) {
			closed := false
			reader := &testReader{
				Reader: bytes.NewReader([]byte{}),
				onClose: func() {
					closed = true
				},
			}

			resp := &http.Response{
				Body:          reader,
				ContentLength: 0,
			}

			closeResponse(resp)

			if !closed {
				t.Error("Expected body to be closed")
			}

			// Should not drain anything for ContentLength=0
			if reader.bytesRead != 0 {
				t.Errorf("Expected 0 bytes to be drained for ContentLength=0, but got %d bytes", reader.bytesRead)
			}
		})
	})
}

// testReader is a custom reader for testing
type testReader struct {
	*bytes.Reader
	bytesRead int
	onClose   func()
}

func (tr *testReader) Read(p []byte) (n int, err error) {
	n, err = tr.Reader.Read(p)
	tr.bytesRead += n
	return n, err
}

func (tr *testReader) Close() error {
	if tr.onClose != nil {
		tr.onClose()
	}
	return nil
}

// Helper function to check if error is timeout-related
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "context deadline exceeded") ||
		strings.Contains(errMsg, "request canceled") ||
		strings.Contains(errMsg, "timeout") ||
		err == context.DeadlineExceeded
}

func TestCancelFunctionContract(t *testing.T) {
	t.Run("cancel function is returned when timeout is added", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Quick response
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("test"))
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		ctx := context.Background() // No deadline
		client := NewHTTPClient(5 * time.Second)

		resp, cancel, err := SendRequestWithTimeout(ctx, client, req, 1*time.Second)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		defer resp.Body.Close()

		// Cancel function should be non-nil when timeout is added
		if cancel == nil {
			t.Error("Expected non-nil cancel function when timeout is added")
		}

		// Should be safe to call cancel multiple times (idempotent)
		cancel()
		cancel() // Second call should not panic
	})

	t.Run("cancel function is nil when context already has deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("test"))
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)

		// Context with existing deadline
		ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ctxCancel()

		client := NewHTTPClient(5 * time.Second)

		resp, cancel, err := SendRequestWithTimeout(ctx, client, req, 1*time.Second)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		defer resp.Body.Close()

		// Cancel function should be nil when context already has deadline
		if cancel != nil {
			t.Error("Expected nil cancel function when context already has deadline")
		}
	})

	t.Run("retry preserves cancel function contract", func(t *testing.T) {
		var attempts int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attemptNum := atomic.AddInt32(&attempts, 1)
			if attemptNum < 3 {
				// Fail first 2 attempts
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Success on 3rd attempt
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		retryConfig := RetryConfig{
			MaxAttempts:  3,
			InitialDelay: 10 * time.Millisecond,
			MaxDelay:     100 * time.Millisecond,
			Multiplier:   2.0,
			Timeout:      1 * time.Second,
		}

		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)
		resp, finalCancel, err := SendRequestWithRetry(ctx, client, req, nil, retryConfig)
		if err != nil {
			t.Fatalf("SendRequestWithRetry failed: %v", err)
		}

		// Verify we got a response
		if resp == nil {
			t.Fatal("Expected non-nil response")
		}

		// Read response body before calling cancel
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Verify response content
		if string(body) != "success" {
			t.Errorf("Expected body 'success', got %q", string(body))
		}

		// Now it's safe to call cancel
		if finalCancel != nil {
			finalCancel()
			// Test idempotency
			finalCancel()
		}

		// Verify that we made 3 attempts
		if atomic.LoadInt32(&attempts) != 3 {
			t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
		}
	})

	t.Run("double cancel protection in retry failure", func(t *testing.T) {
		// Server that always fails
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		retryConfig := RetryConfig{
			MaxAttempts:  2,
			InitialDelay: 10 * time.Millisecond,
			MaxDelay:     100 * time.Millisecond,
			Multiplier:   2.0,
			Timeout:      1 * time.Second,
		}

		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)

		// This should fail after all retries
		_, cancel, err := SendRequestWithRetry(ctx, client, req, nil, retryConfig)

		// Error is expected
		if err == nil {
			t.Fatal("Expected error after all retries failed")
		}

		// Cancel should be nil after error cleanup
		if cancel != nil {
			t.Error("Expected nil cancel function after retry failure cleanup")
		}
	})
}

func TestTimeoutEdgeCases(t *testing.T) {
	t.Run("zero timeout with no context deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Slow response
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)

		// Zero timeout should not add any timeout
		resp, cancel, err := SendRequestWithTimeout(ctx, client, req, 0)
		if err != nil {
			t.Fatalf("Unexpected error with zero timeout: %v", err)
		}
		defer resp.Body.Close()

		// Cancel should be nil when no timeout is added
		if cancel != nil {
			t.Error("Expected nil cancel function with zero timeout")
		}
	})

	t.Run("negative timeout treated as zero", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)

		// Negative timeout should be treated like zero (no timeout)
		resp, cancel, err := SendRequestWithTimeout(ctx, client, req, -1*time.Second)
		if err != nil {
			t.Fatalf("Unexpected error with negative timeout: %v", err)
		}
		defer resp.Body.Close()

		if cancel != nil {
			t.Error("Expected nil cancel function with negative timeout")
		}
	})

	t.Run("timeout cancellation during response body read", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			// Write partial response then delay
			_, _ = w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			time.Sleep(500 * time.Millisecond)
			_, _ = w.Write([]byte(" response"))
		}))
		defer server.Close()

		req, _ := http.NewRequest("GET", server.URL, nil)
		ctx := context.Background()
		client := NewHTTPClient(5 * time.Second)

		// Short timeout that expires during body read
		resp, cancel, err := SendRequestWithTimeout(ctx, client, req, 200*time.Millisecond)
		if err != nil {
			t.Fatalf("Request failed before body read: %v", err)
		}

		// Try to read body - this should fail due to timeout
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Cancel after body is closed
		if cancel != nil {
			cancel()
		}

		// Body read should have failed or been incomplete
		if readErr == nil && strings.Contains(string(body), "response") {
			t.Error("Expected timeout during body read, but got complete response")
		}
	})

	t.Run("concurrent requests with different timeouts", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Vary response time based on path
			if r.URL.Path == "/slow" {
				time.Sleep(300 * time.Millisecond)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := NewHTTPClient(5 * time.Second)
		ctx := context.Background()

		// Start multiple concurrent requests with different timeouts
		type result struct {
			path    string
			timeout time.Duration
			err     error
		}

		results := make(chan result, 2)

		// Fast request with long timeout
		go func() {
			req, _ := http.NewRequest("GET", server.URL+"/fast", nil)
			_, cancel, err := SendRequestWithTimeout(ctx, client, req, 1*time.Second)
			if cancel != nil {
				defer cancel()
			}
			results <- result{"/fast", 1 * time.Second, err}
		}()

		// Slow request with short timeout
		go func() {
			req, _ := http.NewRequest("GET", server.URL+"/slow", nil)
			_, cancel, err := SendRequestWithTimeout(ctx, client, req, 100*time.Millisecond)
			if cancel != nil {
				defer cancel()
			}
			results <- result{"/slow", 100 * time.Millisecond, err}
		}()

		// Collect results
		var fastResult, slowResult result
		for i := 0; i < 2; i++ {
			r := <-results
			if r.path == "/fast" {
				fastResult = r
			} else {
				slowResult = r
			}
		}

		// Fast request should succeed
		if fastResult.err != nil {
			t.Errorf("Fast request failed: %v", fastResult.err)
		}

		// Slow request should timeout
		if slowResult.err == nil {
			t.Error("Expected slow request to timeout")
		}
	})
}
