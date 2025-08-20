package core

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseOpenAIStreamLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantContent string
		wantDone    bool
		wantErr     bool
	}{
		{
			name:        "valid data chunk",
			line:        `data: {"choices":[{"delta":{"content":"Hello"}}]}`,
			wantContent: "Hello",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "done marker",
			line:        "data: [DONE]",
			wantContent: "",
			wantDone:    true,
			wantErr:     false,
		},
		{
			name:        "empty delta content",
			line:        `data: {"choices":[{"delta":{"content":""}}]}`,
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "no choices",
			line:        `data: {"choices":[]}`,
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "malformed JSON",
			line:        `data: {"choices":[{"delta":{"content":"Hello"`,
			wantContent: "",
			wantDone:    false,
			wantErr:     true,
		},
		{
			name:        "non-data line",
			line:        "event: message",
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "multiline content",
			line:        `data: {"choices":[{"delta":{"content":"Line 1\nLine 2"}}]}`,
			wantContent: "Line 1\nLine 2",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "special characters",
			line:        `data: {"choices":[{"delta":{"content":"Hello \"world\" with 'quotes'"}}]}`,
			wantContent: `Hello "world" with 'quotes'`,
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "unicode content",
			line:        `data: {"choices":[{"delta":{"content":"Hello 世界 🌍"}}]}`,
			wantContent: "Hello 世界 🌍",
			wantDone:    false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, done, err := parseOpenAIStreamLine(tt.line, nil)

			if (err != nil) != tt.wantErr {
				t.Errorf("parseOpenAIStreamLine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if content != tt.wantContent {
				t.Errorf("parseOpenAIStreamLine() content = %v, want %v", content, tt.wantContent)
			}
			if done != tt.wantDone {
				t.Errorf("parseOpenAIStreamLine() done = %v, want %v", done, tt.wantDone)
			}
		})
	}
}

func TestParseAnthropicStreamLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		state       *anthropicStreamState
		wantContent string
		wantDone    bool
		wantErr     bool
		wantEvent   string
	}{
		{
			name:        "event line",
			line:        "event: content_block_delta",
			state:       &anthropicStreamState{},
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
			wantEvent:   "content_block_delta",
		},
		{
			name:        "data line with matching event",
			line:        `data: {"delta":{"text":"Hello"}}`,
			state:       &anthropicStreamState{currentEvent: "content_block_delta"},
			wantContent: "Hello",
			wantDone:    false,
			wantErr:     false,
			wantEvent:   "content_block_delta",
		},
		{
			name:        "data line with non-matching event",
			line:        `data: {"delta":{"text":"Hello"}}`,
			state:       &anthropicStreamState{currentEvent: "other_event"},
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
			wantEvent:   "other_event",
		},
		{
			name:        "malformed JSON data",
			line:        `data: {"delta":{"text":"Hello"`,
			state:       &anthropicStreamState{currentEvent: "content_block_delta"},
			wantContent: "",
			wantDone:    false,
			wantErr:     true,
			wantEvent:   "content_block_delta",
		},
		{
			name:        "empty text in delta",
			line:        `data: {"delta":{"text":""}}`,
			state:       &anthropicStreamState{currentEvent: "content_block_delta"},
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
			wantEvent:   "content_block_delta",
		},
		{
			name:        "multiline text",
			line:        `data: {"delta":{"text":"Line 1\nLine 2\nLine 3"}}`,
			state:       &anthropicStreamState{currentEvent: "content_block_delta"},
			wantContent: "Line 1\nLine 2\nLine 3",
			wantDone:    false,
			wantErr:     false,
			wantEvent:   "content_block_delta",
		},
		{
			name:        "event change",
			line:        "event: message_stop",
			state:       &anthropicStreamState{currentEvent: "content_block_delta"},
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
			wantEvent:   "message_stop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.state == nil {
				tt.state = &anthropicStreamState{}
			}

			content, done, err := parseAnthropicStreamLine(tt.line, tt.state)

			if (err != nil) != tt.wantErr {
				t.Errorf("parseAnthropicStreamLine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if content != tt.wantContent {
				t.Errorf("parseAnthropicStreamLine() content = %v, want %v", content, tt.wantContent)
			}
			if done != tt.wantDone {
				t.Errorf("parseAnthropicStreamLine() done = %v, want %v", done, tt.wantDone)
			}
			if tt.state.currentEvent != tt.wantEvent {
				t.Errorf("parseAnthropicStreamLine() state.currentEvent = %v, want %v", tt.state.currentEvent, tt.wantEvent)
			}
		})
	}
}

func TestParseGoogleStreamLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantContent string
		wantDone    bool
		wantErr     bool
	}{
		{
			name:        "valid data chunk",
			line:        `data: {"candidates":[{"content":{"parts":[{"text":"Hello Google"}]}}]}`,
			wantContent: "Hello Google",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "empty candidates",
			line:        `data: {"candidates":[]}`,
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "empty parts",
			line:        `data: {"candidates":[{"content":{"parts":[]}}]}`,
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "empty text",
			line:        `data: {"candidates":[{"content":{"parts":[{"text":""}]}}]}`,
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "malformed JSON",
			line:        `data: {"candidates":[{"content":{"parts":[{"text":"Hello"`,
			wantContent: "",
			wantDone:    false,
			wantErr:     true,
		},
		{
			name:        "non-data line",
			line:        "event: something",
			wantContent: "",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "multiple parts (only first used)",
			line:        `data: {"candidates":[{"content":{"parts":[{"text":"First"},{"text":"Second"}]}}]}`,
			wantContent: "First",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "multiple candidates (only first used)",
			line:        `data: {"candidates":[{"content":{"parts":[{"text":"First candidate"}]}},{"content":{"parts":[{"text":"Second candidate"}]}}]}`,
			wantContent: "First candidate",
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "nested JSON structure",
			line:        `data: {"candidates":[{"content":{"parts":[{"text":"Complex \"nested\" content"}]}}]}`,
			wantContent: `Complex "nested" content`,
			wantDone:    false,
			wantErr:     false,
		},
		{
			name:        "unicode in content",
			line:        `data: {"candidates":[{"content":{"parts":[{"text":"Hello 世界 from Google AI 🚀"}]}}]}`,
			wantContent: "Hello 世界 from Google AI 🚀",
			wantDone:    false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, done, err := parseGoogleStreamLine(tt.line, nil)

			if (err != nil) != tt.wantErr {
				t.Errorf("parseGoogleStreamLine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if content != tt.wantContent {
				t.Errorf("parseGoogleStreamLine() content = %v, want %v", content, tt.wantContent)
			}
			if done != tt.wantDone {
				t.Errorf("parseGoogleStreamLine() done = %v, want %v", done, tt.wantDone)
			}
		})
	}
}

// Test edge cases and error scenarios
func TestStreamParsersEdgeCases(t *testing.T) {
	t.Run("OpenAI parser with very long line", func(t *testing.T) {
		// Create a very long content string
		longContent := ""
		for i := 0; i < 10000; i++ {
			longContent += "a"
		}
		line := `data: {"choices":[{"delta":{"content":"` + longContent + `"}}]}`

		content, done, err := parseOpenAIStreamLine(line, nil)
		if err != nil {
			t.Errorf("parseOpenAIStreamLine() unexpected error with long content: %v", err)
		}
		if content != longContent {
			t.Errorf("parseOpenAIStreamLine() failed to handle long content")
		}
		if done {
			t.Errorf("parseOpenAIStreamLine() unexpected done = true")
		}
	})

	t.Run("Anthropic parser state persistence", func(t *testing.T) {
		state := &anthropicStreamState{}

		// Set event
		_, _, _ = parseAnthropicStreamLine("event: content_block_delta", state)
		if state.currentEvent != "content_block_delta" {
			t.Errorf("State not updated correctly")
		}

		// Process data with the event
		content, _, _ := parseAnthropicStreamLine(`data: {"delta":{"text":"Test"}}`, state)
		if content != "Test" {
			t.Errorf("Failed to process data with stored event")
		}

		// Change event
		_, _, _ = parseAnthropicStreamLine("event: other_event", state)
		if state.currentEvent != "other_event" {
			t.Errorf("State not updated on event change")
		}

		// Same data should not produce content with different event
		content, _, _ = parseAnthropicStreamLine(`data: {"delta":{"text":"Test"}}`, state)
		if content != "" {
			t.Errorf("Should not process data with non-matching event")
		}
	})

	t.Run("Empty JSON objects", func(t *testing.T) {
		// OpenAI empty object
		content, _, err := parseOpenAIStreamLine(`data: {}`, nil)
		if err != nil {
			t.Errorf("parseOpenAIStreamLine() error with empty object: %v", err)
		}
		if content != "" {
			t.Errorf("parseOpenAIStreamLine() should return empty content for empty object")
		}

		// Google empty object
		content, _, err = parseGoogleStreamLine(`data: {}`, nil)
		if err != nil {
			t.Errorf("parseGoogleStreamLine() error with empty object: %v", err)
		}
		if content != "" {
			t.Errorf("parseGoogleStreamLine() should return empty content for empty object")
		}
	})
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
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer ts.Close()

	// Create a temporary log directory
	logDir := t.TempDir()
	os.Setenv("LMC_LOG_DIR", logDir)
	defer os.Unsetenv("LMC_LOG_DIR")

	// Create request config with unknown provider
	cfg := &testRequestConfig{
		provider: "unknown-provider",
		stream:   true,
	}

	// Create HTTP response
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("Failed to create test request: %v", err)
	}
	defer resp.Body.Close()

	// Create a test logger
	logger := &testLogger{}

	// Handle the streaming response
	ctx := context.Background()
	content, err := HandleResponse(ctx, cfg, resp, logger)
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}

	// Verify accumulated content matches expected
	if content != expectedContent {
		t.Errorf("Expected content %q, got %q", expectedContent, content)
	}
}

