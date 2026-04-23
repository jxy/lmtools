package mockserver

import (
	"encoding/json"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newMockConfig(mock *MockServer, model string) config.Config {
	return config.Config{
		Provider:    constants.ProviderArgo,
		ArgoUser:    "testuser",
		Model:       model,
		ProviderURL: mock.URL(),
		Timeout:     5 * time.Second,
	}
}

func decodeJSONMap(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	return result
}

func readBodyString(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	return string(body)
}

func TestMockServer_NativeOpenAIChat(t *testing.T) {
	mock := NewMockServer(
		WithDefaultResponse("Hello from mock server!"),
	)
	defer mock.Close()

	cfg := newMockConfig(mock, "gpt4o")

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Test message")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	result := decodeJSONMap(t, resp)

	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) != 1 {
		t.Fatalf("Expected one OpenAI choice, got %T", result["choices"])
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected choice object, got %T", choices[0])
	}
	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected message object, got %T", choice["message"])
	}
	expectedResponse := "This is a test response from the mock server."
	if got := message["content"]; got != expectedResponse {
		t.Errorf("Expected %q, got %v", expectedResponse, got)
	}

	requests := mock.GetRequests()
	if len(requests) != 1 {
		t.Fatalf("Expected 1 request, got %d", len(requests))
	}

	lastReq := mock.GetLastRequest()
	if !strings.HasSuffix(lastReq.Path, "/v1/chat/completions") {
		t.Errorf("Expected /v1/chat/completions endpoint, got %s", lastReq.Path)
	}
}

func TestMockServer_Embedding(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := newMockConfig(mock, "v3large")
	cfg.Embed = true

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Test embedding")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify embedding response (2D array)
	embedding, ok := result["embedding"].([]interface{})
	if !ok {
		t.Fatalf("Expected embedding array, got %T", result["embedding"])
	}

	if len(embedding) != 1 {
		t.Errorf("Expected 1 embedding vector, got %d", len(embedding))
	}

	// Check the inner array
	if len(embedding) > 0 {
		innerArray, ok := embedding[0].([]interface{})
		if !ok {
			t.Fatalf("Expected inner array, got %T", embedding[0])
		}
		if len(innerArray) != 1536 {
			t.Errorf("Expected 1536 embedding dimensions, got %d", len(innerArray))
		}
	}
}

func TestMockServer_NativeAnthropicChat(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := newMockConfig(mock, "claude-sonnet-4-5")

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Test message")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	result := decodeJSONMap(t, resp)
	content, ok := result["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("Expected one Anthropic content block, got %T", result["content"])
	}
	block, ok := content[0].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected content block object, got %T", content[0])
	}
	expectedResponse := "This is a test response from the mock server."
	if got := block["text"]; got != expectedResponse {
		t.Errorf("Expected %q, got %v", expectedResponse, got)
	}

	lastReq := mock.GetLastRequest()
	if !strings.HasSuffix(lastReq.Path, "/v1/messages") {
		t.Errorf("Expected /v1/messages endpoint, got %s", lastReq.Path)
	}
}

func TestMockServer_LegacyChat(t *testing.T) {
	mock := NewMockServer(
		WithDefaultResponse("Hello from mock server!"),
	)
	defer mock.Close()

	cfg := newMockConfig(mock, "gpt4o")
	cfg.ArgoLegacy = true

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Test message")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	result := decodeJSONMap(t, resp)
	expectedResponse := "This is a test response from the mock server."
	if got := result["response"]; got != expectedResponse {
		t.Errorf("Expected %q, got %v", expectedResponse, got)
	}

	lastReq := mock.GetLastRequest()
	if !strings.HasSuffix(lastReq.Path, "/chat/") {
		t.Errorf("Expected /chat/ endpoint, got %s", lastReq.Path)
	}
}

func TestMockServer_NativeOpenAIStreamChat(t *testing.T) {
	mock := NewMockServer(
		WithDefaultResponse("Streaming response test"),
	)
	defer mock.Close()

	cfg := newMockConfig(mock, "gpt4o")
	cfg.StreamChat = true

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Stream this please")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Expected text/event-stream, got %s", contentType)
	}
	body := readBodyString(t, resp)
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("Expected OpenAI SSE terminator, got %q", body)
	}

	lastReq := mock.GetLastRequest()
	if !strings.HasSuffix(lastReq.Path, "/v1/chat/completions") {
		t.Errorf("Expected /v1/chat/completions endpoint, got %s", lastReq.Path)
	}
}

