package core

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"
)

type TestRequestConfig = RequestOptions

// NewTestRequestConfig creates a TestRequestConfig with default values
func NewTestRequestConfig() RequestOptions {
	return RequestOptions{
		User:          "testuser",
		Model:         "test-model",
		System:        "You are a helpful assistant",
		Env:           "test",
		Provider:      "argo",
		MaxToolRounds: 32,
		ToolTimeout:   30 * time.Second,
	}
}

// TestSession is a mock that implements Session interface for testing
type TestSession struct {
	Path string
}

func (s *TestSession) GetPath() string { return s.Path }

// NewTestSession creates a TestSession with a given path
func NewTestSession(path string) *TestSession {
	return &TestSession{Path: path}
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
	ApprovalResponses map[string]bool // Map of command JSON to approval response
	DefaultApproval   bool
	ApprovalCalls     [][]string // Track what commands were asked for approval
	mu                sync.Mutex
}

func (a *TestApprover) Approve(ctx context.Context, command []string) (bool, error) {
	// Check if context is already cancelled
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.ApprovalCalls = append(a.ApprovalCalls, command)

	// Check if we have a specific response for this command
	cmdKey := strings.Join(command, " ")
	if approved, exists := a.ApprovalResponses[cmdKey]; exists {
		return approved, nil
	}

	return a.DefaultApproval, nil
}

// NewTestApprover creates a TestApprover with default approval
func NewTestApprover(defaultApproval bool) *TestApprover {
	return &TestApprover{
		ApprovalResponses: make(map[string]bool),
		DefaultApproval:   defaultApproval,
		ApprovalCalls:     [][]string{},
	}
}

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
