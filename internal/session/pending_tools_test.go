package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// MockApprover implements the core.Approver interface for testing
type MockApprover struct {
	core.DeclineNonCommandApprovals
	shouldApprove bool
	approvalError error
}

func (m *MockApprover) Approve(ctx context.Context, _ core.UniversalCommandArgs) (bool, error) {
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

// pendingCallSession creates a session whose head is an assistant message
// holding calls, with nothing answering them.
func pendingCallSession(t *testing.T, calls []core.ToolCall) *Session {
	t.Helper()
	sess, err := CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	ctx := context.Background()
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleUser, Content: "run it", Timestamp: time.Now()}, nil, nil); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant, Content: "running", Timestamp: time.Now(), Model: "test-model"}, calls, nil); err != nil {
		t.Fatalf("append assistant tool calls: %v", err)
	}
	return sess
}

// claimedNeverStarted claims calls the way the tool loop does before saving
// them, then releases the claim as a run that died before starting any would.
func claimedNeverStarted(t *testing.T, calls []core.ToolCall) {
	t.Helper()
	claim, err := NewToolJournal().Claim(calls)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	claim.Release()
}

func pendingToolTestConfig() core.RequestOptions {
	cfg := core.NewTestRequestConfig()
	cfg.ToolEnabled = true
	cfg.ToolTimeout = 5 * time.Second
	return cfg
}

func echoCall(id, invocation, text string) core.ToolCall {
	return core.ToolCall{
		ID:           id,
		Name:         "universal_command",
		Args:         json.RawMessage(fmt.Sprintf(`{"command":["echo",%q]}`, text)),
		InvocationID: invocation,
	}
}

func lastToolResults(t *testing.T, sess *Session) []core.ToolResult {
	t.Helper()
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("GetLineage() error = %v", err)
	}
	last := messages[len(messages)-1]
	interaction, err := LoadToolInteraction(sess.Path, last.ID)
	if err != nil {
		t.Fatalf("LoadToolInteraction() error = %v", err)
	}
	if interaction == nil {
		t.Fatalf("last message %s carries no tool results", last.ID)
	}
	return interaction.Results
}

func TestResolvePendingToolCallsWithoutPendingCallsDoesNothing(t *testing.T) {
	UseTestSessionDir(t)
	sess, err := CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), sess, Message{Role: core.RoleUser, Content: "Hello", Timestamp: time.Now()}, nil, nil); err != nil {
		t.Fatalf("append: %v", err)
	}

	resolution, err := ResolvePendingToolCalls(context.Background(), sess, pendingToolTestConfig(), &MockLogger{}, core.NewTestNotifier(), core.TestToolUI{}, &MockApprover{shouldApprove: true})
	if err != nil {
		t.Fatalf("ResolvePendingToolCalls() error = %v", err)
	}
	if resolution.Found || resolution.Committed {
		t.Fatalf("resolution = %+v, want nothing found or committed", resolution)
	}
}

func TestResolvePendingToolCallsRunsClaimedCallsThatNeverStarted(t *testing.T) {
	UseTestSessionDir(t)
	calls := []core.ToolCall{
		echoCall("call_001", core.NewInvocationID(), "Hello"),
		echoCall("call_002", core.NewInvocationID(), "World"),
	}
	claimedNeverStarted(t, calls)
	sess := pendingCallSession(t, calls)

	resolution, err := ResolvePendingToolCalls(context.Background(), sess, pendingToolTestConfig(), &MockLogger{}, core.NewTestNotifier(), core.TestToolUI{}, &MockApprover{shouldApprove: true})
	if err != nil {
		t.Fatalf("ResolvePendingToolCalls() error = %v", err)
	}
	if !resolution.Found || !resolution.Committed {
		t.Fatalf("resolution = %+v, want found and committed", resolution)
	}
	results := lastToolResults(t, sess)
	if len(results) != 2 || !strings.Contains(results[0].Output, "Hello") || !strings.Contains(results[1].Output, "World") {
		t.Fatalf("results = %+v, want both commands run", results)
	}
	if pending, err := CheckForPendingToolCalls(context.Background(), sess.Path); err != nil || len(pending) != 0 {
		t.Fatalf("pending after resolution = %v (err %v), want none", pending, err)
	}
}

// A call saved before invocations had identities may already have run, so
// it is uncertain: without an operator to ask, it is answered with an unknown
// outcome and not run.
func TestResolvePendingToolCallsDoesNotRerunLegacyCalls(t *testing.T) {
	UseTestSessionDir(t)
	marker := filepath.Join(t.TempDir(), "ran")
	calls := []core.ToolCall{{
		ID:   "call_legacy",
		Name: "universal_command",
		Args: json.RawMessage(fmt.Sprintf(`{"command":["touch",%q]}`, marker)),
	}}
	sess := pendingCallSession(t, calls)

	resolution, err := ResolvePendingToolCalls(context.Background(), sess, pendingToolTestConfig(), &MockLogger{}, core.NewTestNotifier(), core.TestToolUI{}, &MockApprover{shouldApprove: true})
	if err != nil {
		t.Fatalf("ResolvePendingToolCalls() error = %v", err)
	}
	if !resolution.Committed {
		t.Fatalf("resolution = %+v, want the unknown outcome committed", resolution)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("legacy call ran again, marker stat err = %v", err)
	}
	results := lastToolResults(t, sess)
	if len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown || results[0].NotRun {
		t.Fatalf("results = %+v, want one unknown outcome that does not claim the call never ran", results)
	}
}

func TestResolvePendingToolCallsRequiresToolFlag(t *testing.T) {
	UseTestSessionDir(t)
	sess := pendingCallSession(t, []core.ToolCall{echoCall("call_123", core.NewInvocationID(), "ok")})

	cfg := core.NewTestRequestConfig()
	cfg.ToolEnabled = false
	resolution, err := ResolvePendingToolCalls(context.Background(), sess, cfg, &MockLogger{}, core.NewTestNotifier(), core.TestToolUI{}, &MockApprover{shouldApprove: true})
	if !resolution.Found {
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
	UseTestSessionDir(t)

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
