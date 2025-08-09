//go:build e2e
// +build e2e

package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// E2ETestSuite runs comprehensive end-to-end tests
type E2ETestSuite struct {
	server        *httptest.Server
	config        *Config
	mockProviders map[string]*httptest.Server
}

// SetupE2ETestSuite creates a new test suite
func SetupE2ETestSuite(t *testing.T) *E2ETestSuite {
	// Create mock providers
	openAIMock := httptest.NewServer(NewE2EMockProvider(t, "openai"))
	geminiMock := httptest.NewServer(NewE2EMockProvider(t, "gemini"))
	argoMock := httptest.NewServer(NewE2EMockProvider(t, "argo"))

	// Create config
	config := &Config{
		OpenAIAPIKey:       "test-openai-key",
		GeminiAPIKey:       "test-gemini-key",
		ArgoUser:           "testuser",
		Provider:  "openai",
		SmallModel:         "gpt-4o-mini",
		BigModel:           "gpt-4o",
		OpenAIModels:       []string{"gpt-4o", "gpt-4o-mini"},
		GeminiModels:       []string{"gemini-2.0-flash", "gemini-1.5-pro"},
		ArgoModels:         []string{"gpt4", "claude"},
		MaxRequestBodySize: 10 * 1024 * 1024, // 10MB
	}

	// Set mock URLs in config
	config.OpenAIURL = openAIMock.URL + "/v1/chat/completions"
	config.GeminiURL = geminiMock.URL + "/v1beta/models"
	config.ArgoBaseURL = argoMock.URL

	// Create proxy server
	proxyServer := httptest.NewServer(NewServer(config))

	suite := &E2ETestSuite{
		server: proxyServer,
		config: config,
		mockProviders: map[string]*httptest.Server{
			"openai": openAIMock,
			"gemini": geminiMock,
			"argo":   argoMock,
		},
	}

	// Cleanup function
	t.Cleanup(func() {
		proxyServer.Close()
		openAIMock.Close()
		geminiMock.Close()
		argoMock.Close()
	})

	return suite
}

// E2EMockProvider provides sophisticated mock responses
type E2EMockProvider struct {
	t        *testing.T
	provider string
	// Track requests for verification
	requests []RecordedRequest
}

type RecordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

func NewE2EMockProvider(t *testing.T, provider string) *E2EMockProvider {
	return &E2EMockProvider{
		t:        t,
		provider: provider,
		requests: []RecordedRequest{},
	}
}

func (m *E2EMockProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Record request
	body, _ := io.ReadAll(r.Body)
	m.requests = append(m.requests, RecordedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: r.Header,
		Body:    body,
	})

	// Log request
	m.t.Logf("[%s] %s %s", m.provider, r.Method, r.URL.Path)

	switch m.provider {
	case "openai":
		m.handleOpenAIE2E(w, r, body)
	case "gemini":
		m.handleGeminiE2E(w, r, body)
	case "argo":
		m.handleArgoE2E(w, r, body)
	}
}