// TestBuildRegenerationRequestDelegation tests that BuildRegenerationRequest delegates to BuildRequestWithSession
func TestBuildRegenerationRequestDelegation(t *testing.T) {
	cfg := &testRequestConfig{
		provider: "argo",
		model:    "test-model",
		system:   "test system",
		input:    "test input",
	}

	sess := &testSession{
		path: "0001/0002",
	}

	messages := []Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}

	getLineage := func(path string) ([]Message, error) {
		if path != sess.GetPath() {
			t.Errorf("Expected path %q, got %q", sess.GetPath(), path)
		}
		return messages, nil
	}

	// Call both functions and compare results
	req1, body1, err1 := BuildRequestWithSession(cfg, sess, getLineage)
	req2, body2, err2 := BuildRegenerationRequest(cfg, sess, getLineage)

	// Both should succeed or fail together
	if (err1 == nil) != (err2 == nil) {
		t.Fatalf("Error mismatch: BuildRequestWithSession: %v, BuildRegenerationRequest: %v", err1, err2)
	}

	if err1 != nil {
		return // Both failed as expected
	}

	// Compare requests
	if req1.URL.String() != req2.URL.String() {
		t.Errorf("URL mismatch: %s vs %s", req1.URL.String(), req2.URL.String())
	}

	if req1.Method != req2.Method {
		t.Errorf("Method mismatch: %s vs %s", req1.Method, req2.Method)
	}

	// Compare bodies
	if !bytes.Equal(body1, body2) {
		t.Errorf("Body mismatch:\n%s\nvs\n%s", string(body1), string(body2))
	}
}

// TestSSEParserMultiLine tests SSE parser with multi-line data frames
func TestSSEParserMultiLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		parser   streamParser
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
			parser: parseTestSSE,
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
			parser: parseTestSSE,
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
			parser: parseTestSSE,
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

			// Collect parsed content
			var collected []string
			parser := func(line string, state interface{}) (string, bool, error) {
				if strings.HasPrefix(line, "data: ") {
					content := strings.TrimPrefix(line, "data: ")
					collected = append(collected, content)
					return content, false, nil
				}
				return "", false, nil
			}

			// Run the parser
			ctx := context.Background()
			_, err = handleGenericStream(ctx, io.NopCloser(reader), logFile, parser, nil)
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

// Test helpers
type testRequestConfig struct {
	provider string
	model    string
	system   string
	input    string
	stream   bool
	embed    bool
}

func (t *testRequestConfig) GetUser() string        { return "testuser" }
func (t *testRequestConfig) GetProvider() string    { return t.provider }
func (t *testRequestConfig) GetModel() string       { return t.model }
func (t *testRequestConfig) GetSystem() string      { return t.system }
func (t *testRequestConfig) GetInput() string       { return t.input }
func (t *testRequestConfig) IsStreamChat() bool     { return t.stream }
func (t *testRequestConfig) IsEmbed() bool          { return t.embed }
func (t *testRequestConfig) GetEnv() string         { return "" }
func (t *testRequestConfig) GetMaxTokens() int      { return 0 }
func (t *testRequestConfig) GetAPIKey() string      { return "" }
func (t *testRequestConfig) GetAPIKeyFile() string  { return "" }
func (t *testRequestConfig) GetProviderURL() string { return "" }
func (t *testRequestConfig) GetTools() []byte       { return nil }

type testSession struct {
	path string
}

func (t *testSession) GetPath() string { return t.path }

type testLogger struct {
	logDir string
}

func (t *testLogger) Info(format string, args ...interface{})  {}
func (t *testLogger) Warn(format string, args ...interface{})  {}
func (t *testLogger) Error(format string, args ...interface{}) {}
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

func (t *testLogger) LogJSON(dir, prefix string, data []byte) error {
	return nil
}

func parseTestSSE(line string, state interface{}) (string, bool, error) {
	if strings.HasPrefix(line, "data: ") {
		return strings.TrimPrefix(line, "data: "), false, nil
	}
	return "", false, nil
}
