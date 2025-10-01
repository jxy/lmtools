package retry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetry(t *testing.T) {
	t.Run("success on first attempt", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("GET", server.URL, nil)

		config := &Config{
			MaxRetries:     2,
			InitialBackoff: 100 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		resp, err := retryer.Do(context.Background(), client, req, nil)
		if err != nil {
			t.Fatalf("Expected success, got error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}
	})

	t.Run("retry on 5xx errors", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := atomic.AddInt32(&attempts, 1)
			if attempt < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success"))
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("POST", server.URL, nil)

		config := &Config{
			MaxRetries:     2,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		resp, err := retryer.Do(context.Background(), client, req, nil)
		if err != nil {
			t.Fatalf("Expected success after retries, got error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		if atomic.LoadInt32(&attempts) != 3 {
			t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
		}
	})

	t.Run("retry on 429 rate limit", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := atomic.AddInt32(&attempts, 1)
			if attempt < 2 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("GET", server.URL, nil)

		config := &Config{
			MaxRetries:     2,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		resp, err := retryer.Do(context.Background(), client, req, nil)
		if err != nil {
			t.Fatalf("Expected success after retries, got error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		if atomic.LoadInt32(&attempts) != 2 {
			t.Errorf("Expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
		}
	})

	t.Run("no retry on 4xx errors", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("GET", server.URL, nil)

		config := &Config{
			MaxRetries:     2,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		resp, err := retryer.Do(context.Background(), client, req, nil)
		if err != nil {
			t.Fatalf("Expected success (no retry), got error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", resp.StatusCode)
		}

		if atomic.LoadInt32(&attempts) != 1 {
			t.Errorf("Expected 1 attempt (no retry), got %d", atomic.LoadInt32(&attempts))
		}
	})

	t.Run("context cancellation stops retries", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("GET", server.URL, nil)

		// Create context that will be canceled
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Cancel after first attempt
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		config := &Config{
			MaxRetries:     4,
			InitialBackoff: 200 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		_, err := retryer.Do(ctx, client, req, nil)
		if err == nil {
			t.Fatal("Expected error due to context cancellation")
		}

		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", err)
		}

		// Should have made at least one attempt but not all 5
		attemptCount := atomic.LoadInt32(&attempts)
		if attemptCount < 1 || attemptCount >= 5 {
			t.Errorf("Expected 1-4 attempts due to cancellation, got %d", attemptCount)
		}
	})

	t.Run("retry-after header support", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := atomic.AddInt32(&attempts, 1)
			if attempt < 2 {
				w.Header().Set("Retry-After", "1") // 1 second
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("GET", server.URL, nil)

		start := time.Now()
		config := &Config{
			MaxRetries:     2,
			InitialBackoff: 100 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		resp, err := retryer.Do(context.Background(), client, req, nil)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("Expected success, got error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		// Should have waited at least 1 second due to Retry-After header
		if elapsed < 1*time.Second {
			t.Errorf("Expected at least 1s delay (Retry-After), got %v", elapsed)
		}
	})

	t.Run("body bytes preserved across retries", func(t *testing.T) {
		var attempts int32
		expectedBody := []byte("test request body")
		var receivedBodies []string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempt := atomic.AddInt32(&attempts, 1)
			body := make([]byte, len(expectedBody))
			n, _ := r.Body.Read(body)
			receivedBodies = append(receivedBodies, string(body[:n]))

			if attempt < 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("POST", server.URL, nil)

		config := &Config{
			MaxRetries:     2,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		resp, err := retryer.Do(context.Background(), client, req, expectedBody)
		if err != nil {
			t.Fatalf("Expected success, got error: %v", err)
		}
		defer resp.Body.Close()

		if atomic.LoadInt32(&attempts) != 2 {
			t.Errorf("Expected 2 attempts, got %d", atomic.LoadInt32(&attempts))
		}

		// Check that body was preserved across retries
		for i, body := range receivedBodies {
			if body != string(expectedBody) {
				t.Errorf("Attempt %d: expected body %q, got %q", i+1, expectedBody, body)
			}
		}
	})

	t.Run("exhausted attempts", func(t *testing.T) {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		client := &http.Client{}
		req, _ := http.NewRequest("GET", server.URL, nil)

		config := &Config{
			MaxRetries:     2,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		_, err := retryer.Do(context.Background(), client, req, nil)
		if err == nil {
			t.Fatal("Expected error after exhausting attempts")
		}

		if atomic.LoadInt32(&attempts) != 3 {
			t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
		}

		expectedMsg := "all 3 attempts failed for GET"
		if !strings.Contains(fmt.Sprintf("%v", err), expectedMsg) {
			t.Errorf("Expected error message to contain %q, got %q", expectedMsg, err)
		}
	})

	t.Run("invalid parameters", func(t *testing.T) {
		client := &http.Client{}
		req, _ := http.NewRequest("GET", "http://example.com", nil)

		config := &Config{
			MaxRetries:     -1, // Invalid: should be >= 0
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     30 * time.Second,
			BackoffFactor:  2.0,
		}
		retryer := NewRetryer(config, nil)
		_, err := retryer.Do(context.Background(), client, req, nil)
		if err == nil {
			t.Fatal("Expected error for MaxRetries < 0")
		}

		expectedMsg := "validate retry config: max retries must be >= 0"
		if err.Error() != expectedMsg {
			t.Errorf("Expected error %q, got %q", expectedMsg, err.Error())
		}
	})
}

func TestDrainAndClose(t *testing.T) {
	t.Run("handles nil response", func(t *testing.T) {
		// Should not panic
		DrainAndClose(nil)
	})

	t.Run("handles response with nil body", func(t *testing.T) {
		resp := &http.Response{Body: nil}
		// Should not panic
		DrainAndClose(resp)
	})

	t.Run("drains and closes body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(make([]byte, 1024)) // 1KB response
		}))
		defer server.Close()

		resp, err := http.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}

		// Should drain and close without error
		DrainAndClose(resp)
	})
}