func TestMockServer_NativeAnthropicStreamChat(t *testing.T) {
	mock := NewMockServer(
		WithDefaultResponse("Streaming response test"),
	)
	defer mock.Close()

	cfg := newMockConfig(mock, "claude-sonnet-4-5")
	cfg.StreamChat = true

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Stream this please")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Expected text/event-stream, got %s", contentType)
	}
	body := readBodyString(t, resp)
	if !strings.Contains(body, "event: message_start") {
		t.Errorf("Expected Anthropic SSE start event, got %q", body)
	}

	lastReq := mock.GetLastRequest()
	if !strings.HasSuffix(lastReq.Path, "/v1/messages") {
		t.Errorf("Expected /v1/messages endpoint, got %s", lastReq.Path)
	}
}

func TestMockServer_LegacyStreamChat(t *testing.T) {
	mock := NewMockServer(
		WithDefaultResponse("Streaming response test"),
	)
	defer mock.Close()

	cfg := newMockConfig(mock, "gpt4o")
	cfg.StreamChat = true
	cfg.ArgoLegacy = true

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Stream this please")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/plain" {
		t.Errorf("Expected text/plain, got %s", contentType)
	}
	body := readBodyString(t, resp)
	if !strings.Contains(body, "Streaming response test") {
		t.Errorf("Expected legacy stream body, got %q", body)
	}

	lastReq := mock.GetLastRequest()
	if !strings.HasSuffix(lastReq.Path, "/streamchat/") {
		t.Errorf("Expected /streamchat/ endpoint, got %s", lastReq.Path)
	}
}

func TestMockServer_CustomResponse(t *testing.T) {
	mock := NewMockServer(
		WithResponseFunc(func(r *http.Request) (interface{}, int, error) {
			// Parse request to determine response
			var reqData map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&reqData); err != nil {
				return nil, 400, err
			}

			response := "Custom response"
			if messages, ok := reqData["messages"].([]interface{}); ok && len(messages) > 0 {
				lastMsg := messages[len(messages)-1]
				if msgMap, ok := lastMsg.(map[string]interface{}); ok {
					var content string
					// Handle both string content and content blocks
					switch c := msgMap["content"].(type) {
					case string:
						content = c
					case []interface{}:
						// Extract text from content blocks
						for _, block := range c {
							if blockMap, ok := block.(map[string]interface{}); ok {
								if blockMap["type"] == "text" {
									if text, ok := blockMap["text"].(string); ok {
										content = text
										break
									}
								}
							}
						}
					}
					if content != "" {
						response = "You said: " + content
					}
				}
			}

			return map[string]interface{}{
				"response": response,
				"custom":   true,
			}, 200, nil
		}),
	)
	defer mock.Close()

	cfg := newMockConfig(mock, "gpt4o")

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "Hello custom")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify custom response
	if result["response"] != "You said: Hello custom" {
		t.Errorf("Expected 'You said: Hello custom', got %v", result["response"])
	}

	if result["custom"] != true {
		t.Errorf("Expected custom=true, got %v", result["custom"])
	}
}

func TestMockServer_ErrorSimulation(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	// Simulate a 500 error
	mock.SimulateError(500, "Internal server error")

	cfg := newMockConfig(mock, "gpt4o")

	req, _, err := core.BuildRequest(cfg.RequestOptions(), "This should fail")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 500 {
		t.Errorf("Expected status 500, got %d", resp.StatusCode)
	}
}

func TestMockServer_RequestCapture(t *testing.T) {
	mock := NewMockServer()
	defer mock.Close()

	cfg := newMockConfig(mock, "gpt4o")
	cfg.System = "You are a helpful assistant"

	// Make multiple requests
	messages := []string{"First", "Second", "Third"}

	for _, msg := range messages {
		req, _, err := core.BuildRequest(cfg.RequestOptions(), msg)
		if err != nil {
			t.Fatalf("Failed to build request: %v", err)
		}

		client := &http.Client{Timeout: cfg.Timeout}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		resp.Body.Close()
	}

	// Verify all requests were captured
	requests := mock.GetRequests()
	if len(requests) != 3 {
		t.Fatalf("Expected 3 requests, got %d", len(requests))
	}

	// Verify request details
	for i, req := range requests {
		if !strings.Contains(req.Body, messages[i]) {
			t.Errorf("Request %d: expected body to contain %s", i, messages[i])
		}

		if req.Method != "POST" {
			t.Errorf("Request %d: expected POST, got %s", i, req.Method)
		}
	}

	// Test reset
	mock.Reset()
	if len(mock.GetRequests()) != 0 {
		t.Error("Expected 0 requests after reset")
	}
}
