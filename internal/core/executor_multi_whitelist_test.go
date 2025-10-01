package core

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockLoggerMulti implements ExecLogger for testing
type mockLoggerMulti struct {
	debugEnabled bool
	logs         []string
}

func (m *mockLoggerMulti) Debugf(format string, args ...interface{}) {
	if m.debugEnabled {
		m.logs = append(m.logs, fmt.Sprintf(format, args...))
	}
}

func (m *mockLoggerMulti) IsDebugEnabled() bool {
	return m.debugEnabled
}

// Test configuration that implements RequestConfig interface
type testExecutorConfigMulti struct {
	toolWhitelist   string
	toolBlacklist   string
	toolAutoApprove bool
	toolTimeout     int
}

func (c testExecutorConfigMulti) GetUser() string             { return "testuser" }
func (c testExecutorConfigMulti) GetModel() string            { return "test-model" }
func (c testExecutorConfigMulti) GetProvider() string         { return "test" }
func (c testExecutorConfigMulti) GetSystem() string           { return "" }
func (c testExecutorConfigMulti) IsToolEnabled() bool         { return true }
func (c testExecutorConfigMulti) GetToolWhitelist() string    { return c.toolWhitelist }
func (c testExecutorConfigMulti) GetToolBlacklist() string    { return c.toolBlacklist }
func (c testExecutorConfigMulti) GetToolAutoApprove() bool    { return c.toolAutoApprove }
func (c testExecutorConfigMulti) GetToolNonInteractive() bool { return true }
func (c testExecutorConfigMulti) GetToolTimeout() time.Duration {
	if c.toolTimeout > 0 {
		return time.Duration(c.toolTimeout) * time.Second
	}
	return 30 * time.Second
}
func (c testExecutorConfigMulti) IsSystemExplicitlySet() bool { return false }
func (c testExecutorConfigMulti) GetInput() string            { return "" }
func (c testExecutorConfigMulti) IsEmbed() bool               { return false }
func (c testExecutorConfigMulti) IsStreamChat() bool          { return false }
func (c testExecutorConfigMulti) GetProviderURL() string      { return "" }
func (c testExecutorConfigMulti) GetAPIKeyFile() string       { return "" }
func (c testExecutorConfigMulti) GetEnv() string              { return "" }
func (c testExecutorConfigMulti) GetMaxTokens() int           { return 0 }
func (c testExecutorConfigMulti) GetTimeout() time.Duration   { return 30 * time.Second }
func (c testExecutorConfigMulti) GetRetries() int             { return 0 }
func (c testExecutorConfigMulti) GetMaxToolRounds() int       { return 32 }
func (c testExecutorConfigMulti) GetMaxToolParallel() int     { return 4 }
func (c testExecutorConfigMulti) GetToolMaxOutputBytes() int  { return 1024 * 1024 } // 1MB default
func (c testExecutorConfigMulti) GetAPIKey() string           { return "test-key" }
func (c testExecutorConfigMulti) GetEffectiveSystem() string  { return c.GetSystem() }
func (c testExecutorConfigMulti) GetResume() string           { return "" }
func (c testExecutorConfigMulti) GetBranch() string           { return "" }

func TestExecutorMultiArgumentWhitelist(t *testing.T) {
	// Create a temporary whitelist file with multi-argument commands
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")

	whitelistContent := `# All entries are now JSON arrays
["echo"]
["ls"]
["git", "status"]
["git", "log"]
["npm", "test"]
["docker", "ps", "-a"]
`

	if err := os.WriteFile(whitelistPath, []byte(whitelistContent), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create whitelist file: %v", err)
	}

	cfg := testExecutorConfigMulti{
		toolWhitelist:   whitelistPath,
		toolAutoApprove: false, // Make sure we're testing whitelist, not auto-approve
	}

	logger := &mockLoggerMulti{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	tests := []struct {
		name     string
		command  []string
		expected bool
	}{
		// Simple commands (now prefix-based)
		{"echo allowed", []string{"echo", "hello"}, true},
		{"ls allowed", []string{"ls", "-la"}, true},
		{"rm not allowed", []string{"rm", "dangerous-file"}, false},

		// Multi-argument prefix matches
		{"git status exact", []string{"git", "status"}, true},
		{"git status with extra args", []string{"git", "status", "-s"}, true}, // Now allowed via prefix
		{"npm test exact", []string{"npm", "test"}, true},
		{"npm test with args", []string{"npm", "test", "--coverage"}, true}, // Now allowed via prefix
		{"npm install not allowed", []string{"npm", "install"}, false},
		{"docker ps -a exact", []string{"docker", "ps", "-a"}, true},
		{"docker ps without -a", []string{"docker", "ps"}, false}, // Still not allowed - doesn't match prefix

		// Prefix matching
		{"git log simple", []string{"git", "log"}, true},
		{"git log with args", []string{"git", "log", "--oneline", "-10"}, true}, // Allowed via prefix
		{"git diff not allowed", []string{"git", "diff"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test with the ApprovalPolicy
			policy := ApprovalPolicy{
				Whitelist:   executor.whitelist,
				Blacklist:   executor.blacklist,
				AutoApprove: executor.autoApprove,
				Interactive: false, // Non-interactive for testing
			}
			decision := policy.Decide(tt.command)
			approved := decision == DecisionAllow
			if approved != tt.expected {
				t.Errorf("Command %v: expected approved=%v, got %v", tt.command, tt.expected, approved)
			}
		})
	}
}

func TestExecutorMultiArgumentWhitelistExecution(t *testing.T) {
	// Create a temporary whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")

	whitelistContent := `["echo", "test"]
["echo", "multi"]
`

	if err := os.WriteFile(whitelistPath, []byte(whitelistContent), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create whitelist file: %v", err)
	}

	cfg := testExecutorConfigMulti{
		toolWhitelist:   whitelistPath,
		toolAutoApprove: true, // Set to true to avoid TTY prompts
	}

	logger := &mockLoggerMulti{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test exact match
	call1 := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: json.RawMessage(`{"command": ["echo", "test"]}`),
	}

	// Test wildcard match
	call2 := ToolCall{
		ID:   "test-2",
		Name: "universal_command",
		Args: json.RawMessage(`{"command": ["echo", "multi", "arg", "test"]}`),
	}

	// Test non-matching command
	call3 := ToolCall{
		ID:   "test-3",
		Name: "universal_command",
		Args: json.RawMessage(`{"command": ["ls", "test"]}`),
	}

	ctx := context.Background()

	// Execute whitelisted commands
	result1 := executor.executeSingle(ctx, call1)
	if result1.Error != "" {
		t.Errorf("Expected echo test to succeed, got error: %s", result1.Error)
	}

	result2 := executor.executeSingle(ctx, call2)
	if result2.Error != "" {
		t.Errorf("Expected echo multi arg test to succeed, got error: %s", result2.Error)
	}

	// Execute non-whitelisted command (should fail)
	result3 := executor.executeSingle(ctx, call3)
	if !strings.HasPrefix(result3.Error, "denied: not in whitelist") {
		t.Errorf("Expected error to start with 'denied: not in whitelist', got: %s", result3.Error)
	}
}
