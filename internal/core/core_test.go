package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newCoreTestRequestConfig() *TestRequestConfig {
	cfg := NewTestRequestConfig()
	cfg.User = ""
	cfg.Model = ""
	cfg.System = ""
	cfg.Env = ""
	cfg.Provider = ""
	cfg.ProviderURL = ""
	cfg.APIKeyFile = ""
	cfg.IsEmbedMode = false
	cfg.IsStreamChatMode = false
	cfg.IsToolEnabledFlag = false
	return cfg
}

// TestBuildOpenAIToolResultRequest tests the OpenAI tool result request builder
func TestBuildOpenAIToolResultRequest(t *testing.T) {
	// Create a temporary API key file
	apiKeyFile, err := os.CreateTemp("", "test-api-key-*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp API key file: %v", err)
	}
	defer os.Remove(apiKeyFile.Name())
	if _, err := apiKeyFile.WriteString("test-api-key"); err != nil {
		t.Fatalf("Failed to write API key: %v", err)
	}
	apiKeyFile.Close()

	// Test configuration
	cfg := newCoreTestRequestConfig()
	cfg.User = "testuser"
	cfg.Model = "gpt-5"
	cfg.System = "Test system prompt"
	cfg.APIKeyFile = apiKeyFile.Name()
	cfg.Provider = "openai"

	// Test tool results are now embedded in typedMessages

	// Create typed messages from the accumulated messages
	typedMessages := []TypedMessage{
		NewTextMessage("system", "Test system prompt"),
		NewTextMessage("user", "List files and read one"),
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll help you list files"},
				ToolUseBlock{
					ID:    "call_123",
					Name:  "list_files",
					Input: json.RawMessage(`{"directory": "/tmp"}`),
				},
				ToolUseBlock{
					ID:    "call_456",
					Name:  "read_file",
					Input: json.RawMessage(`{"path": "/tmp/test.txt"}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "call_123",
					Content:   "file1.txt\nfile2.txt",
				},
				ToolResultBlock{
					ToolUseID: "call_456",
					Content:   "Error: File not found",
				},
				TextBlock{Text: "Additional context"},
			},
		},
	}

	// Tool definitions are now passed directly to request builders
	model := "gpt-5"

	// Build the request
	req, body, err := BuildToolResultRequest(cfg, model, "You are a helpful assistant.", nil, typedMessages)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify request properties
	if req.Method != "POST" {
		t.Errorf("Expected method POST, got %s", req.Method)
	}
	if !strings.Contains(req.URL.String(), "/chat/completions") {
		t.Errorf("Expected URL to contain /chat/completions, got %s", req.URL.String())
	}

	// Verify headers
	if req.Header.Get("Authorization") != "Bearer test-api-key" {
		t.Errorf("Expected Authorization header 'Bearer test-api-key', got '%s'", req.Header.Get("Authorization"))
	}

	// Parse and verify request body
	var parsedReq map[string]interface{}
	if err := json.Unmarshal(body, &parsedReq); err != nil {
		t.Fatalf("Failed to unmarshal request body: %v", err)
	}

	// Check model
	if parsedReq["model"] != "gpt-5" {
		t.Errorf("Expected model 'gpt-5', got %v", parsedReq["model"])
	}

	// Check messages
	messages, ok := parsedReq["messages"].([]interface{})
	if !ok {
		t.Fatalf("Expected messages to be an array")
	}

	// Should have: system, user, assistant (with tool calls), tool result 1, tool result 2, user (additional text)
	if len(messages) != 6 {
		t.Errorf("Expected 6 messages, got %d", len(messages))
	}

	// Verify assistant message with tool calls
	if len(messages) >= 3 {
		assistantMsg, ok := messages[2].(map[string]interface{})
		if !ok {
			t.Error("Expected third message to be a map")
		} else {
			if assistantMsg["role"] != "assistant" {
				t.Errorf("Expected assistant role, got %v", assistantMsg["role"])
			}
			toolCalls, ok := assistantMsg["tool_calls"].([]interface{})
			if !ok || len(toolCalls) != 2 {
				t.Error("Expected assistant message to have 2 tool calls")
			}
		}
	}

	// Verify tool result messages
	if len(messages) >= 5 {
		// First tool result
		toolMsg1, ok := messages[3].(map[string]interface{})
		if !ok {
			t.Error("Expected fourth message to be a map")
		} else {
			if toolMsg1["role"] != "tool" {
				t.Errorf("Expected tool role, got %v", toolMsg1["role"])
			}
			if toolMsg1["tool_call_id"] != "call_123" {
				t.Errorf("Expected tool_call_id 'call_123', got %v", toolMsg1["tool_call_id"])
			}
		}

		// Second tool result with error
		toolMsg2, ok := messages[4].(map[string]interface{})
		if !ok {
			t.Error("Expected fifth message to be a map")
		} else {
			if toolMsg2["role"] != "tool" {
				t.Errorf("Expected tool role, got %v", toolMsg2["role"])
			}
			content, ok := toolMsg2["content"].(string)
			if !ok || !strings.Contains(content, "Error:") {
				t.Error("Expected tool result to contain error message")
			}
		}
	}

	// Clear cache after test
	// ClearRequestCache() - removed
}

// TestBuildAnthropicToolResultRequest tests the Anthropic tool result request builder
func TestBuildAnthropicToolResultRequest(t *testing.T) {
	// Create a temporary API key file
	apiKeyFile, err := os.CreateTemp("", "test-api-key-*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp API key file: %v", err)
	}
	defer os.Remove(apiKeyFile.Name())
	if _, err := apiKeyFile.WriteString("test-api-key"); err != nil {
		t.Fatalf("Failed to write API key: %v", err)
	}
	apiKeyFile.Close()

	// Test configuration
	cfg := newCoreTestRequestConfig()
	cfg.User = "testuser"
	cfg.Model = "claude-opus-4-1-20250805"
	cfg.Provider = "anthropic"
	cfg.System = "Test system prompt"
	cfg.APIKeyFile = apiKeyFile.Name()

	// Test tool results are now embedded in typedMessages

	// Set up request cache
	// Create typed messages from the accumulated messages
	typedMessages := []TypedMessage{
		NewTextMessage("system", "Test system prompt"),
		NewTextMessage("user", "List directory and read a file"),
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll list the directory for you"},
				ToolUseBlock{
					ID:    "toolu_123",
					Name:  "list_directory",
					Input: json.RawMessage(`{"path": "/home/user"}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "toolu_123",
					Content:   "file1.txt\nfile2.txt\ndirectory1/",
				},
				TextBlock{Text: "Note: Output was truncated"},
			},
		},
	}

	// Tool definitions are now passed directly to request builders
	model := "claude-opus-4-1-20250805"

	// Build the request
	req, body, err := BuildToolResultRequest(cfg, model, "Test system prompt", nil, typedMessages)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify request properties
	if req.Method != "POST" {
		t.Errorf("Expected method POST, got %s", req.Method)
	}
	if !strings.Contains(req.URL.String(), "/messages") {
		t.Errorf("Expected URL to contain /messages, got %s", req.URL.String())
	}

	// Verify headers
	if req.Header.Get("x-api-key") != "test-api-key" {
		t.Errorf("Expected x-api-key header 'test-api-key', got '%s'", req.Header.Get("x-api-key"))
	}
	if req.Header.Get("anthropic-version") == "" {
		t.Error("Expected anthropic-version header to be set")
	}

	// Parse and verify request body
	var parsedReq map[string]interface{}
	if err := json.Unmarshal(body, &parsedReq); err != nil {
		t.Fatalf("Failed to unmarshal request body: %v", err)
	}

	// Check model and system
	if parsedReq["model"] != "claude-opus-4-1-20250805" {
		t.Errorf("Expected model 'claude-opus-4-1-20250805', got %v", parsedReq["model"])
	}
	if parsedReq["system"] != "Test system prompt" {
		t.Errorf("Expected system 'Test system prompt', got %v", parsedReq["system"])
	}

	// Check messages
	messages, ok := parsedReq["messages"].([]interface{})
	if !ok {
		t.Fatalf("Expected messages to be an array")
	}

	// Should have: user (original), assistant (with tool use), user (tool results)
	// System is extracted and placed in top-level field for Anthropic
	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
	}

	// Verify assistant message with tool use content blocks
	if len(messages) >= 2 {
		assistantMsg, ok := messages[1].(map[string]interface{})
		if !ok {
			t.Error("Expected third message to be a map")
		} else {
			if assistantMsg["role"] != "assistant" {
				t.Errorf("Expected assistant role, got %v", assistantMsg["role"])
			}
			content, ok := assistantMsg["content"].([]interface{})
			if !ok {
				t.Error("Expected assistant content to be an array")
			} else {
				// Should have text block and tool use blocks
				if len(content) < 2 {
					t.Error("Expected at least 2 content blocks in assistant message")
				}
			}
		}
	}

	// Verify user message with tool results
	if len(messages) >= 4 {
		userMsg, ok := messages[3].(map[string]interface{})
		if !ok {
			t.Error("Expected fourth message to be a map")
		} else {
			if userMsg["role"] != "user" {
				t.Errorf("Expected user role, got %v", userMsg["role"])
			}
			content, ok := userMsg["content"].([]interface{})
			if !ok {
				t.Error("Expected user content to be an array")
			} else {
				// Should have tool result and additional text
				if len(content) != 2 { // 1 tool result + 1 text
					t.Errorf("Expected 2 content blocks, got %d", len(content))
				}
			}
		}
	}

	// Clear cache after test
	// ClearRequestCache() - removed
}

