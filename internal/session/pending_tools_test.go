package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// MockApprover implements the core.Approver interface for testing
type MockApprover struct {
	shouldApprove bool
	approvalError error
}

func (m *MockApprover) Approve(ctx context.Context, command []string) (bool, error) {
	// Check if context is already cancelled
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	if m.approvalError != nil {
		return false, m.approvalError
	}
	return m.shouldApprove, nil
}

// MockLogger implements the core.Logger interface for testing
type MockLogger struct {
	debugMessages []string
	debugEnabled  bool
	logDir        string
	mu            sync.Mutex
}

func (m *MockLogger) GetLogDir() string {
	if m.logDir == "" {
		return "/tmp"
	}
	return m.logDir
}

func (m *MockLogger) LogJSON(logDir, prefix string, data []byte) error {
	return nil
}

func (m *MockLogger) CreateLogFile(logDir, prefix string) (*os.File, string, error) {
	f, err := os.CreateTemp(logDir, prefix)
	return f, f.Name(), err
}

func (m *MockLogger) Debugf(format string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.debugMessages = append(m.debugMessages, fmt.Sprintf(format, args...))
}

func (m *MockLogger) IsDebugEnabled() bool {
	return m.debugEnabled
}

func (m *MockLogger) Infof(format string, args ...interface{})  {}
func (m *MockLogger) Warnf(format string, args ...interface{})  {}
func (m *MockLogger) Errorf(format string, args ...interface{}) {}