func (m *E2EMockProvider) handleOpenAIE2E(w http.ResponseWriter, r *http.Request, body []byte) {
	var req OpenAIRequest
	json.Unmarshal(body, &req)

	// Different responses based on model
	var responseText string
	switch req.Model {
	case "gpt-4o":
		responseText = "This is a response from GPT-4o, the most capable model."
	case "gpt-4o-mini":
		responseText = "This is a response from GPT-4o-mini, optimized for efficiency."
	default:
		responseText = fmt.Sprintf("Response from OpenAI model: %s", req.Model)
	}

	// Handle streaming
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Stream the response word by word
		words := strings.Split(responseText, " ")
		for i, word := range words {
			chunk := map[string]interface{}{
				"id": "chatcmpl-e2e",
				"choices": []map[string]interface{}{
					{
						"delta": map[string]interface{}{
							"content": word,
						},
						"index": 0,
					},
				},
			}
			if i < len(words)-1 {
				chunk["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["content"] = word + " "
			}

			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", string(data))
			w.(http.Flusher).Flush()
			
			// Use context-aware delay
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return // Client cancelled
			case <-timer.C:
				// Continue to next word
			}
		}

		// Send completion
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-e2e\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
		return
	}

	// Handle tool calls
	if len(req.Tools) > 0 && strings.Contains(req.Messages[len(req.Messages)-1].Content.(string), "calculate") {
		// Simulate tool use
		resp := OpenAIResponse{
			ID:    "chatcmpl-e2e-tools",
			Model: req.Model,
			Choices: []OpenAIChoice{
				{
					Message: OpenAIMessage{
						Role: "assistant",
						ToolCalls: []ToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: FunctionCall{
									Name:      "calculator",
									Arguments: `{"expression": "25 * 4"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: &OpenAIUsage{
				PromptTokens:     20,
				CompletionTokens: 15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Normal response
	resp := OpenAIResponse{
		ID:    "chatcmpl-e2e",
		Model: req.Model,
		Choices: []OpenAIChoice{
			{
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: responseText,
				},
				FinishReason: "stop",
			},
		},
		Usage: &OpenAIUsage{
			PromptTokens:     countTokens(req),
			CompletionTokens: len(strings.Split(responseText, " ")) * 2,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *E2EMockProvider) handleGeminiE2E(w http.ResponseWriter, r *http.Request, body []byte) {
	var req GeminiRequest
	json.Unmarshal(body, &req)

	// Extract model from URL
	model := "gemini-2.0-flash"
	if strings.Contains(r.URL.Path, "gemini-1.5-pro") {
		model = "gemini-1.5-pro"
	}

	var responseText string
	switch model {
	case "gemini-1.5-pro":
		responseText = "This is a response from Gemini 1.5 Pro with advanced capabilities."
	case "gemini-2.0-flash":
		responseText = "This is a response from Gemini 2.0 Flash, optimized for speed."
	default:
		responseText = "Response from Gemini model."
	}

	// Handle streaming
	if strings.Contains(r.URL.Path, "streamGenerateContent") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Stream response in chunks
		chunks := []string{"This is ", "a response ", "from Gemini ", model}
		for i, chunk := range chunks {
			data := map[string]interface{}{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": chunk},
							},
						},
					},
				},
			}

			if i == len(chunks)-1 {
				data["candidates"].([]map[string]interface{})[0]["finishReason"] = "STOP"
			}

			jsonData, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", string(jsonData))
			w.(http.Flusher).Flush()
			
			// Use context-aware delay
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return // Client cancelled
			case <-timer.C:
				// Continue to next chunk
			}
		}
		return
	}

	// Handle function calls
	if len(req.Tools) > 0 {
		// Check if we should use a tool
		lastMessage := req.Contents[len(req.Contents)-1]
		needsCalculator := false
		for _, part := range lastMessage.Parts {
			if strings.Contains(part.Text, "calculate") || strings.Contains(part.Text, "multiply") {
				needsCalculator = true
				break
			}
		}

		if needsCalculator {
			resp := GeminiResponse{
				Candidates: []GeminiCandidate{
					{
						Content: GeminiContent{
							Role: "model",
							Parts: []GeminiPart{
								{
									FunctionCall: &GeminiFunctionCall{
										Name: "calculator",
										Args: map[string]interface{}{
											"expression": "25 * 4",
										},
									},
								},
							},
						},
						FinishReason: "STOP",
					},
				},
				UsageMetadata: &GeminiUsage{
					PromptTokenCount:     25,
					CandidatesTokenCount: 10,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	// Normal response
	resp := GeminiResponse{
		Candidates: []GeminiCandidate{
			{
				Content: GeminiContent{
					Role: "model",
					Parts: []GeminiPart{
						{Text: responseText},
					},
				},
				FinishReason: "STOP",
			},
		},
		UsageMetadata: &GeminiUsage{
			PromptTokenCount:     30,
			CandidatesTokenCount: len(strings.Split(responseText, " ")) * 2,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (m *E2EMockProvider) handleArgoE2E(w http.ResponseWriter, r *http.Request, body []byte) {
	var req ArgoChatRequest
	json.Unmarshal(body, &req)

	responseText := fmt.Sprintf("Argo response for user %s using model %s", req.User, req.Model)

	// Handle streaming
	if strings.Contains(r.URL.Path, "streamchat") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		// Stream character by character
		for _, char := range responseText {
			fmt.Fprintf(w, "%c", char)
			w.(http.Flusher).Flush()
			
			// Use context-aware delay
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return // Client cancelled
			case <-timer.C:
				// Continue to next character
			}
		}
		return
	}

	// Normal response
	resp := ArgoChatResponse{
		Response: responseText,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Test functions

func TestE2EBasicChat(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	tests := []struct {
		name           string
		model          string
		message        string
		expectProvider string
		expectContent  string
	}{
		{
			name:           "Haiku maps to small model",
			model:          "claude-3-haiku-20240307",
			message:        "Hello",
			expectProvider: "openai",
			expectContent:  "GPT-4o-mini",
		},
		{
			name:           "Sonnet maps to big model",
			model:          "claude-3-5-sonnet-20241022",
			message:        "Hello",
			expectProvider: "openai",
			expectContent:  "GPT-4o",
		},
		{
			name:           "Direct Gemini model",
			model:          "gemini-2.0-flash",
			message:        "Hello",
			expectProvider: "gemini",
			expectContent:  "Gemini 2.0 Flash",
		},
		{
			name:           "Direct Argo model",
			model:          "gpt4",
			message:        "Hello",
			expectProvider: "argo",
			expectContent:  "Argo response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := AnthropicRequest{
				Model:     tt.model,
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"` + tt.message + `"`),
					},
				},
			}

			reqBody, _ := json.Marshal(req)
			resp, err := http.Post(
				suite.server.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
			}

			var anthResp AnthropicResponse
			if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Verify response
			if len(anthResp.Content) == 0 {
				t.Fatal("Expected content in response")
			}

			if !strings.Contains(anthResp.Content[0].Text, tt.expectContent) {
				t.Errorf("Expected content to contain %q, got %q", tt.expectContent, anthResp.Content[0].Text)
			}

			// Verify model is preserved
			if anthResp.Model != tt.model {
				t.Errorf("Expected model %s, got %s", tt.model, anthResp.Model)
			}
		})
	}
}

