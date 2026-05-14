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

func TestParseOpenAIStreamChunkTopLevelErrorIsFatal(t *testing.T) {
	_, err := ParseOpenAIStreamChunk([]byte(`{"error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}`))
	if err == nil {
		t.Fatal("ParseOpenAIStreamChunk() error = nil, want provider stream error")
	}
	if !isFatalStreamError(err) {
		t.Fatalf("ParseOpenAIStreamChunk() error is not fatal: %v", err)
	}
	for _, want := range []string{"upstream stream error", "quota exceeded", "insufficient_quota", "billing_hard_limit_reached"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ParseOpenAIStreamChunk() error = %q, want %q", err.Error(), want)
		}
	}
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

func TestOpenAIResponsesStreamingFailedEventReturnsError(t *testing.T) {
	tests := []struct {
		name         string
		responseBody string
	}{
		{
			name: "failed event flushed by blank line",
			responseBody: `data: {"type":"response.output_text.delta","delta":"partial"}

data: {"type":"response.failed","response":{"error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}}

`,
		},
		{
			name: "failed event flushed at EOF",
			responseBody: `data: {"type":"response.output_text.delta","delta":"partial"}

data: {"type":"response.failed","response":{"error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}}`,
		},
		{
			name: "top-level error event flushed by blank line",
			responseBody: `data: {"type":"response.output_text.delta","delta":"partial"}

data: {"type":"error","error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}

`,
		},
		{
			name: "top-level error envelope flushed at EOF",
			responseBody: `data: {"type":"response.output_text.delta","delta":"partial"}

data: {"error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("http.Get() error = %v", err)
			}

			cfg := newStreamTestRequestConfig("openai", true, false, "")
			cfg.OpenAIResponses = true

			response, err := HandleResponse(context.Background(), cfg, resp, &MockLogger{logDir: t.TempDir()}, &MockNotifier{})
			if err == nil {
				t.Fatalf("HandleResponse() error = nil, want provider failure; response = %#v", response)
			}
			for _, want := range []string{"quota exceeded", "insufficient_quota", "billing_hard_limit_reached"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("HandleResponse() error = %q, want %q", err.Error(), want)
				}
			}
			if response.Text != "" || len(response.ToolCalls) != 0 || len(response.Blocks) != 0 || response.Streamed {
				t.Fatalf("HandleResponse() response = %#v, want zero value on error", response)
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

func TestOpenAIStreamStatePreservesCustomToolInputDeltasWithoutRepeatedType(t *testing.T) {
	state := NewOpenAIStreamState()
	lines := []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_custom","type":"custom","custom":{"name":"apply_patch","input":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"custom":{"input":"*** Begin"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"custom":{"input":" Patch\n*** End Patch\n"}}]}}]}`,
		`data: [DONE]`,
	}

	var finalCalls []ToolCall
	for _, line := range lines {
		_, calls, done, err := state.ParseLine(line)
		if err != nil {
			t.Fatalf("ParseLine(%q) error = %v", line, err)
		}
		if done {
			finalCalls = calls
		}
	}

	if len(finalCalls) != 1 {
		t.Fatalf("len(finalCalls) = %d, want 1: %#v", len(finalCalls), finalCalls)
	}
	call := finalCalls[0]
	if call.ID != "call_custom" || call.Type != "custom" || call.Name != "apply_patch" {
		t.Fatalf("custom call metadata = %+v", call)
	}
	wantInput := "*** Begin Patch\n*** End Patch\n"
	if call.Input != wantInput {
		t.Fatalf("custom input = %q, want %q", call.Input, wantInput)
	}
	if len(call.Args) != 0 {
		t.Fatalf("custom args = %s, want empty args", string(call.Args))
	}
}

func TestAnthropicStreamingPreservesSignedThinkingBeforeToolCall(t *testing.T) {
	responseBody := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[]}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should inspect the tool output."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"fixture"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_123","name":"lookup","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"weather\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}

	response, err := HandleResponse(context.Background(), newStreamTestRequestConfig("anthropic", true, false, ""), resp, &MockLogger{logDir: t.TempDir()}, &MockNotifier{})
	if err != nil {
		t.Fatalf("HandleResponse() error = %v", err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "tool_123" {
		t.Fatalf("tool calls = %+v, want tool_123", response.ToolCalls)
	}
	if len(response.Blocks) != 2 {
		t.Fatalf("blocks = %#v, want thinking plus tool_use", response.Blocks)
	}
	reasoning, ok := response.Blocks[0].(ReasoningBlock)
	if !ok {
		t.Fatalf("first block = %T, want ReasoningBlock", response.Blocks[0])
	}
	if reasoning.Provider != "anthropic" || reasoning.Type != "thinking" || reasoning.Text != "I should inspect the tool output." || reasoning.Signature != "sig_fixture" {
		t.Fatalf("reasoning block = %#v", reasoning)
	}
	toolUse, ok := response.Blocks[1].(ToolUseBlock)
	if !ok || toolUse.ID != "tool_123" || toolUse.Name != "lookup" || string(toolUse.Input) != `{"query":"weather"}` {
		t.Fatalf("second block = %#v, want lookup tool_use", response.Blocks[1])
	}

	anthropic := ToAnthropicTyped([]TypedMessage{{
		Role:   string(RoleAssistant),
		Blocks: response.Blocks,
	}})
	marshaled := MarshalAnthropicMessagesForRequest(anthropic)
	if len(marshaled) != 1 {
		t.Fatalf("marshaled messages = %#v, want one message", marshaled)
	}
	msg, ok := marshaled[0].(map[string]interface{})
	if !ok {
		t.Fatalf("marshaled message = %T, want map", marshaled[0])
	}
	content, ok := msg["content"].([]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("marshaled content = %#v, want thinking plus tool_use", msg["content"])
	}
	replayedThinking, ok := content[0].(map[string]interface{})
	if !ok || replayedThinking["type"] != "thinking" || replayedThinking["thinking"] != "I should inspect the tool output." || replayedThinking["signature"] != "sig_fixture" {
		t.Fatalf("replayed thinking = %#v", content[0])
	}
	replayedTool, ok := content[1].(map[string]interface{})
	if !ok || replayedTool["type"] != "tool_use" || replayedTool["id"] != "tool_123" {
		t.Fatalf("replayed tool = %#v", content[1])
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
