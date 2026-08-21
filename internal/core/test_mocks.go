package core

import (
	"context"
	"os"
	"sync"
)

type TestRequestConfig = RequestOptions

// NewTestRequestConfig creates a TestRequestConfig with default values
func NewTestRequestConfig() RequestOptions {
	return RequestOptions{
		User:            "testuser",
		Model:           "test-model",
		System:          "You are a helpful assistant",
		Env:             "test",
		Provider:        "argo",
		MaxToolRounds:   DefaultMaxToolRounds,
		MaxToolParallel: DefaultMaxToolParallel,
		ToolTimeout:     DefaultToolTimeout,
	}
}

// TestLogger is a mock that implements Logger interface for testing
type TestLogger struct {
	DebugMessages []string
	InfoMessages  []string
	WarnMessages  []string
	ErrorMessages []string
	DebugEnabled  bool
	mu            sync.Mutex
}

func (l *TestLogger) Debugf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.DebugEnabled {
		l.DebugMessages = append(l.DebugMessages, format)
	}
}

func (l *TestLogger) Infof(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.InfoMessages = append(l.InfoMessages, format)
}

func (l *TestLogger) Warnf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.WarnMessages = append(l.WarnMessages, format)
}

func (l *TestLogger) Errorf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ErrorMessages = append(l.ErrorMessages, format)
}

func (l *TestLogger) IsDebugEnabled() bool {
	return l.DebugEnabled
}

func (l *TestLogger) LogJSON(logDir, prefix string, data []byte) error {
	// Mock implementation - just record that it was called
	l.mu.Lock()
	defer l.mu.Unlock()
	l.DebugMessages = append(l.DebugMessages, "LogJSON:"+prefix)
	return nil
}

func (l *TestLogger) GetLogDir() string {
	return "/tmp/test-logs"
}

func (l *TestLogger) CreateLogFile(prefix string, purpose string) (*os.File, string, error) {
	// Mock implementation - just return nil since tests don't need actual files
	return nil, "", nil
}

// NewTestLogger creates a TestLogger
func NewTestLogger(debugEnabled bool) *TestLogger {
	return &TestLogger{
		DebugEnabled:  debugEnabled,
		DebugMessages: []string{},
		InfoMessages:  []string{},
		WarnMessages:  []string{},
		ErrorMessages: []string{},
	}
}

// TestApprover is a mock that implements Approver interface for testing
type TestApprover struct {
	DefaultApproval bool
	ApprovalCalls   []UniversalCommandArgs
	// ResetResponses answers round-limit reset prompts in order; an exhausted
	// or empty queue declines. ResetCalls records the maxRounds of each prompt.
	ResetResponses []bool
	ResetCalls     []int
	mu             sync.Mutex
}

func (a *TestApprover) Approve(ctx context.Context, args UniversalCommandArgs) (bool, error) {
	// Check if context is already cancelled
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.ApprovalCalls = append(a.ApprovalCalls, args)
	return a.DefaultApproval, nil
}

func (a *TestApprover) ApproveToolRoundLimitReset(ctx context.Context, maxRounds int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.ResetCalls = append(a.ResetCalls, maxRounds)
	if len(a.ResetResponses) == 0 {
		return false, nil
	}
	approved := a.ResetResponses[0]
	a.ResetResponses = a.ResetResponses[1:]
	return approved, nil
}

// DeclineToolRoundLimitReset is the round-limit half of Approver for test
// doubles that only care about Approve. Embed it to decline every reset.
type DeclineToolRoundLimitReset struct{}

func (DeclineToolRoundLimitReset) ApproveToolRoundLimitReset(ctx context.Context, _ int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// NewTestApprover creates a TestApprover with default approval
func NewTestApprover(defaultApproval bool) *TestApprover {
	return &TestApprover{
		DefaultApproval: defaultApproval,
	}
}

// TestToolUI is a no-op ToolUI for tests that need the reviewed-UI contract
// satisfied without asserting on rendering.
type TestToolUI struct{}

func (TestToolUI) ShowCall(int, int, ToolCall, *UniversalCommandArgs) {}
func (TestToolUI) BeforeRun(int, int, int)                            {}
func (TestToolUI) AfterExecute([]ToolCall, []ToolResult)              {}

// TestNotifier is a mock that implements Notifier interface for testing
type TestNotifier struct {
	mu             sync.Mutex
	InfoMessages   []string
	WarnMessages   []string
	ErrorMessages  []string
	PromptMessages []string
}

func (n *TestNotifier) Infof(format string, args ...interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.InfoMessages = append(n.InfoMessages, format)
}

func (n *TestNotifier) Warnf(format string, args ...interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.WarnMessages = append(n.WarnMessages, format)
}

func (n *TestNotifier) Errorf(format string, args ...interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ErrorMessages = append(n.ErrorMessages, format)
}

func (n *TestNotifier) Promptf(format string, args ...interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.PromptMessages = append(n.PromptMessages, format)
}

// NewTestNotifier creates a TestNotifier
func NewTestNotifier() *TestNotifier {
	return &TestNotifier{
		InfoMessages:   []string{},
		WarnMessages:   []string{},
		ErrorMessages:  []string{},
		PromptMessages: []string{},
	}
}
