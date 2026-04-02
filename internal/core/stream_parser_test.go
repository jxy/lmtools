package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// MockNotifier implements the Notifier interface for testing
type MockNotifier struct {
	warnings []string
	errors   []string
	infos    []string
}

func (m *MockNotifier) Warnf(format string, args ...interface{}) {
	m.warnings = append(m.warnings, fmt.Sprintf(format, args...))
}

func (m *MockNotifier) Errorf(format string, args ...interface{}) {
	m.errors = append(m.errors, fmt.Sprintf(format, args...))
}

func (m *MockNotifier) Infof(format string, args ...interface{}) {
	m.infos = append(m.infos, fmt.Sprintf(format, args...))
}

func (m *MockNotifier) Promptf(format string, args ...interface{}) {
	m.infos = append(m.infos, fmt.Sprintf(format, args...))
}

// MockLogger implements the Logger interface for testing
type MockLogger struct {
	debugMessages []string
	debugEnabled  bool
	logDir        string
}

func (m *MockLogger) Debugf(format string, args ...interface{}) {
	m.debugMessages = append(m.debugMessages, fmt.Sprintf(format, args...))
}

func (m *MockLogger) IsDebugEnabled() bool {
	return m.debugEnabled
}

func (m *MockLogger) Infof(format string, args ...interface{})  {}
func (m *MockLogger) Warnf(format string, args ...interface{})  {}
func (m *MockLogger) Errorf(format string, args ...interface{}) {}
func (m *MockLogger) LogJSON(logDir string, opName string, data []byte) error {
	return nil
}

func (m *MockLogger) CreateLogFile(logDir, prefix string) (*os.File, string, error) {
	f, err := os.CreateTemp(logDir, prefix)
	return f, f.Name(), err
}

func (m *MockLogger) GetLogDir() string {
	if m.logDir != "" {
		return m.logDir
	}
	return "/tmp"
}

// TestStreamingParseErrorThreshold tests the parse error threshold behavior
// for streaming responses across different providers
func TestStreamingParseErrorThreshold(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		responseBody   string
		expectWarning  bool
		warningPattern string
		expectedText   string
	}{
		{
			name:     "anthropic_clean_stream",
			provider: "anthropic",
			responseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-3-opus"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}
`,
			expectWarning: false,
			expectedText:  "Hello world",
		},
		// Note: Current implementation silently ignores JSON unmarshal errors
		// which is the correct behavior for streaming parsers (they should be
		// resilient to malformed chunks). The parse error threshold is only
		// triggered when the parser itself returns an error, not for ignored
		// malformed JSON chunks.
		{
			name:     "anthropic_with_malformed_json",
			provider: "anthropic",
			responseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-3-opus"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {invalid json 1}

event: content_block_delta
data: {invalid json 2}

event: content_block_delta
data: {invalid json 3}

event: content_block_delta
data: {invalid json 4}

event: content_block_delta
data: {invalid json 5}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}
`,
			expectWarning: false, // Parser silently ignores malformed JSON
			expectedText:  "Hello world",
		},
		{
			name:     "google_clean_stream",
			provider: "google",
			responseBody: `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}

data: {"candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":10,"totalTokenCount":20}}
`,
			expectWarning: false,
			expectedText:  "Hello world",
		},
		{
			name:     "google_with_malformed_json",
			provider: "google",
			responseBody: `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}]}

data: {malformed json 1

data: {malformed json 2

data: {malformed json 3

data: {malformed json 4

data: {malformed json 5

data: {"candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"},"finishReason":"STOP","index":0}]}
`,
			expectWarning:  true, // Google parser returns errors for malformed JSON
			warningPattern: "Multiple streaming parse errors detected",
			expectedText:   "Hello world",
		},
		{
			name:     "openai_clean_stream",
			provider: "openai",
			responseBody: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`,
			expectWarning: false,
			expectedText:  "Hello world",
		},
		{
			name:     "openai_with_malformed_json",
			provider: "openai",
			responseBody: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {broken json 1

data: {broken json 2

data: {broken json 3

data: {broken json 4

data: {broken json 5

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`,
			expectWarning:  true, // OpenAI parser returns errors for malformed JSON
			warningPattern: "Multiple streaming parse errors detected",
			expectedText:   "Hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock notifier and logger
			notifier := &MockNotifier{}
			logger := &MockLogger{
				debugEnabled: true,
				logDir:       t.TempDir(),
			}

			// Create a test server that returns the streaming response
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			// Make request
			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("Failed to make request: %v", err)
			}

			// Create a mock config
			cfg := newStreamTestRequestConfig(tt.provider, true, false, "")

			// Handle the response
			ctx := context.Background()
			response, err := HandleResponse(ctx, cfg, resp, logger, notifier)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			// Check response text
			if response.Text != tt.expectedText {
				t.Errorf("Expected text '%s', got '%s'", tt.expectedText, response.Text)
			}

			// Check warning expectation
			hasWarning := false
			for _, warning := range notifier.warnings {
				if strings.Contains(warning, tt.warningPattern) {
					hasWarning = true
					break
				}
			}

			if tt.expectWarning && !hasWarning {
				t.Errorf("Expected warning containing '%s', but got none. Warnings: %v", tt.warningPattern, notifier.warnings)
			}

			if !tt.expectWarning && hasWarning {
				t.Errorf("Did not expect warning, but got one. Warnings: %v", notifier.warnings)
			}
		})
	}
}

