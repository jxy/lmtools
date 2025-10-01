package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() {
	// Initialize logger with request counter enabled for all proxy tests
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)
}

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
		Model:              "gpto3",
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
		Model:              "gpto3",
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

	// Create server config
	config := &Config{
		ArgoBaseURL:        mockProvider.URL,
		ArgoUser:           "testuser",
		Model:              "gpto3",
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
	}

	// Create server with cleanup using testing-optimized configuration
	server, cleanup := NewServerForTesting(config)

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

	// Clean up connections
	cleanup()

	// Cancel context to stop any pending handlers
	serverCancel()

	// Close the mock provider
	mockProvider.Close()

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
		Model:              "gpto3",
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
		Model:              "gpto3",
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

func TestJSONLog_IncomingAnthropicRequest(t *testing.T) {
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("json"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)
	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req AnthropicRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := AnthropicResponse{ID: "msg", Type: "message", Role: "assistant", Content: []AnthropicContentBlock{{Type: "text", Text: "ok"}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockAnthropic.Close()
	config := &Config{Provider: "anthropic", AnthropicAPIKey: "k", AnthropicURL: mockAnthropic.URL, Model: "claude-3-opus-20240229", MaxRequestBodySize: 10 * 1024 * 1024}
	server := NewServer(config)
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	reqBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"Hello"}],"max_tokens":10}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	lines := strings.Split(buf.String(), "\n")
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		msg, _ := m["message"].(string)
		if strings.HasPrefix(msg, "Incoming Anthropic Request: ") {
			payload := strings.TrimPrefix(msg, "Incoming Anthropic Request: ")
			var pj map[string]interface{}
			if json.Unmarshal([]byte(payload), &pj) == nil && pj["model"] != nil {
				found = true
				break
			}
		}
	}
	if !found {
		t.Logf("Captured logs:\n%s", buf.String())
		t.Errorf("missing JSON Incoming Anthropic Request log")
	}
}

func TestJSONLog_IncomingAnthropicStreamingRequest(t *testing.T) {
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("json"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)
	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\n")
		fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		fmt.Fprintf(w, "event: message_stop\n")
		fmt.Fprintf(w, "data: {\"type\":\"message_stop\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer mockAnthropic.Close()
	config := &Config{Provider: "anthropic", AnthropicAPIKey: "k", AnthropicURL: mockAnthropic.URL, Model: "claude-3-opus-20240229", MaxRequestBodySize: 10 * 1024 * 1024}
	server := NewServer(config)
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	reqBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"Hello"}],"max_tokens":10,"stream":true}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	lines := strings.Split(buf.String(), "\n")
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		msg, _ := m["message"].(string)
		if strings.HasPrefix(msg, "Incoming Anthropic Streaming Request: ") {
			payload := strings.TrimPrefix(msg, "Incoming Anthropic Streaming Request: ")
			var pj map[string]interface{}
			if json.Unmarshal([]byte(payload), &pj) == nil && pj["stream"] == true {
				found = true
				break
			}
		}
	}
	if !found {
		t.Logf("Captured logs:\n%s", buf.String())
		t.Errorf("missing JSON Incoming Anthropic Streaming Request log")
	}
}

func TestJSONLog_OutgoingArgoStreamingRequest(t *testing.T) {
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("json"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer mockArgo.Close()
	config := &Config{Provider: "argo", ArgoUser: "u", ArgoBaseURL: mockArgo.URL, Model: "claude-3-haiku-20240307", SmallModel: "claude-3-haiku-20240307", MaxRequestBodySize: 10 * 1024 * 1024}
	server := NewServer(config)
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	reqBody := `{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"Hello"}],"max_tokens":10,"stream":true}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	lines := strings.Split(buf.String(), "\n")
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		msg, _ := m["message"].(string)
		if strings.HasPrefix(msg, "Outgoing Argo Streaming Request: ") {
			payload := strings.TrimPrefix(msg, "Outgoing Argo Streaming Request: ")
			var pj map[string]interface{}
			if json.Unmarshal([]byte(payload), &pj) == nil && pj["model"] != nil {
				found = true
				break
			}
		}
	}
	if !found {
		t.Logf("Captured logs:\n%s", buf.String())
		t.Errorf("missing JSON Outgoing Argo Streaming Request log")
	}
}

func TestJSONLog_ToolCallInfo(t *testing.T) {
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("json"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)
	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := AnthropicResponse{ID: "msg", Type: "message", Role: "assistant", Content: []AnthropicContentBlock{{Type: "tool_use", ID: "id1", Name: "sum", Input: map[string]interface{}{"a": 1, "b": 2}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockAnthropic.Close()
	config := &Config{Provider: "anthropic", AnthropicAPIKey: "k", AnthropicURL: mockAnthropic.URL, Model: "claude-3-opus-20240229", MaxRequestBodySize: 10 * 1024 * 1024}
	server := NewServer(config)
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	reqBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"Hello"}],"max_tokens":10}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	lines := strings.Split(buf.String(), "\n")
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		msg, _ := m["message"].(string)
		// Check for the new format from InfoJSON: "Tool call: sum: {...}"
		// INFO level now shows the truncated JSON structure
		if strings.HasPrefix(msg, "Tool call: sum: ") {
			// Should contain JSON with the tool input
			if strings.Contains(msg, `"a":`) && strings.Contains(msg, `"b":`) {
				found = true
				// Verify it's valid JSON after the label
				jsonPart := strings.TrimPrefix(msg, "Tool call: sum: ")
				var toolData map[string]interface{}
				if err := json.Unmarshal([]byte(jsonPart), &toolData); err == nil {
					// Successfully parsed as JSON - good!
					t.Logf("Tool call log contains valid JSON: %s", jsonPart)
				}
				break
			}
		}
	}
	if !found {
		t.Logf("Captured logs:\n%s", buf.String())
		t.Errorf("missing JSON Tool call info log")
	}
}

func TestLogTimestamps(t *testing.T) {
	// Test that all logs have proper ISO 8601 timestamps
	// Capture the actual logged output
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Reinitialize logger to use the new stderr
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	// Create a new request logger after reinitializing
	// Create a context with counter ID to test request ID logging
	ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(42))
	reqLogger := logger.From(ctx)

	// Log a test message
	reqLogger.Infof("Test message")

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = oldStderr

	// Restore logger to use original stderr
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	output := buf.String()

	// Verify the message content is preserved
	if !strings.Contains(output, "Test message") {
		t.Errorf("Expected output to contain 'Test message', got: %s", output)
	}

	// Verify timestamp format
	if !strings.Contains(output, "[INFO]") {
		t.Errorf("Expected output to contain [INFO], got: %s", output)
	}

	// Verify ISO timestamp format
	if !strings.Contains(output, "2025-") {
		t.Errorf("Expected output to contain timestamp, got: %s", output)
	}

	// Verify request ID is present (format: [#N])
	if !strings.Contains(output, "[#42]") {
		t.Errorf("Expected output to contain [#42], got: %s", output)
	}
}

// Benchmark tests
func BenchmarkLoggingWithRequestID(b *testing.B) {
	ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(1))
	logger := logger.From(ctx)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		logger.Debugf("Benchmark message %d", i)
	}
}

func BenchmarkConcurrentLogging(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(1))
		logger := logger.From(ctx)
		i := 0
		for pb.Next() {
			logger.Debugf("Concurrent benchmark message %d", i)
			i++
		}
	})
}
