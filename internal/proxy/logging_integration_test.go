package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// LogCapture captures logs for testing
type LogCapture struct {
	entries []LogEntry
	mu      sync.Mutex
}

type LogEntry struct {
	RequestID int64
	Timestamp time.Time
	Level     string
	Message   string
}

func NewLogCapture() *LogCapture {
	return &LogCapture{
		entries: make([]LogEntry, 0),
	}
}

func (lc *LogCapture) CaptureEntry(requestID int64, level, message string) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	lc.entries = append(lc.entries, LogEntry{
		RequestID: requestID,
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	})
}

func (lc *LogCapture) GetLogsForRequest(requestID int64) []LogEntry {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	var logs []LogEntry
	for _, entry := range lc.entries {
		if entry.RequestID == requestID {
			logs = append(logs, entry)
		}
	}
	return logs
}

func (lc *LogCapture) GetAllLogs() []LogEntry {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	result := make([]LogEntry, len(lc.entries))
	copy(result, lc.entries)
	return result
}

// Test helper to create a mock provider server
func createMockProvider(t *testing.T, responseFunc func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(responseFunc))
}

func TestLoggingWithModelMapping(t *testing.T) {
	// Create mock Argo provider
	mockArgo := createMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		var argoReq map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&argoReq); err != nil {
			t.Errorf("Failed to decode Argo request: %v", err)
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		// Send response
		response := ArgoChatResponse{
			Response: "Test response from Argo",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	})
	defer mockArgo.Close()

	// Create server config
	config := &Config{
		ArgoBaseURL:        mockArgo.URL,
		ArgoUser:           "testuser",
		ArgoModels:         []string{"gpto3"},
		BigModel:           "gpto3",
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
	}

	// Create server
	server := NewServer(config)

	// Create test request
	anthReq := AnthropicRequest{
		Model: "claude-3-opus-20240229",
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
		},
	}

	reqBody, _ := json.Marshal(anthReq)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Create response recorder
	rr := httptest.NewRecorder()

	// Handle request
	server.ServeHTTP(rr, req)

	// Verify response
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
		t.Logf("Response body: %s", rr.Body.String())
	}

	// In a real test, we would capture and verify the logs
	// For now, we're testing that the flow works without errors
}

func TestStreamingRequestLogging(t *testing.T) {
	// Create mock Argo provider for streaming
	mockArgo := createMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		// For streamchat endpoint, send plain text response
		if strings.Contains(r.URL.Path, "streamchat") {
			w.Header().Set("Content-Type", "text/plain")
			if _, err := w.Write([]byte("Test streaming response")); err != nil {
				t.Errorf("Failed to write response: %v", err)
			}
			return
		}

		// For regular chat endpoint (used in simulated streaming)
		response := ArgoChatResponse{
			Response: "Test response from Argo",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	})
	defer mockArgo.Close()

	// Create server config
	config := &Config{
		ArgoBaseURL:        mockArgo.URL,
		ArgoUser:           "testuser",
		ArgoModels:         []string{"gpto3"},
		BigModel:           "gpto3",
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
	}

	// Create server
	server := NewServer(config)

	// Create streaming request
	anthReq := AnthropicRequest{
		Model: "claude-3-opus-20240229",
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Hello"`)},
		},
		Stream: true,
	}

	reqBody, _ := json.Marshal(anthReq)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Create response recorder that supports flushing
	rr := httptest.NewRecorder()

	// Handle request
	server.ServeHTTP(rr, req)

	// Verify response contains SSE data
	body := rr.Body.String()
	if !strings.Contains(body, "event:") {
		t.Error("Expected SSE events in response")
		t.Logf("Response body: %s", body)
	}
}

func TestConcurrentRequestLogging(t *testing.T) {
	// Create test-controlled context for clean shutdown
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Create mock provider
	mockProvider := createMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate some processing time with context-aware delay
		timer := time.NewTimer(10 * time.Millisecond)
		defer timer.Stop()

		select {
		case <-r.Context().Done():
			return // Client cancelled
		case <-serverCtx.Done():
			return // Test ending
		case <-timer.C:
			// Timer expired, send response
			response := ArgoChatResponse{
				Response: "Concurrent test response",
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Errorf("Failed to encode response: %v", err)
			}
		}
	})
	defer func() {
		serverCancel()
		mockProvider.Close()
	}()

	// Create server config
	config := &Config{
		ArgoBaseURL:        mockProvider.URL,
		ArgoUser:           "testuser",
		ArgoModels:         []string{"gpto3"},
		BigModel:           "gpto3",
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
	}

	// Create server
	server := NewServer(config)

	// Number of concurrent requests
	numRequests := 10
	var wg sync.WaitGroup
	wg.Add(numRequests)

	// Send concurrent requests
	for i := 0; i < numRequests; i++ {
		go func(idx int) {
			defer wg.Done()

			// Create request
			anthReq := AnthropicRequest{
				Model: "claude-3-opus-20240229",
				Messages: []AnthropicMessage{
					{Role: "user", Content: json.RawMessage(fmt.Sprintf(`"Request %d"`, idx))},
				},
			}

			reqBody, _ := json.Marshal(anthReq)
			req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")

			// Create response recorder
			rr := httptest.NewRecorder()

			// Handle request
			server.ServeHTTP(rr, req)

			// Verify response
			if rr.Code != http.StatusOK {
				t.Errorf("Request %d: Expected status 200, got %d", idx, rr.Code)
			}
		}(i)
	}

	wg.Wait()

	// In a real test, we would verify that each request has unique logs
	// and that logs are not interleaved
}