// TestExecutePendingTools tests the pending tools execution functionality
func TestExecutePendingTools(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	sessionsDir := filepath.Join(tempDir, "sessions")
	SetSessionsDir(sessionsDir)

	tests := []struct {
		name             string
		setupSession     func() (*Session, error)
		expectHasPending bool
		expectError      bool
		approverBehavior bool
		checkResults     func(t *testing.T, sess *Session)
	}{
		{
			name: "no_pending_tools",
			setupSession: func() (*Session, error) {
				// Create a session with no pending tools
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return nil, err
				}
				// Add a user message
				msg := Message{
					Role:      "user",
					Content:   "Hello",
					Timestamp: time.Now(),
				}
				_, err = AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil)
				return sess, err
			},
			expectHasPending: false,
			expectError:      false,
		},
		{
			name: "pending_tools_approved",
			setupSession: func() (*Session, error) {
				// Create a session with pending tool calls
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return nil, err
				}

				// Add an assistant message with tool calls
				assistantMsg := Message{
					Role:      "assistant",
					Content:   "I'll help you list files",
					Timestamp: time.Now(),
					Model:     "test-model",
				}
				toolCalls := []core.ToolCall{
					{
						ID:   "call_123",
						Name: "universal_command",
						Args: json.RawMessage(`{"command":["ls","-la"]}`),
					},
				}

				_, err = AppendMessageWithToolInteraction(context.Background(), sess, assistantMsg, toolCalls, nil)
				return sess, err
			},
			expectHasPending: true,
			expectError:      false,
			approverBehavior: true,
			checkResults: func(t *testing.T, sess *Session) {
				// Verify that tool results were saved
				messages, err := GetLineage(sess.Path)
				if err != nil {
					t.Fatalf("Failed to get lineage: %v", err)
				}

				// Should have 2 messages: assistant with tool calls, user with tool results
				if len(messages) != 2 {
					t.Errorf("Expected 2 messages, got %d", len(messages))
				}

				if len(messages) >= 2 {
					lastMsg := messages[len(messages)-1]
					if lastMsg.Role != "user" {
						t.Errorf("Expected last message to be user role, got %s", lastMsg.Role)
					}

					// Check for tool results
					toolInteraction, err := LoadToolInteraction(sess.Path, lastMsg.ID)
					if err != nil {
						t.Fatalf("Failed to load tool interaction: %v", err)
					}

					if toolInteraction == nil || len(toolInteraction.Results) == 0 {
						t.Error("Expected tool results to be saved")
					}
				}
			},
		},
		{
			name: "pending_tools_denied",
			setupSession: func() (*Session, error) {
				// Create a session with pending tool calls
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return nil, err
				}

				// Add an assistant message with tool calls
				assistantMsg := Message{
					Role:      "assistant",
					Content:   "I'll help you remove files",
					Timestamp: time.Now(),
					Model:     "test-model",
				}
				toolCalls := []core.ToolCall{
					{
						ID:   "call_456",
						Name: "universal_command",
						Args: json.RawMessage(`{"command":["rm","-rf","/"]}`),
					},
				}

				_, err = AppendMessageWithToolInteraction(context.Background(), sess, assistantMsg, toolCalls, nil)
				return sess, err
			},
			expectHasPending: true,
			expectError:      false, // Denial is not an error, results are still saved
			approverBehavior: false,
			checkResults: func(t *testing.T, sess *Session) {
				// Verify that tool results were saved with error
				messages, err := GetLineage(sess.Path)
				if err != nil {
					t.Fatalf("Failed to get lineage: %v", err)
				}

				if len(messages) >= 2 {
					lastMsg := messages[len(messages)-1]
					toolInteraction, err := LoadToolInteraction(sess.Path, lastMsg.ID)
					if err != nil {
						t.Fatalf("Failed to load tool interaction: %v", err)
					}

					if toolInteraction == nil || len(toolInteraction.Results) == 0 {
						t.Error("Expected tool results to be saved even for denied commands")
					}

					// Check that the result contains an error
					if len(toolInteraction.Results) > 0 && toolInteraction.Results[0].Error == "" {
						t.Error("Expected error in tool result for denied command")
					}
				}
			},
		},
		{
			name: "multiple_pending_tools",
			setupSession: func() (*Session, error) {
				// Create a session with multiple pending tool calls
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return nil, err
				}

				// Add an assistant message with multiple tool calls
				assistantMsg := Message{
					Role:      "assistant",
					Content:   "I'll help you with multiple tasks",
					Timestamp: time.Now(),
					Model:     "test-model",
				}
				toolCalls := []core.ToolCall{
					{
						ID:   "call_001",
						Name: "universal_command",
						Args: json.RawMessage(`{"command":["echo","Hello"]}`),
					},
					{
						ID:   "call_002",
						Name: "universal_command",
						Args: json.RawMessage(`{"command":["echo","World"]}`),
					},
					{
						ID:   "call_003",
						Name: "universal_command",
						Args: json.RawMessage(`{"command":["pwd"]}`),
					},
				}

				_, err = AppendMessageWithToolInteraction(context.Background(), sess, assistantMsg, toolCalls, nil)
				return sess, err
			},
			expectHasPending: true,
			expectError:      false,
			approverBehavior: true,
			checkResults: func(t *testing.T, sess *Session) {
				messages, err := GetLineage(sess.Path)
				if err != nil {
					t.Fatalf("Failed to get lineage: %v", err)
				}

				if len(messages) >= 2 {
					lastMsg := messages[len(messages)-1]
					toolInteraction, err := LoadToolInteraction(sess.Path, lastMsg.ID)
					if err != nil {
						t.Fatalf("Failed to load tool interaction: %v", err)
					}

					if toolInteraction == nil || len(toolInteraction.Results) != 3 {
						t.Errorf("Expected 3 tool results, got %d", len(toolInteraction.Results))
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup session
			sess, err := tt.setupSession()
			if err != nil {
				t.Fatalf("Failed to setup session: %v", err)
			}

			// Create test dependencies
			ctx := context.Background()
			cfg := core.NewTestRequestConfig()
			cfg.IsToolEnabledFlag = true
			cfg.ToolTimeout = 5 * time.Second
			logger := &MockLogger{debugEnabled: true}
			notifier := &pendingTestNotifier{}
			approver := &MockApprover{shouldApprove: tt.approverBehavior}

			// Execute pending tools
			hasPending, err := ExecutePendingTools(ctx, sess, cfg, logger, notifier, approver)

			// Check expectations
			if hasPending != tt.expectHasPending {
				t.Errorf("Expected hasPending=%v, got %v", tt.expectHasPending, hasPending)
			}

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			// Run additional checks if provided
			if tt.checkResults != nil {
				tt.checkResults(t, sess)
			}
		})
	}
}

func TestExecutePendingToolsRequiresToolFlag(t *testing.T) {
	tempDir := t.TempDir()
	SetSessionsDir(filepath.Join(tempDir, "sessions"))

	sess, err := CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	assistantMsg := Message{
		Role:      "assistant",
		Content:   "I'll run a command",
		Timestamp: time.Now(),
		Model:     "test-model",
	}
	toolCalls := []core.ToolCall{
		{
			ID:   "call_123",
			Name: "universal_command",
			Args: json.RawMessage(`{"command":["echo","ok"]}`),
		},
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), sess, assistantMsg, toolCalls, nil); err != nil {
		t.Fatalf("AppendMessageWithToolInteraction failed: %v", err)
	}

	cfg := core.NewTestRequestConfig()
	cfg.IsToolEnabledFlag = false
	hasPending, err := ExecutePendingTools(context.Background(), sess, cfg, &MockLogger{}, &pendingTestNotifier{}, &MockApprover{shouldApprove: true})
	if !hasPending {
		t.Fatal("expected pending tools to be reported")
	}
	if err == nil || !strings.Contains(err.Error(), "require -tool") {
		t.Fatalf("expected requires -tool error, got %v", err)
	}

	pending, err := CheckForPendingToolCalls(context.Background(), sess.Path)
	if err != nil {
		t.Fatalf("CheckForPendingToolCalls failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending tool calls should remain unexecuted, got %d", len(pending))
	}
}