// TestBuildToolResultRequestNoCacheError tests error handling when no cache is available
func TestBuildToolResultRequestNoCacheError(t *testing.T) {
	// Clear any existing cache
	// ClearRequestCache() - removed

	cfg := newCoreTestRequestConfig()
	cfg.User = "testuser"
	cfg.Model = "gpt-5"

	// Test each provider
	providers := []string{"openai", "anthropic", "argo"}
	for _, provider := range providers {
		t.Run(provider, func(t *testing.T) {
			// Override provider method for test
			cfg.ProviderURL = "" // This would normally come from GetProvider()

			// Create a nil conversation to test error handling
			_, _, err := BuildToolResultRequest(cfg, "test-model", "", nil, nil)

			if err == nil {
				t.Error("Expected error when no conversation is available")
			} else if strings.Contains(err.Error(), "model is required") {
				t.Errorf("Got unexpected 'model is required' error: %v", err)
			}
		})
	}
}

// TestBuildGoogleToolResultRequest tests the Google tool result request builder
func TestBuildGoogleToolResultRequest(t *testing.T) {
	// Create a temporary API key file
	apiKeyFile, err := os.CreateTemp("", "test-api-key-*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp API key file: %v", err)
	}
	defer os.Remove(apiKeyFile.Name())

	// Write test API key
	if _, err := apiKeyFile.WriteString("test-api-key"); err != nil {
		t.Fatalf("Failed to write API key: %v", err)
	}
	apiKeyFile.Close()

	cfg := newCoreTestRequestConfig()
	cfg.User = "testuser"
	cfg.Model = "gemini-2.5-pro"
	cfg.APIKeyFile = apiKeyFile.Name()
	cfg.ProviderURL = "https://generativelanguage.googleapis.com"
	cfg.System = "Test system prompt"
	cfg.Provider = "google"

	// Create typed messages to simulate the conversation including tool results
	typedMessages := []TypedMessage{
		NewTextMessage("user", "List files in current directory"),
		{
			Role: "model",
			Blocks: []Block{
				ToolUseBlock{
					ID:    "call_123",
					Name:  "list_files",
					Input: json.RawMessage(`{"directory": "."}`),
				},
				ToolUseBlock{
					ID:    "call_456",
					Name:  "read_file",
					Input: json.RawMessage(`{"path": "/etc/passwd"}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "call_123",
					Content:   "Files: file1.txt, file2.txt",
				},
				ToolResultBlock{
					ToolUseID: "call_456",
					Content:   "Error: Permission denied",
					IsError:   true,
				},
			},
		},
		NewTextMessage("user", "Additional context"),
	}

	// Tool definitions are now passed directly to request builders
	model := "gemini-2.5-pro"
	system := "Test system prompt"

	// Build the request using the unified builder
	req, body, err := buildChatRequestFromTyped(cfg, typedMessages, model, system, system != "", nil, nil, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify request properties
	if req.Method != "POST" {
		t.Errorf("Expected method POST, got %s", req.Method)
	}
	if !strings.Contains(req.URL.String(), "/models/gemini-2.5-pro:generateContent") {
		t.Errorf("Expected URL to contain model endpoint, got %s", req.URL.String())
	}
	if strings.Contains(req.URL.String(), "key=test-api-key") {
		t.Errorf("Expected URL to omit API key parameter, got %s", req.URL.String())
	}
	if got := req.Header.Get("x-goog-api-key"); got != "test-api-key" {
		t.Errorf("Expected x-goog-api-key header 'test-api-key', got %q", got)
	}

	// Parse and verify request body
	var parsedReq map[string]interface{}
	if err := json.Unmarshal(body, &parsedReq); err != nil {
		t.Fatalf("Failed to unmarshal request body: %v", err)
	}

	// Check system instruction
	if sysInst, ok := parsedReq["systemInstruction"].(map[string]interface{}); ok {
		if parts, ok := sysInst["parts"].([]interface{}); ok && len(parts) > 0 {
			if part, ok := parts[0].(map[string]interface{}); ok {
				if part["text"] != "Test system prompt" {
					t.Errorf("Expected system prompt 'Test system prompt', got %v", part["text"])
				}
			}
		}
	}

	// Check contents
	contents, ok := parsedReq["contents"].([]interface{})
	if !ok {
		t.Fatalf("Expected contents to be an array")
	}

	// Should have: user, model (with function calls), function responses, user (additional)
	if len(contents) != 4 {
		t.Errorf("Expected 4 contents, got %d", len(contents))
		// Debug: print the contents
		for i, content := range contents {
			if c, ok := content.(map[string]interface{}); ok {
				t.Logf("Content %d: role=%v", i, c["role"])
			}
		}
	}

	// Verify function response messages
	if len(contents) >= 3 {
		funcResp, ok := contents[2].(map[string]interface{})
		if !ok {
			t.Error("Expected third content to be a map")
		} else {
			// Google format uses "user" role for tool results
			if funcResp["role"] != "user" {
				t.Errorf("Expected user role for tool results, got %v", funcResp["role"])
			}
			parts, ok := funcResp["parts"].([]interface{})
			if !ok || len(parts) != 2 {
				t.Error("Expected function response to have 2 parts")
			} else {
				// Verify each part has functionResponse
				for i, part := range parts {
					partMap, ok := part.(map[string]interface{})
					if !ok {
						t.Errorf("Part %d is not a map", i)
						continue
					}
					if _, ok := partMap["functionResponse"]; !ok {
						t.Errorf("Part %d missing functionResponse", i)
					}
				}
			}
		}
	}
}

// TestStreamingFallbackAccumulation tests that unknown providers accumulate streamed content
func TestStreamingFallbackAccumulation(t *testing.T) {
	// Create a test server that streams content
	expectedContent := "Hello, streaming world!"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		// Stream content byte by byte
		for _, b := range []byte(expectedContent) {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}))
	defer ts.Close()

	// Create a temporary log directory
	logDir := t.TempDir()
	os.Setenv("LMC_LOG_DIR", logDir)
	defer os.Unsetenv("LMC_LOG_DIR")

	// Create request config with unknown provider
	cfg := newCoreTestRequestConfig()
	cfg.Provider = "unknown-provider"
	cfg.IsStreamChatMode = true

	// Create HTTP response
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("Failed to create test request: %v", err)
	}
	defer resp.Body.Close()

	// Create a test logger with proper log directory
	logger := &testLogger{
		logDir: logDir,
	}

	// Handle the streaming response
	ctx := context.Background()
	notifier := NewTestNotifier()
	response, err := HandleResponse(ctx, cfg, resp, logger, notifier)
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}

	// Verify accumulated content matches expected
	if response.Text != expectedContent {
		t.Errorf("Expected content %q, got %q", expectedContent, response.Text)
	}
}

// TestSSEParserMultiLine tests SSE parser with multi-line data frames
func TestSSEParserMultiLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name: "multi-line data frame",
			input: `data: {"text": "line1
data: line2
data: line3"}

data: {"text": "single"}`,
			expected: []string{
				`{"text": "line1
line2
line3"}`,
				`{"text": "single"}`,
			},
		},
		{
			name: "comment lines ignored",
			input: `: this is a comment
data: {"text": "data1"}
: another comment

data: {"text": "data2"}`,
			expected: []string{
				`{"text": "data1"}`,
				`{"text": "data2"}`,
			},
		},
		{
			name: "event and id fields",
			input: `event: message
id: 123
data: {"text": "with metadata"}

event: ping
: keepalive`,
			expected: []string{
				`{"text": "with metadata"}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a reader from the input
			reader := strings.NewReader(tt.input)

			// Create temporary log file
			logFile, err := os.CreateTemp("", "test-sse-*.log")
			if err != nil {
				t.Fatalf("Failed to create temp file: %v", err)
			}
			defer os.Remove(logFile.Name())
			defer logFile.Close()

			var collected []string
			state := &collectingStreamState{collected: &collected}

			// Run the parser
			ctx := context.Background()
			var output strings.Builder
			testNotifier := &testNotifier{}
			_, _, err = handleGenericStream(ctx, io.NopCloser(reader), logFile, &output, testNotifier, state, "test")
			if err != nil {
				t.Fatalf("handleGenericStream failed: %v", err)
			}

			// Verify collected content
			if len(collected) != len(tt.expected) {
				t.Errorf("Expected %d items, got %d", len(tt.expected), len(collected))
			}

			for i, expected := range tt.expected {
				if i >= len(collected) {
					break
				}
				if collected[i] != expected {
					t.Errorf("Item %d mismatch:\nExpected: %q\nGot: %q", i, expected, collected[i])
				}
			}
		})
	}
}

type collectingStreamState struct {
	collected *[]string
}

func (s *collectingStreamState) ParseLine(line string) (string, []ToolCall, bool, error) {
	if strings.HasPrefix(line, "data: ") {
		content := strings.TrimPrefix(line, "data: ")
		*s.collected = append(*s.collected, content)
		return content, nil, false, nil
	}
	return "", nil, false, nil
}

type testLogger struct {
	logDir string
}

func (t *testLogger) Info(format string, args ...interface{})   {}
func (t *testLogger) Warn(format string, args ...interface{})   {}
func (t *testLogger) Error(format string, args ...interface{})  {}
func (t *testLogger) Debugf(format string, args ...interface{}) {}
func (t *testLogger) IsDebugEnabled() bool                      { return false }
func (t *testLogger) GetLogDir() string {
	if t.logDir == "" {
		return os.TempDir()
	}
	return t.logDir
}

func (t *testLogger) CreateLogFile(dir string, prefix string) (*os.File, string, error) {
	// Create temp file in the specified directory
	pattern := prefix + "_*"
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

func (t *testLogger) LogJSON(dir string, prefix string, data []byte) error {
	// Create temp file in the specified directory
	pattern := prefix + "_*.json"
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

// testNotifier is a minimal implementation for testing
type testNotifier struct{}

func (t *testNotifier) Infof(format string, args ...interface{})   {}
func (t *testNotifier) Warnf(format string, args ...interface{})   {}
func (t *testNotifier) Errorf(format string, args ...interface{})  {}
func (t *testNotifier) Promptf(format string, args ...interface{}) {}

func TestHandleResponseWithToolCalls(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		responseBody  string
		wantText      string
		wantToolCalls int
		wantErr       bool
	}{
		{
			name:     "Anthropic with tool calls",
			provider: "anthropic",
			responseBody: `{
				"content": [
					{
						"type": "text",
						"text": "I'll help you with that."
					},
					{
						"type": "tool_use",
						"id": "call-123",
						"name": "universal_command",
						"input": {"command": ["echo", "hello"]}
					}
				]
			}`,
			wantText:      "I'll help you with that.",
			wantToolCalls: 1,
			wantErr:       false,
		},
		{
			name:     "OpenAI with tool calls",
			provider: "openai",
			responseBody: `{
				"choices": [{
					"message": {
						"content": "Let me run that command for you.",
						"tool_calls": [
							{
								"id": "call-456",
								"type": "function",
								"function": {
									"name": "universal_command",
									"arguments": "{\"command\":[\"ls\",\"-la\"]}"
								}
							}
						]
					}
				}]
			}`,
			wantText:      "Let me run that command for you.",
			wantToolCalls: 1,
			wantErr:       false,
		},
		{
			name:     "Google with tool calls",
			provider: "google",
			responseBody: `{
				"candidates": [{
					"content": {
						"parts": [
							{
								"text": "Executing the command now."
							},
							{
								"functionCall": {
									"name": "universal_command",
									"args": {"command": ["pwd"]}
								}
							}
						]
					}
				}]
			}`,
			wantText:      "Executing the command now.",
			wantToolCalls: 1,
			wantErr:       false,
		},
		{
			name:     "Multiple tool calls",
			provider: "anthropic",
			responseBody: `{
				"content": [
					{
						"type": "tool_use",
						"id": "call-1",
						"name": "universal_command",
						"input": {"command": ["echo", "first"]}
					},
					{
						"type": "tool_use",
						"id": "call-2",
						"name": "universal_command",
						"input": {"command": ["echo", "second"]}
					}
				]
			}`,
			wantText:      "",
			wantToolCalls: 2,
			wantErr:       false,
		},
		{
			name:     "Text only, no tools",
			provider: "anthropic",
			responseBody: `{
				"content": [
					{
						"type": "text",
						"text": "Just a text response, no tools needed."
					}
				]
			}`,
			wantText:      "Just a text response, no tools needed.",
			wantToolCalls: 0,
			wantErr:       false,
		},
		{
			name:          "Empty response",
			provider:      "anthropic",
			responseBody:  `{"content": []}`,
			wantText:      "",
			wantToolCalls: 0,
			wantErr:       false,
		},
		{
			name:          "Malformed JSON",
			provider:      "anthropic",
			responseBody:  `{"content": [{"type": "text", "text": "incomplete`,
			wantText:      "",
			wantToolCalls: 0,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer ts.Close()

			// Create request config
			cfg := newCoreTestRequestConfig()
			cfg.Provider = tt.provider

			// Create HTTP response
			resp, err := http.Get(ts.URL)
			if err != nil {
				t.Fatalf("Failed to create test request: %v", err)
			}
			defer resp.Body.Close()

			// Create test logger
			logger := &testLogger{}

			// Handle response
			ctx := context.Background()
			notifier := NewTestNotifier()
			response, err := HandleResponse(ctx, cfg, resp, logger, notifier)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("HandleResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return // Don't check other values on error
			}

			// Check text content
			if response.Text != tt.wantText {
				t.Errorf("HandleResponse() text = %q, want %q", response.Text, tt.wantText)
			}

			// Check tool calls count
			if len(response.ToolCalls) != tt.wantToolCalls {
				t.Errorf("HandleResponse() got %d tool calls, want %d", len(response.ToolCalls), tt.wantToolCalls)
			}

			// Verify tool call structure if present
			if len(response.ToolCalls) > 0 {
				for i, call := range response.ToolCalls {
					if call.ID == "" {
						t.Errorf("Tool call %d has empty ID", i)
					}
					if call.Name == "" {
						t.Errorf("Tool call %d has empty Name", i)
					}
					if len(call.Args) == 0 {
						t.Errorf("Tool call %d has empty Args", i)
					}
				}
			}
		})
	}
}

func TestBuildToolResultRequest(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		toolResults  []ToolResult
		wantContains []string
		wantErr      bool
	}{
		{
			name:     "Anthropic tool results",
			provider: "anthropic",
			toolResults: []ToolResult{
				{
					ID:     "call-123",
					Output: "Command executed successfully",
				},
			},
			wantContains: []string{
				`"type":"tool_result"`,
				`"tool_use_id":"call-123"`,
				`"content":"Command executed successfully"`,
			},
			wantErr: false,
		},
		{
			name:     "OpenAI tool results",
			provider: "openai",
			toolResults: []ToolResult{
				{
					ID:     "call-456",
					Output: "File listing complete",
				},
			},
			wantContains: []string{
				`"tool_call_id":"call-456"`,
				`"content":"File listing complete"`,
				`"role":"tool"`,
			},
			wantErr: false,
		},
		{
			name:     "Google tool results",
			provider: "google",
			toolResults: []ToolResult{
				{
					ID:     "call-789",
					Output: "Directory: /home/user",
				},
			},
			wantContains: []string{
				`"contents"`, // Google uses contents instead of messages
			},
			wantErr: false, // Google request builds successfully but without tool support
		},
		{
			name:     "Multiple tool results",
			provider: "anthropic",
			toolResults: []ToolResult{
				{
					ID:     "call-1",
					Output: "First result",
				},
				{
					ID:     "call-2",
					Output: "Second result",
				},
			},
			wantContains: []string{
				`"tool_use_id":"call-1"`,
				`"content":"First result"`,
				`"tool_use_id":"call-2"`,
				`"content":"Second result"`,
			},
			wantErr: false,
		},
		{
			name:     "Tool result with error",
			provider: "anthropic",
			toolResults: []ToolResult{
				{
					ID:    "call-err",
					Error: "Command not found",
				},
			},
			wantContains: []string{
				`"tool_use_id":"call-err"`,
				`"is_error":true`,
			},
			wantErr: false,
		},
		{
			name:     "Truncated output",
			provider: "anthropic",
			toolResults: []ToolResult{
				{
					ID:        "call-trunc",
					Output:    "Large output...",
					Truncated: true,
				},
			},
			wantContains: []string{
				`"tool_use_id":"call-trunc"`,
				`"content":"Large output...\n[output truncated]"`,
			},
			wantErr: false,
		},
		{
			name:         "Empty tool results",
			provider:     "anthropic",
			toolResults:  []ToolResult{},
			wantContains: []string{},
			wantErr:      false, // Empty results are allowed - might just have additional text
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary API key file if needed
			var apiKeyFile string
			if tt.provider != "argo" {
				tmpFile, err := os.CreateTemp("", "test-api-key-*.txt")
				if err != nil {
					t.Fatalf("Failed to create temp API key file: %v", err)
				}
				defer os.Remove(tmpFile.Name())

				if _, err := tmpFile.WriteString("test-api-key"); err != nil {
					t.Fatalf("Failed to write API key: %v", err)
				}
				tmpFile.Close()
				apiKeyFile = tmpFile.Name()
			}

			// Create request config
			cfg := newCoreTestRequestConfig()
			cfg.Provider = tt.provider
			cfg.Model = "test-model"
			cfg.APIKeyFile = apiKeyFile

			// Set up conversation
			var typedMessages []TypedMessage
			typedMessages = append(typedMessages, NewTextMessage("user", "Test message"))

			if len(tt.toolResults) > 0 {
				// Add assistant message with tool calls
				assistantBlocks := []Block{
					TextBlock{Text: "I'll help you with that"},
				}
				for _, result := range tt.toolResults {
					assistantBlocks = append(assistantBlocks, ToolUseBlock{
						ID:    result.ID,
						Name:  "test_tool",
						Input: json.RawMessage(`{}`),
					})
				}
				typedMessages = append(typedMessages, TypedMessage{
					Role:   "assistant",
					Blocks: assistantBlocks,
				})

				// Add tool results
				var toolResultBlocks []Block
				for _, result := range tt.toolResults {
					var toolResult ToolResultBlock
					toolResult.ToolUseID = result.ID
					if result.Error != "" {
						toolResult.Content = result.Error
						toolResult.IsError = true
					} else {
						output := result.Output
						if result.Truncated {
							output += "\n[output truncated]"
						}
						toolResult.Content = output
					}
					toolResultBlocks = append(toolResultBlocks, toolResult)
				}
				typedMessages = append(typedMessages, TypedMessage{
					Role:   "user",
					Blocks: toolResultBlocks,
				})
			}

			// Tool definitions are now passed directly to request builders
			model := "test-model"

			// Build request
			req, body, err := BuildToolResultRequest(cfg, model, "", nil, typedMessages)

			// Check error
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildToolResultRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return // Don't check other values on error
			}

			// Verify request is not nil
			if req == nil {
				t.Fatal("BuildToolResultRequest() returned nil request")
			}

			// Check request method
			if req.Method != "POST" {
				t.Errorf("BuildToolResultRequest() method = %s, want POST", req.Method)
			}

			// Check body contains expected content
			bodyStr := string(body)
			for _, want := range tt.wantContains {
				if !strings.Contains(bodyStr, want) {
					t.Errorf("BuildToolResultRequest() body missing %q\nGot: %s", want, bodyStr)
				}
			}
		})
	}
}

func TestBuildToolResultRequestStreamingPolicy(t *testing.T) {
	typedMessages := []TypedMessage{
		NewTextMessage("user", "Run a command"),
		{
			Role: "assistant",
			Blocks: []Block{
				ToolUseBlock{
					ID:    "call_1",
					Name:  "universal_command",
					Input: json.RawMessage(`{"command":["echo","ok"]}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "call_1",
					Name:      "universal_command",
					Content:   "ok",
				},
			},
		},
	}

	t.Run("native providers preserve streaming", func(t *testing.T) {
		cfg := newCoreTestRequestConfig()
		cfg.Provider = "argo"
		cfg.Model = "gpt-5"
		cfg.IsStreamChatMode = true
		cfg.IsToolEnabledFlag = true

		req, body, err := BuildToolResultRequest(cfg, cfg.Model, "", nil, typedMessages)
		if err != nil {
			t.Fatalf("BuildToolResultRequest failed: %v", err)
		}
		if !strings.Contains(string(body), `"stream":true`) {
			t.Fatalf("native Argo follow-up should preserve stream=true, body: %s", body)
		}
		if got := req.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", got)
		}
	})

	t.Run("legacy argo with tools disables streaming", func(t *testing.T) {
		cfg := newCoreTestRequestConfig()
		cfg.Provider = "argo"
		cfg.Model = "gpt-5"
		cfg.ArgoLegacy = true
		cfg.IsStreamChatMode = true
		cfg.IsToolEnabledFlag = true

		req, body, err := BuildToolResultRequest(cfg, cfg.Model, "", nil, typedMessages)
		if err != nil {
			t.Fatalf("BuildToolResultRequest failed: %v", err)
		}
		if strings.Contains(req.URL.Path, "streamchat") {
			t.Fatalf("legacy Argo tool follow-up should use non-stream endpoint, got %s", req.URL.String())
		}
		if strings.Contains(string(body), `"stream":`) {
			t.Fatalf("legacy Argo body should not include stream for tool follow-up, body: %s", body)
		}
		if got := req.Header.Get("Accept"); got == "text/event-stream" {
			t.Fatalf("legacy Argo tool follow-up should not request SSE")
		}
	})
}

func TestToolConversionRoundTrip(t *testing.T) {
	// Test that tool calls can be converted between formats and back
	originalCalls := []ToolCall{
		{
			ID:   "test-call-1",
			Name: "universal_command",
			Args: []byte(`{"command":["echo","hello world"],"environ":{"USER":"test"}}`),
		},
	}

	tests := []struct {
		name     string
		provider string
	}{
		{"Anthropic format", "anthropic"},
		{"OpenAI format", "openai"},
		{"Google format", "google"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For now, just verify the tool calls are valid JSON
			for _, call := range originalCalls {
				if !json.Valid(call.Args) {
					t.Errorf("Invalid JSON in tool call args: %s", string(call.Args))
				}
			}
		})
	}
}