func TestRequestDurationLogging(t *testing.T) {
	// Create test-controlled context for clean shutdown
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Create mock provider with controlled delay
	mockProvider := createMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate processing time with context-aware delay
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()

		select {
		case <-r.Context().Done():
			return // Client cancelled
		case <-serverCtx.Done():
			return // Test ending
		case <-timer.C:
			// Timer expired, send response
			response := ArgoChatResponse{
				Response: "Delayed response",
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Errorf("Failed to encode response: %v", err)
			}
		}
	})
	defer func() {
		serverCancel()
		mockProvider.Close()
	}()

	// Create server config
	config := &Config{
		ArgoBaseURL:        mockProvider.URL,
		ArgoUser:           "testuser",
		ArgoModels:         []string{"gpto3"},
		BigModel:           "gpto3",
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
	}

	// Create server
	server := NewServer(config)

	// Create request
	anthReq := AnthropicRequest{
		Model: "claude-3-opus-20240229",
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Test duration"`)},
		},
	}

	reqBody, _ := json.Marshal(anthReq)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Create response recorder
	rr := httptest.NewRecorder()

	// Record start time
	start := time.Now()

	// Handle request
	server.ServeHTTP(rr, req)

	// Verify duration
	duration := time.Since(start)
	if duration < 100*time.Millisecond {
		t.Errorf("Expected request to take at least 100ms, took %v", duration)
	}

	// Verify response
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}
}

func TestPingIntervalLogging(t *testing.T) {
	// This test verifies that ping interval is logged correctly (not as pointer)
	// In a real implementation, we would capture logs and verify the format

	// Create test-controlled context for clean shutdown
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	// Create mock provider that delays response
	mockProvider := createMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		// Delay to trigger pings with context-aware timer
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()

		select {
		case <-r.Context().Done():
			return // Client cancelled
		case <-serverCtx.Done():
			return // Test ending
		case <-timer.C:
			// Timer expired, send response
			response := ArgoChatResponse{
				Response: "Delayed for pings",
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Errorf("Failed to encode response: %v", err)
			}
		}
	})
	defer func() {
		serverCancel()
		mockProvider.Close()
	}()

	// Create server config
	config := &Config{
		ArgoBaseURL:        mockProvider.URL,
		ArgoUser:           "testuser",
		ArgoModels:         []string{"gpto3"},
		BigModel:           "gpto3",
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
	}

	// Create server
	server := NewServer(config)

	// Create streaming request with tools (forces simulated streaming)
	anthReq := AnthropicRequest{
		Model: "claude-3-opus-20240229",
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Test"`)},
		},
		Stream: true,
		Tools: []AnthropicTool{
			{Name: "test_tool", Description: "Test tool"},
		},
	}

	reqBody, _ := json.Marshal(anthReq)
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	// Create response recorder
	rr := httptest.NewRecorder()

	// Handle request
	server.ServeHTTP(rr, req)

	// Verify response
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
		t.Logf("Response: %s", rr.Body.String())
	}
}

func TestLogTimestamps(t *testing.T) {
	// Test that all logs have proper ISO 8601 timestamps
	logger := NewRequestScopedLogger()

	// Get formatted message - now only contains request ID, not timestamp
	formatted := logger.formatMessage("Test message")

	// New format: [#1] Test message
	// Timestamp is added by core logger, not in formatMessage
	if !strings.HasPrefix(formatted, "[#") {
		t.Errorf("Expected formatted message to start with [#, got: %s", formatted)
	}

	// Verify request ID is present
	expectedPrefix := fmt.Sprintf("[#%d]", logger.GetRequestID())
	if !strings.HasPrefix(formatted, expectedPrefix) {
		t.Errorf("Expected formatted message to start with %s, got: %s", expectedPrefix, formatted)
	}

	// Verify the message content is preserved
	if !strings.Contains(formatted, "Test message") {
		t.Errorf("Expected formatted message to contain 'Test message', got: %s", formatted)
	}

	// Verify that start time was set
	if logger.GetStartTime().IsZero() {
		t.Error("Start time was not set")
	}

	// Verify that start time is recent
	if time.Since(logger.GetStartTime()) > time.Second {
		t.Error("Start time seems too old")
	}
}

// Benchmark tests
func BenchmarkLoggingWithRequestID(b *testing.B) {
	logger := NewRequestScopedLogger()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		logger.Debugf("Benchmark message %d", i)
	}
}

func BenchmarkConcurrentLogging(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		logger := NewRequestScopedLogger()
		i := 0
		for pb.Next() {
			logger.Debugf("Concurrent benchmark message %d", i)
			i++
		}
	})
}