func TestE2EStreamingResponses(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	req := AnthropicRequest{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Tell me a story"`),
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		suite.server.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Verify streaming headers
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", ct)
	}

	// Collect events
	var events []string
	var textChunks []string
	scanner := NewSSEScanner(resp.Body)

	for scanner.Scan() {
		event := scanner.Event()
		data := scanner.Data()

		if event != "" {
			events = append(events, event)
		}

		// Extract text from content_block_delta events
		if event == "content_block_delta" {
			var delta map[string]interface{}
			if err := json.Unmarshal([]byte(data), &delta); err == nil {
				if deltaBlock, ok := delta["delta"].(map[string]interface{}); ok {
					if text, ok := deltaBlock["text"].(string); ok {
						textChunks = append(textChunks, text)
					}
				}
			}
		}
	}

	// Verify we got expected events
	expectedEvents := []string{
		"message_start",
		"content_block_start",
		"ping",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}

	eventMap := make(map[string]bool)
	for _, e := range events {
		eventMap[e] = true
	}

	for _, expected := range expectedEvents {
		if !eventMap[expected] {
			t.Errorf("Missing expected event: %s", expected)
		}
	}

	// Verify we got text content
	fullText := strings.Join(textChunks, "")
	if !strings.Contains(fullText, "GPT-4o-mini") {
		t.Errorf("Expected streamed text to contain model info, got: %s", fullText)
	}
}

func TestE2EToolUse(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	// Test with each provider
	providers := []struct {
		model    string
		provider string
	}{
		{"claude-3-sonnet-20240229", "openai"},
		{"gemini-1.5-pro", "gemini"},
	}

	for _, p := range providers {
		t.Run(p.provider, func(t *testing.T) {
			req := AnthropicRequest{
				Model:     p.model,
				MaxTokens: 200,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Please calculate 25 times 4"`),
					},
				},
				Tools: []AnthropicTool{
					{
						Name:        "calculator",
						Description: "A simple calculator",
						InputSchema: map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"expression": map[string]interface{}{
									"type":        "string",
									"description": "Mathematical expression to evaluate",
								},
							},
							"required": []string{"expression"},
						},
					},
				},
			}

			reqBody, _ := json.Marshal(req)
			resp, err := http.Post(
				suite.server.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
			}

			var anthResp AnthropicResponse
			if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Verify tool use in response
			hasToolUse := false
			for _, content := range anthResp.Content {
				if content.Type == "tool_use" {
					hasToolUse = true
					if content.Name != "calculator" {
						t.Errorf("Expected tool name 'calculator', got %s", content.Name)
					}
					// Verify the tool input
					if content.Input != nil {
						if expr, ok := content.Input["expression"].(string); ok {
							if !strings.Contains(expr, "25") || !strings.Contains(expr, "4") {
								t.Errorf("Expected expression to contain '25' and '4', got %s", expr)
							}
						}
					}
				}
			}

			if !hasToolUse {
				t.Error("Expected tool use in response")
			}
		})
	}
}