// TestCheckForPendingToolCalls tests the detection of pending tool calls
func TestCheckForPendingToolCalls(t *testing.T) {
	tempDir := t.TempDir()
	sessionsDir := filepath.Join(tempDir, "sessions")
	SetSessionsDir(sessionsDir)

	tests := []struct {
		name         string
		setupSession func() (string, error)
		expectCalls  int
	}{
		{
			name: "no_messages",
			setupSession: func() (string, error) {
				sess, err := CreateSession("", core.NewTestLogger(false))
				return sess.Path, err
			},
			expectCalls: 0,
		},
		{
			name: "last_message_is_user",
			setupSession: func() (string, error) {
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return "", err
				}
				msg := Message{
					Role:      "user",
					Content:   "Hello",
					Timestamp: time.Now(),
				}
				_, err = AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil)
				return sess.Path, err
			},
			expectCalls: 0,
		},
		{
			name: "assistant_message_without_tools",
			setupSession: func() (string, error) {
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return "", err
				}
				msg := Message{
					Role:      "assistant",
					Content:   "Hello there!",
					Timestamp: time.Now(),
				}
				_, err = AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil)
				return sess.Path, err
			},
			expectCalls: 0,
		},
		{
			name: "assistant_message_with_pending_tools",
			setupSession: func() (string, error) {
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return "", err
				}
				msg := Message{
					Role:      "assistant",
					Content:   "Let me help",
					Timestamp: time.Now(),
				}
				toolCalls := []core.ToolCall{
					{
						ID:   "call_123",
						Name: "test_tool",
						Args: json.RawMessage(`{}`),
					},
				}
				_, err = AppendMessageWithToolInteraction(context.Background(), sess, msg, toolCalls, nil)
				return sess.Path, err
			},
			expectCalls: 1,
		},
		{
			name: "tools_already_executed",
			setupSession: func() (string, error) {
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					return "", err
				}

				// Assistant message with tool calls
				assistantMsg := Message{
					Role:      "assistant",
					Content:   "Let me help",
					Timestamp: time.Now(),
				}
				toolCalls := []core.ToolCall{
					{
						ID:   "call_123",
						Name: "test_tool",
						Args: json.RawMessage(`{}`),
					},
				}
				_, err = AppendMessageWithToolInteraction(context.Background(), sess, assistantMsg, toolCalls, nil)
				if err != nil {
					return "", err
				}

				// User message with tool results
				userMsg := Message{
					Role:      "user",
					Content:   "",
					Timestamp: time.Now(),
				}
				toolResults := []core.ToolResult{
					{
						ID:     "call_123",
						Output: "Success",
					},
				}
				_, err = AppendMessageWithToolInteraction(context.Background(), sess, userMsg, nil, toolResults)

				return sess.Path, err
			},
			expectCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionPath, err := tt.setupSession()
			if err != nil {
				t.Fatalf("Failed to setup session: %v", err)
			}

			calls, err := CheckForPendingToolCalls(context.Background(), sessionPath)
			if err != nil {
				t.Fatalf("Failed to check for pending tools: %v", err)
			}

			if len(calls) != tt.expectCalls {
				t.Errorf("Expected %d pending calls, got %d", tt.expectCalls, len(calls))
			}
		})
	}
}

// pendingTestNotifier implements core.Notifier for testing
type pendingTestNotifier struct {
	messages []string
}

func (t *pendingTestNotifier) Infof(format string, args ...interface{}) {
	t.messages = append(t.messages, fmt.Sprintf("INFO: "+format, args...))
}

func (t *pendingTestNotifier) Warnf(format string, args ...interface{}) {
	t.messages = append(t.messages, fmt.Sprintf("WARN: "+format, args...))
}

func (t *pendingTestNotifier) Errorf(format string, args ...interface{}) {
	t.messages = append(t.messages, fmt.Sprintf("ERROR: "+format, args...))
}

func (t *pendingTestNotifier) Promptf(format string, args ...interface{}) {
	t.messages = append(t.messages, fmt.Sprintf("PROMPT: "+format, args...))
}
