package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFormatToolCallsInline tests the tool call inline formatting
func TestFormatToolCallsInline(t *testing.T) {
	tests := []struct {
		name     string
		calls    []core.ToolCall
		expected string
	}{
		{
			name:     "no calls",
			calls:    []core.ToolCall{},
			expected: "",
		},
		{
			name: "single tool with path",
			calls: []core.ToolCall{
				{ID: "1", Name: "file_read", Args: json.RawMessage(`{"path": "/test/file.txt"}`)},
			},
			expected: " [tool: file_read(/test/file.txt)]",
		},
		{
			name: "command tool",
			calls: []core.ToolCall{
				{ID: "1", Name: "universal_command", Args: json.RawMessage(`{"command": ["ls", "-la"]}`)},
			},
			expected: " [tool: universal_command(ls)]",
		},
		{
			name: "multiple tools",
			calls: []core.ToolCall{
				{ID: "1", Name: "file_read", Args: json.RawMessage(`{"path": "/src/main.go"}`)},
				{ID: "2", Name: "universal_command", Args: json.RawMessage(`{"command": ["go", "test"]}`)},
			},
			expected: " [tool: file_read(/src/main.go); universal_command(go)]",
		},
		{
			name: "tool with no args",
			calls: []core.ToolCall{
				{ID: "1", Name: "get_time", Args: json.RawMessage(``)},
			},
			expected: " [tool: get_time]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatToolCallsInline(tt.calls)
			if result != tt.expected {
				t.Errorf("formatToolCallsInline() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestFormatToolResultsInline tests the tool result inline formatting
func TestFormatToolResultsInline(t *testing.T) {
	tests := []struct {
		name     string
		results  []core.ToolResult
		expected string
	}{
		{
			name:     "no results",
			results:  []core.ToolResult{},
			expected: "",
		},
		{
			name: "single success",
			results: []core.ToolResult{
				{ID: "1", Output: "package main\nfunc main() {}", Elapsed: 100},
			},
			expected: " [result: package main func main() {} (27B)]",
		},
		{
			name: "error result",
			results: []core.ToolResult{
				{ID: "1", Error: "file not found", Elapsed: 100},
			},
			expected: " [result: error: file not found]",
		},
		{
			name: "multiple results",
			results: []core.ToolResult{
				{ID: "1", Output: "test output", Elapsed: 100},
				{ID: "2", Output: "PASS", Elapsed: 200},
			},
			expected: " [result: test output (11B); PASS (4B)]",
		},
		{
			name: "long output truncated",
			results: []core.ToolResult{
				{ID: "1", Output: "This is a very long output that will be truncated for display", Elapsed: 100},
			},
			expected: " [result: This is a very long output ... (61B)]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatToolResultsInline(tt.results)
			if result != tt.expected {
				t.Errorf("formatToolResultsInline() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestFormatJSONArgs tests JSON argument formatting
func TestFormatJSONArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     json.RawMessage
		indent   string
		contains []string // Check if output contains these strings
	}{
		{
			name:     "empty args",
			args:     json.RawMessage{},
			indent:   "  ",
			contains: []string{""},
		},
		{
			name:   "simple object",
			args:   json.RawMessage(`{"path": "/test", "recursive": true}`),
			indent: "  ",
			contains: []string{
				`"path": "/test"`,
				`"recursive": true`,
			},
		},
		{
			name:   "array args",
			args:   json.RawMessage(`["ls", "-la", "/home"]`),
			indent: "    ",
			contains: []string{
				`"ls"`,
				`"-la"`,
				`"/home"`,
			},
		},
		{
			name:   "invalid JSON returns raw",
			args:   json.RawMessage(`not valid json`),
			indent: "  ",
			contains: []string{
				`not valid json`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := format.PrettyJSONArgs(tt.args, tt.indent)
			for _, expected := range tt.contains {
				if expected == "" && result != "" {
					t.Errorf("formatJSONArgs() = %q, want empty string", result)
				} else if expected != "" && !contains(result, expected) {
					t.Errorf("formatJSONArgs() = %q, want to contain %q", result, expected)
				}
			}
		})
	}
}

// TestDisplayTreeWithTools tests tree display with tool interactions
func TestDisplayTreeWithTools(t *testing.T) {
	// Create a temporary session directory
	tempDir := t.TempDir()
	sessionsDir := filepath.Join(tempDir, "sessions")
	sessionPath := filepath.Join(sessionsDir, "0001")
	if err := os.MkdirAll(sessionPath, constants.DirPerm); err != nil {
		t.Fatal(err)
	}

	// Set the sessions directory for the test
	oldDir := GetSessionsDir()
	SetSessionsDir(sessionsDir)
	defer SetSessionsDir(oldDir)

	// Create a session
	sess := &Session{Path: sessionPath}

	// Create messages with tool interactions
	// Message 1: User prompt
	msg1 := Message{
		Role:      "user",
		Content:   "Analyze this code",
		Timestamp: time.Now(),
	}
	result1, err := AppendMessageWithToolInteraction(context.Background(), sess, msg1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	msgID1 := result1.MessageID

	// Message 2: Assistant with tool calls (will be created by SaveAssistantResponseWithTools)
	toolCalls := []core.ToolCall{
		{
			ID:   "call_123",
			Name: "file_read",
			Args: json.RawMessage(`{"path": "/src/main.go"}`),
		},
		{
			ID:   "call_456",
			Name: "universal_command",
			Args: json.RawMessage(`{"command": ["go", "test"]}`),
		},
	}
	result2, err := SaveAssistantResponseWithTools(
		context.Background(),
		sess,
		"I'll analyze the code for you",
		toolCalls,
		"claude-3-opus",
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID2 := result2.MessageID

	// Message 3: User with tool results
	toolResults := []core.ToolResult{
		{
			ID:      "call_123",
			Output:  "package main\nfunc main() {}",
			Elapsed: 15,
		},
		{
			ID:      "call_456",
			Output:  "PASS",
			Elapsed: 523,
		},
	}
	result3, err := SaveToolResults(
		context.Background(),
		sess,
		toolResults,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID3 := result3.MessageID

	// Create a test notifier
	notifier := core.NewTestNotifier()

	// Now test ShowSessions - it should display with tool indicators
	err = ShowSessions(notifier)
	if err != nil {
		t.Errorf("ShowSessions() error = %v", err)
	}

	// Test ShowSessionTree
	err = ShowSessionTree(sessionPath, notifier)
	if err != nil {
		t.Errorf("ShowSessionTree() error = %v", err)
	}

	// Test ShowConversation
	err = ShowConversation(sessionPath, notifier)
	if err != nil {
		t.Errorf("ShowConversation() error = %v", err)
	}

	// Test ShowMessage with tool interaction
	msgPath := filepath.Join(sessionPath, msgID2)
	err = ShowMessage(msgPath, notifier)
	if err != nil {
		t.Errorf("ShowMessage() error = %v", err)
	}

	// Silence unused variables
	_ = msgID1
	_ = msgID3
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
