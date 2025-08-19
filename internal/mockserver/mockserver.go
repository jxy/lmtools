package mockserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// MockServer represents a mock Argo API server for testing
type MockServer struct {
	*httptest.Server
	mu              sync.Mutex
	requests        []MockRequest
	responseFunc    func(*http.Request) (interface{}, int, error)
	defaultModel    string
	defaultResponse string
	simulateErrors  bool
	errorRate       float64
	delay           time.Duration
}

// MockRequest captures details about incoming requests
type MockRequest struct {
	Method    string
	Path      string
	Body      string
	Headers   http.Header
	Timestamp time.Time
}

// MockServerOption configures the mock server
type MockServerOption func(*MockServer)

// WithDefaultModel sets the default model for responses
func WithDefaultModel(model string) MockServerOption {
	return func(ms *MockServer) {
		ms.defaultModel = model
	}
}

// WithDefaultResponse sets the default response content
func WithDefaultResponse(response string) MockServerOption {
	return func(ms *MockServer) {
		ms.defaultResponse = response
	}
}

// WithResponseFunc sets a custom response function
func WithResponseFunc(f func(*http.Request) (interface{}, int, error)) MockServerOption {
	return func(ms *MockServer) {
		ms.responseFunc = f
	}
}

// WithDelay adds artificial delay to responses
func WithDelay(d time.Duration) MockServerOption {
	return func(ms *MockServer) {
		ms.delay = d
	}
}

// WithErrorSimulation enables random error responses
func WithErrorSimulation(rate float64) MockServerOption {
	return func(ms *MockServer) {
		ms.simulateErrors = true
		ms.errorRate = rate
	}
}

// NewMockServer creates a new mock Argo API server
func NewMockServer(opts ...MockServerOption) *MockServer {
	ms := &MockServer{
		defaultModel:    "gpt4o",
		defaultResponse: "This is a mock response",
		requests:        make([]MockRequest, 0),
	}

	// Apply options
	for _, opt := range opts {
		opt(ms)
	}

	// Create the test server
	ms.Server = httptest.NewServer(http.HandlerFunc(ms.handler))

	return ms
}

// handler processes incoming requests
func (ms *MockServer) handler(w http.ResponseWriter, r *http.Request) {
	// Add delay if configured
	if ms.delay > 0 {
		time.Sleep(ms.delay)
	}

	// Capture request details
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	ms.mu.Lock()
	ms.requests = append(ms.requests, MockRequest{
		Method:    r.Method,
		Path:      r.URL.Path,
		Body:      string(body),
		Headers:   r.Header.Clone(),
		Timestamp: time.Now(),
	})
	ms.mu.Unlock()

	// Handle different endpoints
	switch {
	case strings.HasSuffix(r.URL.Path, "/embed/"):
		ms.handleEmbed(w, r, body)
	case strings.HasSuffix(r.URL.Path, "/chat/"):
		ms.handleChat(w, r, body)
	case strings.HasSuffix(r.URL.Path, "/streamchat/"):
		ms.handleStreamChat(w, r, body)
	default:
		http.Error(w, "Unknown endpoint", http.StatusNotFound)
	}
}

// handleEmbed processes embed requests
func (ms *MockServer) handleEmbed(w http.ResponseWriter, r *http.Request, body []byte) {
	// Use custom response function if provided
	if ms.responseFunc != nil {
		// Restore body for custom function to read
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		resp, status, err := ms.responseFunc(r)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		}
		return
	}

	// Parse request only after checking custom response function
	var req core.EmbedRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Default embedding response
	embedding := make([]float64, 1536) // Standard embedding size
	for i := range embedding {
		embedding[i] = float64(i) / 1536.0
	}

	// Count tokens from input array
	tokenCount := 0
	for _, p := range req.Input {
		tokenCount += len(strings.Fields(p))
	}

	resp := map[string]interface{}{
		"embedding": [][]float64{embedding}, // Wrap in outer array as expected
		"model":     req.Model,
		"usage": map[string]int{
			"prompt_tokens": tokenCount,
			"total_tokens":  tokenCount,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// handleChat processes chat requests
func (ms *MockServer) handleChat(w http.ResponseWriter, r *http.Request, body []byte) {
	// Use custom response function if provided
	if ms.responseFunc != nil {
		// Restore body for custom function to read
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		resp, status, err := ms.responseFunc(r)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		}
		return
	}

	// Parse request only after checking custom response function
	var req core.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Generate contextual response based on last user message
	responseText := ms.defaultResponse
	if len(req.Messages) > 0 {
		lastMsg := req.Messages[len(req.Messages)-1]
		// Extract content as string
		var content string
		if err := json.Unmarshal(lastMsg.Content, &content); err == nil {
			if strings.Contains(strings.ToLower(content), "hello") {
				responseText = "Hello! How can I help you today?"
			} else if strings.Contains(strings.ToLower(content), "weather") {
				responseText = "I don't have access to real-time weather data."
			} else if strings.Contains(strings.ToLower(content), "test") {
				responseText = "This is a test response from the mock server."
			}
		}
	}

	resp := map[string]interface{}{
		"response": responseText,
		"model":    req.Model,
		"usage": map[string]int{
			"prompt_tokens":     100,
			"completion_tokens": len(strings.Fields(responseText)),
			"total_tokens":      100 + len(strings.Fields(responseText)),
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// handleStreamChat processes streaming chat requests
func (ms *MockServer) handleStreamChat(w http.ResponseWriter, r *http.Request, body []byte) {
	// Use custom response function if provided
	if ms.responseFunc != nil {
		// Restore body for custom function to read
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		_, status, err := ms.responseFunc(r)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		// For streaming, we expect the responseFunc to handle the streaming itself
		// So we just return after it's done
		return
	}

	// Parse request only after checking custom response function
	var req core.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// For Argo, stream plain text response
	response := ms.defaultResponse

	// Write response as plain text stream
	for _, char := range response {
		fmt.Fprintf(w, "%c", char)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(5 * time.Millisecond) // Simulate streaming
	}

	// Final newline
	fmt.Fprintln(w)
}

// GetRequests returns all captured requests
func (ms *MockServer) GetRequests() []MockRequest {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Return a copy to avoid race conditions
	requests := make([]MockRequest, len(ms.requests))
	copy(requests, ms.requests)
	return requests
}

// GetLastRequest returns the most recent request
func (ms *MockServer) GetLastRequest() *MockRequest {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.requests) == 0 {
		return nil
	}

	req := ms.requests[len(ms.requests)-1]
	return &req
}

// Reset clears all captured requests
func (ms *MockServer) Reset() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.requests = ms.requests[:0]
}

// Close shuts down the mock server
func (ms *MockServer) Close() {
	ms.Server.Close()
}

// URL returns the mock server's URL
func (ms *MockServer) URL() string {
	return ms.Server.URL
}

// SetResponseFunc updates the response function dynamically
func (ms *MockServer) SetResponseFunc(f func(*http.Request) (interface{}, int, error)) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.responseFunc = f
}

// SimulateError makes the next request return an error
func (ms *MockServer) SimulateError(statusCode int, message string) {
	ms.SetResponseFunc(func(r *http.Request) (interface{}, int, error) {
		// Reset after one use
		ms.SetResponseFunc(nil)
		return nil, statusCode, errors.New(message)
	})
}

// SimulateTimeout makes the next request timeout
func (ms *MockServer) SimulateTimeout(duration time.Duration) {
	ms.SetResponseFunc(func(r *http.Request) (interface{}, int, error) {
		time.Sleep(duration)
		return map[string]string{"response": "Should have timed out"}, 200, nil
	})
}
