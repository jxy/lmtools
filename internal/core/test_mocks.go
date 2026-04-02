package core

import (
	"context"
	"lmtools/internal/prompts"
	"os"
	"strings"
	"sync"
	"time"
)

// TestRequestConfig is a unified mock that implements RequestConfig for testing
type TestRequestConfig struct {
	User                string
	Model               string
	System              string
	SystemExplicitlySet bool
	Env                 string
	Provider            string
	ProviderURL         string
	APIKeyFile          string
	IsEmbedMode         bool
	IsStreamChatMode    bool
	IsToolEnabledFlag   bool
	ToolTimeout         time.Duration
	ToolWhitelist       string
	ToolBlacklist       string
	ToolAutoApprove     bool
	ToolNonInteractive  bool
	MaxToolRounds       int
	MaxToolParallel     int
	ToolMaxOutputBytes  int
	Resume              string
	Branch              string
}

// Implement RequestConfig interface
func (c *TestRequestConfig) GetUser() string   { return c.User }
func (c *TestRequestConfig) GetModel() string  { return c.Model }
func (c *TestRequestConfig) GetSystem() string { return c.System }
func (c *TestRequestConfig) GetEffectiveSystem() string {
	if c.System != "" {
		return c.System
	}
	return prompts.DefaultSystemPrompt
}
func (c *TestRequestConfig) IsSystemExplicitlySet() bool { return c.SystemExplicitlySet }
func (c *TestRequestConfig) GetEnv() string              { return c.Env }
func (c *TestRequestConfig) GetProvider() string         { return c.Provider }
func (c *TestRequestConfig) GetProviderURL() string      { return c.ProviderURL }
func (c *TestRequestConfig) GetAPIKeyFile() string       { return c.APIKeyFile }
func (c *TestRequestConfig) IsEmbed() bool               { return c.IsEmbedMode }
func (c *TestRequestConfig) IsStreamChat() bool          { return c.IsStreamChatMode }
func (c *TestRequestConfig) IsToolEnabled() bool         { return c.IsToolEnabledFlag }
func (c *TestRequestConfig) GetToolTimeout() time.Duration {
	if c.ToolTimeout > 0 {
		return c.ToolTimeout
	}
	return 30 * time.Second
}
func (c *TestRequestConfig) GetToolWhitelist() string    { return c.ToolWhitelist }
func (c *TestRequestConfig) GetToolBlacklist() string    { return c.ToolBlacklist }
func (c *TestRequestConfig) GetToolAutoApprove() bool    { return c.ToolAutoApprove }
func (c *TestRequestConfig) GetToolNonInteractive() bool { return c.ToolNonInteractive }
func (c *TestRequestConfig) GetMaxToolRounds() int {
	if c.MaxToolRounds > 0 {
		return c.MaxToolRounds
	}
	return 32
}

func (c *TestRequestConfig) GetMaxToolParallel() int {
	if c.MaxToolParallel > 0 {
		return c.MaxToolParallel
	}
	return 4
}

func (c *TestRequestConfig) GetToolMaxOutputBytes() int {
	if c.ToolMaxOutputBytes > 0 {
		return c.ToolMaxOutputBytes
	}
	return 1024 * 1024 // 1MB default
}
func (c *TestRequestConfig) GetResume() string { return c.Resume }
func (c *TestRequestConfig) GetBranch() string { return c.Branch }

// NewTestRequestConfig creates a TestRequestConfig with default values
func NewTestRequestConfig() *TestRequestConfig {
	return &TestRequestConfig{
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
