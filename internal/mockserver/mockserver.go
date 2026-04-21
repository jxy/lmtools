package mockserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// MockServer represents a mock server for Argo legacy and Argo native compatibility endpoints.
type MockServer struct {
	*httptest.Server
	mu              sync.Mutex
	requests        []MockRequest
	responseFunc    func(*http.Request) (interface{}, int, error)
	defaultModel    string
	defaultResponse string
	simulateErrors  bool
	errorRate       float64
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

// WithErrorSimulation enables random error responses
func WithErrorSimulation(rate float64) MockServerOption {
	return func(ms *MockServer) {
		ms.simulateErrors = true
		ms.errorRate = rate
	}
}

// NewMockServer creates a new mock server.
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
	case strings.HasSuffix(r.URL.Path, "/messages/count_tokens"):
		ms.handleCountTokens(w, r, body)
	case strings.HasSuffix(r.URL.Path, "/chat/"):
		ms.handleChat(w, r, body)
	case strings.HasSuffix(r.URL.Path, "/streamchat/"):
		ms.handleStreamChat(w, r, body)
	case strings.HasSuffix(r.URL.Path, "/messages"):
		// Handle Anthropic-style messages endpoint
		ms.handleChat(w, r, body)
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		// Handle OpenAI-style chat completions endpoint
		ms.handleChat(w, r, body)
	default:
		http.Error(w, "Unknown endpoint", http.StatusNotFound)
	}
}

func (ms *MockServer) currentResponseFunc() func(*http.Request) (interface{}, int, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.responseFunc
}