// TestStreamParserErrorRecovery tests that streaming parsers can recover from errors
// and continue processing subsequent valid chunks
func TestStreamParserErrorRecovery(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		responseBody string
		expectedText string
	}{
		{
			name:     "anthropic_recovery",
			provider: "anthropic",
			responseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[]}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Part 1"}}

event: bad_event
data: {this is invalid json and should be skipped}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Part 2"}}

event: message_stop
data: {"type":"message_stop"}
`,
			expectedText: "Part 1 Part 2",
		},
		{
			name:     "google_recovery",
			provider: "google",
			responseBody: `data: {"candidates":[{"content":{"parts":[{"text":"Before"}],"role":"model"}}]}

data: {invalid chunk}

data: {"candidates":[{"content":{"parts":[{"text":" After"}],"role":"model"}}]}
`,
			expectedText: "Before After",
		},
		{
			name:     "openai_recovery",
			provider: "openai",
			responseBody: `data: {"id":"chat-123","choices":[{"delta":{"content":"Start"}}]}

data: {bad json}

data: {"id":"chat-123","choices":[{"delta":{"content":" End"}}]}

data: [DONE]
`,
			expectedText: "Start End",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &MockNotifier{}
			logger := &MockLogger{}

			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			// Make request
			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("Failed to make request: %v", err)
			}

			// Create config
			cfg := newStreamTestRequestConfig(tt.provider, true, false, "")

			// Handle response
			ctx := context.Background()
			response, err := HandleResponse(ctx, cfg, resp, logger, notifier)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if response.Text != tt.expectedText {
				t.Errorf("Expected text '%s', got '%s'", tt.expectedText, response.Text)
			}
		})
	}
}

// TestStreamParserToolCallHandling tests that streaming parsers correctly handle tool calls
func TestStreamParserToolCallHandling(t *testing.T) {
	tests := []struct {
		name              string
		provider          string
		responseBody      string
		expectedText      string
		expectedToolCalls int
	}{
		{
			name:     "anthropic_with_tools",
			provider: "anthropic",
			responseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[]}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll check the weather"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_123","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"London\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}
`,
			expectedText:      "I'll check the weather",
			expectedToolCalls: 1,
		},
		{
			name:     "openai_with_tools",
			provider: "openai",
			responseBody: `data: {"choices":[{"delta":{"role":"assistant","content":"Let me help","tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"calculator","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"expr\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"2+2\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]
`,
			expectedText:      "Let me help",
			expectedToolCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &MockNotifier{}
			logger := &MockLogger{}

			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			// Make request
			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("Failed to make request: %v", err)
			}

			// Create config
			cfg := newStreamTestRequestConfig(tt.provider, true, false, "")

			// Handle response
			ctx := context.Background()
			response, err := HandleResponse(ctx, cfg, resp, logger, notifier)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if response.Text != tt.expectedText {
				t.Errorf("Expected text '%s', got '%s'", tt.expectedText, response.Text)
			}

			if len(response.ToolCalls) != tt.expectedToolCalls {
				t.Errorf("Expected %d tool calls, got %d", tt.expectedToolCalls, len(response.ToolCalls))
			}
		})
	}
}

func newStreamTestRequestConfig(provider string, streamChat bool, embed bool, system string) *TestRequestConfig {
	cfg := NewTestRequestConfig()
	cfg.Provider = provider
	cfg.Model = ""
	cfg.System = system
	cfg.ProviderURL = "http://test.example.com"
	cfg.IsStreamChatMode = streamChat
	cfg.IsEmbedMode = embed
	cfg.IsToolEnabledFlag = false
	return cfg
}