func TestE2EComplexContent(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	// Test with content blocks including tool results
	req := AnthropicRequest{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 200,
		Messages: []AnthropicMessage{
			{
				Role: RoleUser,
				Content: json.RawMessage(`[
					{"type": "text", "text": "I used the calculator tool."},
					{"type": "tool_result", "tool_use_id": "calc_123", "content": "Result: 100"}
				]`),
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		suite.server.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var anthResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify response
	if len(anthResp.Content) == 0 {
		t.Fatal("Expected content in response")
	}
}

func TestE2ETokenCounting(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	req := AnthropicTokenCountRequest{
		Model:  "claude-3-haiku-20240307",
		System: json.RawMessage(`"You are a helpful assistant."`),
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"This is a test message with multiple words to count tokens accurately."`),
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		suite.server.URL+"/v1/messages/count_tokens",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp AnthropicTokenCountResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify we got a reasonable token count
	if tokenResp.InputTokens < 5 {
		t.Errorf("Expected at least 5 tokens, got %d", tokenResp.InputTokens)
	}
}

func TestE2EErrorHandling(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	tests := []struct {
		name       string
		request    interface{}
		endpoint   string
		expectCode int
		expectErr  string
	}{
		{
			name:       "Invalid JSON",
			request:    "invalid json",
			endpoint:   "/v1/messages",
			expectCode: http.StatusBadRequest,
			expectErr:  "Invalid request body",
		},
		{
			name: "Missing required fields",
			request: map[string]interface{}{
				"messages": []interface{}{},
			},
			endpoint:   "/v1/messages",
			expectCode: http.StatusBadRequest,
			expectErr:  "Messages array cannot be empty",
		},
		{
			name:       "Invalid endpoint",
			request:    AnthropicRequest{},
			endpoint:   "/v1/invalid",
			expectCode: http.StatusNotFound,
			expectErr:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reqBody []byte
			if str, ok := tt.request.(string); ok {
				reqBody = []byte(str)
			} else {
				reqBody, _ = json.Marshal(tt.request)
			}

			resp, err := http.Post(
				suite.server.URL+tt.endpoint,
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.expectCode {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("Expected status %d, got %d: %s", tt.expectCode, resp.StatusCode, string(body))
			}

			if tt.expectErr != "" {
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), tt.expectErr) {
					t.Errorf("Expected error to contain %q, got %s", tt.expectErr, string(body))
				}
			}
		})
	}
}

func TestE2EPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	suite := SetupE2ETestSuite(t)

	// Measure response time for different models
	models := []string{
		"claude-3-haiku-20240307",
		"claude-3-sonnet-20240229",
		"gemini-2.0-flash",
		"gpt4",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			req := AnthropicRequest{
				Model:     model,
				MaxTokens: 50,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Say hello"`),
					},
				},
			}

			reqBody, _ := json.Marshal(req)

			// Warm up
			http.Post(suite.server.URL+"/v1/messages", "application/json", bytes.NewReader(reqBody))

			// Measure
			start := time.Now()
			resp, err := http.Post(
				suite.server.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			resp.Body.Close()

			t.Logf("Response time for %s: %v", model, elapsed)

			// Verify reasonable response time (adjust as needed)
			if elapsed > 500*time.Millisecond {
				t.Errorf("Response time too slow: %v", elapsed)
			}
		})
	}
}

// Helper functions

func countTokens(req OpenAIRequest) int {
	// Simple token estimation
	tokens := 0
	for _, msg := range req.Messages {
		switch v := msg.Content.(type) {
		case string:
			tokens += len(strings.Split(v, " ")) * 2
		}
	}
	return tokens
}

// SSEScanner helps parse Server-Sent Events
type SSEScanner struct {
	scanner      *bufio.Scanner
	currentEvent string
	currentData  string
}

func NewSSEScanner(r io.Reader) *SSEScanner {
	return &SSEScanner{
		scanner: bufio.NewScanner(r),
	}
}

func (s *SSEScanner) Scan() bool {
	s.currentEvent = ""
	s.currentData = ""

	for s.scanner.Scan() {
		line := s.scanner.Text()

		if line == "" {
			// Empty line signals end of event
			if s.currentEvent != "" || s.currentData != "" {
				return true
			}
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			s.currentEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			s.currentData = strings.TrimPrefix(line, "data: ")
			if s.currentData == "[DONE]" {
				return false
			}
		}
	}

	return false
}

func (s *SSEScanner) Event() string {
	return s.currentEvent
}

func (s *SSEScanner) Data() string {
	return s.currentData
}

// Benchmark tests

func BenchmarkE2ESimpleChat(b *testing.B) {
	suite := SetupE2ETestSuite(&testing.T{})
	defer func() {
		suite.server.Close()
		for _, mock := range suite.mockProviders {
			mock.Close()
		}
	}()

	req := AnthropicRequest{
		Model:     "claude-3-haiku-20240307",
		MaxTokens: 50,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	reqBody, _ := json.Marshal(req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(
			suite.server.URL+"/v1/messages",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			b.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
}

// Integration with real APIs (disabled by default)

func TestE2ERealAPIs(t *testing.T) {
	if os.Getenv("E2E_REAL_APIS") != "true" {
		t.Skip("Skipping real API tests. Set E2E_REAL_APIS=true to enable")
	}

	// This test would use real API keys and endpoints
	// Only run in CI/CD or with proper credentials
	config := &Config{
		OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey: os.Getenv("GEMINI_API_KEY"),
		ArgoUser:     os.Getenv("ARGO_USER"),
		// ... other config
	}

	if config.OpenAIAPIKey == "" || config.GeminiAPIKey == "" {
		t.Skip("Missing API keys for real API tests")
	}

	// Test with real APIs...
}