// handleEmbed processes embed requests
func (ms *MockServer) handleEmbed(w http.ResponseWriter, r *http.Request, body []byte) {
	// Use custom response function if provided
	if responseFunc := ms.currentResponseFunc(); responseFunc != nil {
		// Restore body for custom function to read
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		resp, status, err := responseFunc(r)
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
	var req struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Default embedding response
	embedding := make([]float64, 1536) // Standard embedding size for text-embedding-ada-002
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

func extractTextContent(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		for _, block := range c {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if blockType, _ := blockMap["type"].(string); blockType != "text" {
				continue
			}
			if text, ok := blockMap["text"].(string); ok {
				return text
			}
		}
	}
	return ""
}

func mockResponseForRequest(defaultResponse, defaultModel string, reqData map[string]interface{}) (string, string) {
	responseText := defaultResponse
	model := defaultModel

	if m, ok := reqData["model"].(string); ok {
		model = m
	}

	messages, ok := reqData["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		return responseText, model
	}

	lastMsg, ok := messages[len(messages)-1].(map[string]interface{})
	if !ok {
		return responseText, model
	}

	content := extractTextContent(lastMsg["content"])
	switch {
	case strings.Contains(strings.ToLower(content), "hello"):
		responseText = "Hello! How can I help you today?"
	case strings.Contains(strings.ToLower(content), "weather"):
		responseText = "I don't have access to real-time weather data."
	case strings.Contains(strings.ToLower(content), "test"):
		responseText = "This is a test response from the mock server."
	}

	return responseText, model
}

func wordCount(text string) int {
	return len(strings.Fields(text))
}

func isStreamRequest(path string, reqData map[string]interface{}) bool {
	if strings.HasSuffix(path, "/streamchat/") {
		return true
	}
	stream, ok := reqData["stream"].(bool)
	return ok && stream
}

func (ms *MockServer) writeOpenAIStream(w http.ResponseWriter, responseText, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	chunks := []map[string]interface{}{
		{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"role":    "assistant",
						"content": responseText,
					},
					"finish_reason": nil,
				},
			},
		},
		{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index":         0,
					"delta":         map[string]interface{}{},
					"finish_reason": "stop",
				},
			},
		},
	}

	for _, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			http.Error(w, "Failed to encode stream chunk", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (ms *MockServer) writeAnthropicStream(w http.ResponseWriter, responseText, model string) {
	outputTokens := wordCount(responseText)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events := []struct {
		event string
		data  map[string]interface{}
	}{
		{
			event: "message_start",
			data: map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":            "msg_mock",
					"type":          "message",
					"role":          "assistant",
					"content":       []interface{}{},
					"model":         model,
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage": map[string]int{
						"input_tokens":  100,
						"output_tokens": 0,
					},
				},
			},
		},
		{
			event: "content_block_start",
			data: map[string]interface{}{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]interface{}{
					"type": "text",
					"text": "",
				},
			},
		},
		{
			event: "content_block_delta",
			data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": responseText,
				},
			},
		},
		{
			event: "content_block_stop",
			data: map[string]interface{}{
				"type":  "content_block_stop",
				"index": 0,
			},
		},
		{
			event: "message_delta",
			data: map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason":   "end_turn",
					"stop_sequence": nil,
				},
				"usage": map[string]int{
					"output_tokens": outputTokens,
				},
			},
		},
		{
			event: "message_stop",
			data: map[string]interface{}{
				"type": "message_stop",
			},
		},
	}

	for _, evt := range events {
		encoded, err := json.Marshal(evt.data)
		if err != nil {
			http.Error(w, "Failed to encode stream event", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "event: %s\n", evt.event)
		fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
}

// handleChat processes chat requests.
func (ms *MockServer) handleChat(w http.ResponseWriter, r *http.Request, body []byte) {
	// Use custom response function if provided
	if responseFunc := ms.currentResponseFunc(); responseFunc != nil {
		// Restore body for custom function to read
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		resp, status, err := responseFunc(r)
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

	var reqData map[string]interface{}
	if err := json.Unmarshal(body, &reqData); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	responseText, model := mockResponseForRequest(ms.defaultResponse, ms.defaultModel, reqData)

	if isStreamRequest(r.URL.Path, reqData) {
		switch {
		case strings.Contains(r.URL.Path, "/v1/chat/completions"):
			ms.writeOpenAIStream(w, responseText, model)
		case strings.Contains(r.URL.Path, "/v1/messages"):
			ms.writeAnthropicStream(w, responseText, model)
		default:
			ms.handleStreamChat(w, r, body)
		}
		return
	}

	outputTokens := wordCount(responseText)
	var resp map[string]interface{}
	if strings.Contains(r.URL.Path, "/v1/chat/completions") {
		// OpenAI format
		resp = map[string]interface{}{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": responseText,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     100,
				"completion_tokens": outputTokens,
				"total_tokens":      100 + outputTokens,
			},
		}
	} else if strings.Contains(r.URL.Path, "/v1/messages") {
		// Anthropic format
		resp = map[string]interface{}{
			"id":    "msg_mock",
			"type":  "message",
			"role":  "assistant",
			"model": model,
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": responseText,
				},
			},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage": map[string]int{
				"input_tokens":  100,
				"output_tokens": outputTokens,
			},
		}
	} else {
		// Argo/default format
		resp = map[string]interface{}{
			"response": responseText,
			"model":    model,
			"usage": map[string]int{
				"prompt_tokens":     100,
				"completion_tokens": outputTokens,
				"total_tokens":      100 + outputTokens,
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// handleStreamChat processes streaming chat requests
func (ms *MockServer) handleStreamChat(w http.ResponseWriter, r *http.Request, body []byte) {
	// Use custom response function if provided
	if responseFunc := ms.currentResponseFunc(); responseFunc != nil {
		// Restore body for custom function to read
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		_, status, err := responseFunc(r)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		// For streaming, we expect the responseFunc to handle the streaming itself
		// So we just return after it's done
		return
	}

	// Parse request only after checking custom response function
	var reqData map[string]interface{}
	if err := json.Unmarshal(body, &reqData); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	responseText, _ := mockResponseForRequest(ms.defaultResponse, ms.defaultModel, reqData)

	// Write response as plain text
	fmt.Fprint(w, responseText)
	fmt.Fprintln(w) // Final newline
}

func (ms *MockServer) handleCountTokens(w http.ResponseWriter, r *http.Request, body []byte) {
	var reqData map[string]interface{}
	if err := json.Unmarshal(body, &reqData); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	tokenCount := 0
	if system, ok := reqData["system"].(string); ok {
		tokenCount += wordCount(system)
	}
	if messages, ok := reqData["messages"].([]interface{}); ok {
		for _, raw := range messages {
			msgMap, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			tokenCount += wordCount(extractTextContent(msgMap["content"]))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int{"input_tokens": tokenCount}); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
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
