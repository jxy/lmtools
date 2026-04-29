package core

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockLogger implements ExecLogger for testing
type mockLogger struct {
	debugEnabled bool
	logs         []string
	mu           sync.Mutex
}

func (m *mockLogger) Debugf(format string, args ...interface{}) {
	if m.debugEnabled {
		m.mu.Lock()
		m.logs = append(m.logs, fmt.Sprintf(format, args...))
		m.mu.Unlock()
	}
}

func (m *mockLogger) IsDebugEnabled() bool {
	return m.debugEnabled
}

// mockRequestConfig for testing executor
type mockExecutorConfig struct {
	enableTool      bool
	toolTimeout     time.Duration
	toolWhitelist   string
	toolBlacklist   string
	toolAutoApprove bool
}

func (m mockExecutorConfig) GetUser() string               { return "testuser" }
func (m mockExecutorConfig) GetModel() string              { return "test-model" }
func (m mockExecutorConfig) GetSystem() string             { return "test system" }
func (m mockExecutorConfig) IsSystemExplicitlySet() bool   { return false }
func (m mockExecutorConfig) GetEnv() string                { return "" }
func (m mockExecutorConfig) IsEmbed() bool                 { return false }
func (m mockExecutorConfig) IsStreamChat() bool            { return false }
func (m mockExecutorConfig) GetProvider() string           { return "test" }
func (m mockExecutorConfig) GetProviderURL() string        { return "" }
func (m mockExecutorConfig) GetAPIKeyFile() string         { return "" }
func (m mockExecutorConfig) GetAPIKey() string             { return "test-key" }
func (m mockExecutorConfig) GetInput() string              { return "" }
func (m mockExecutorConfig) GetMaxTokens() int             { return 0 }
func (m mockExecutorConfig) IsToolEnabled() bool           { return m.enableTool }
func (m mockExecutorConfig) GetToolTimeout() time.Duration { return m.toolTimeout }
func (m mockExecutorConfig) GetToolWhitelist() string      { return m.toolWhitelist }
func (m mockExecutorConfig) GetToolBlacklist() string      { return m.toolBlacklist }
func (m mockExecutorConfig) GetToolAutoApprove() bool      { return m.toolAutoApprove }
func (m mockExecutorConfig) GetToolNonInteractive() bool   { return true }
func (m mockExecutorConfig) GetMaxToolRounds() int         { return 32 }
func (m mockExecutorConfig) GetMaxToolParallel() int       { return 4 }
func (m mockExecutorConfig) GetToolMaxOutputBytes() int    { return 1024 * 1024 } // 1MB default
func (m mockExecutorConfig) GetEffectiveSystem() string    { return m.GetSystem() }
func (m mockExecutorConfig) GetResume() string             { return "" }
func (m mockExecutorConfig) GetBranch() string             { return "" }

func TestExecutorWhitelist(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["echo"]
["ls"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test whitelisted command
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"echo", "hello"},
	})

	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error != "" {
		t.Errorf("Expected no error, got: %s", results[0].Error)
	}

	if !strings.Contains(results[0].Output, "hello") {
		t.Errorf("Expected output to contain 'hello', got: %s", results[0].Output)
	}
}

func TestExecutorBlacklist(t *testing.T) {
	// Create temp blacklist file
	tmpDir := t.TempDir()
	blacklistPath := filepath.Join(tmpDir, "blacklist.txt")
	if err := os.WriteFile(blacklistPath, []byte(`["rm"]
["dd"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolBlacklist:   blacklistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test blacklisted command
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"rm", "would-delete-file.txt"},
	})

	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error == "" {
		t.Error("Expected error for blacklisted command")
	}

	if results[0].Error != "denied: blacklisted" {
		t.Errorf("Expected 'denied: blacklisted', got: %s", results[0].Error)
	}
}

func TestExecutorTimeout(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["sleep"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     100 * time.Millisecond,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test command that will timeout
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"sleep", "5"},
	})

	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	ctx := context.Background()
	start := time.Now()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})
	elapsed := time.Since(start)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error == "" {
		t.Error("Expected timeout error")
	}

	if !strings.Contains(results[0].Error, "timed out") {
		t.Errorf("Expected timeout error, got: %s", results[0].Error)
	}

	// Should timeout quickly
	if elapsed > 500*time.Millisecond {
		t.Errorf("Command took too long to timeout: %v", elapsed)
	}
}

func TestExecutorParallel(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["echo"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create multiple commands
	calls := []ToolCall{}
	for i := 0; i < 3; i++ {
		args, _ := json.Marshal(map[string]interface{}{
			"command": []string{"echo", fmt.Sprintf("test-%d", i)},
		})
		calls = append(calls, ToolCall{
			ID:   fmt.Sprintf("test-%d", i),
			Name: "universal_command",
			Args: args,
		})
	}

	ctx := context.Background()
	start := time.Now()
	results := executor.ExecuteParallel(ctx, calls)
	elapsed := time.Since(start)

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	// Should execute in parallel (not take 3x the time)
	if elapsed > 1*time.Second {
		t.Logf("Warning: Parallel execution might not be working efficiently: %v", elapsed)
	}

	// Check all results
	for i, result := range results {
		if result.Error != "" {
			t.Errorf("Result %d had error: %s", i, result.Error)
		}
		expectedOutput := fmt.Sprintf("test-%d", i)
		if !strings.Contains(result.Output, expectedOutput) {
			t.Errorf("Result %d: expected output to contain '%s', got: %s", i, expectedOutput, result.Output)
		}
	}
}

func TestExecutorEnvironment(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["sh"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test with custom environment variable
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"sh", "-c", "echo $TEST_VAR"},
		"environ": map[string]string{
			"TEST_VAR": "custom_value",
		},
	})

	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error != "" {
		t.Errorf("Expected no error, got: %s", results[0].Error)
	}

	if !strings.Contains(results[0].Output, "custom_value") {
		t.Errorf("Expected output to contain 'custom_value', got: %s", results[0].Output)
	}
}

func TestExecutorMultipleEnvironmentVariables(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["sh"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test multiple environment variables
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"sh", "-c", "echo VAR0=$VAR0,VAR1=$VAR1,VAR2=$VAR2"},
		"environ": map[string]string{
			"VAR0": "first",
			"VAR1": "second",
			"VAR2": "third",
		},
	})
	call := ToolCall{
		ID:   "test-multi-env",
		Name: "universal_command",
		Args: args,
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error != "" {
		t.Errorf("Expected no error, got: %s", results[0].Error)
	}

	expectedOutput := "VAR0=first,VAR1=second,VAR2=third\n"
	if results[0].Output != expectedOutput {
		t.Errorf("Expected output '%s', got '%s'", expectedOutput, results[0].Output)
	}
}

func TestExecutorWorkdir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create temp whitelist file
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["pwd"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test with custom working directory
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"pwd"},
		"workdir": tmpDir,
	})

	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error != "" {
		t.Errorf("Expected no error, got: %s", results[0].Error)
	}

	if !strings.Contains(results[0].Output, tmpDir) {
		t.Errorf("Expected output to contain '%s', got: %s", tmpDir, results[0].Output)
	}
}

func TestExecutorOutputTruncation(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["sh"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Override max output size for testing
	executor.maxOutputSize = 100 // 100 bytes for testing

	// Generate command that produces large output
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"sh", "-c", "for i in $(seq 1 1000); do echo 'This is a long line of output'; done"},
	})

	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error != "" {
		t.Errorf("Expected no error, got: %s", results[0].Error)
	}

	if !results[0].Truncated {
		t.Error("Expected output to be truncated")
	}

	if len(results[0].Output) > 100 {
		t.Errorf("Output should be truncated to 100 bytes, got %d bytes", len(results[0].Output))
	}
}

func TestExecutorInvalidCommand(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	// Add nonexistent command to whitelist to test "not found" error
	if err := os.WriteFile(whitelistPath, []byte(`["nonexistentcommand12345"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	testCases := []struct {
		name     string
		args     interface{}
		errorMsg string
	}{
		{
			name:     "empty command array",
			args:     map[string]interface{}{"command": []string{}},
			errorMsg: "command array cannot be empty",
		},
		{
			name:     "invalid JSON",
			args:     "not json",
			errorMsg: "invalid command format",
		},
		{
			name:     "non-existent command",
			args:     map[string]interface{}{"command": []string{"nonexistentcommand12345"}},
			errorMsg: "not found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var args json.RawMessage
			if s, ok := tc.args.(string); ok {
				args = json.RawMessage(s)
			} else {
				args, _ = json.Marshal(tc.args)
			}

			call := ToolCall{
				ID:   "test-1",
				Name: "universal_command",
				Args: args,
			}

			ctx := context.Background()
			results := executor.ExecuteParallel(ctx, []ToolCall{call})

			if len(results) != 1 {
				t.Fatalf("Expected 1 result, got %d", len(results))
			}

			if results[0].Error == "" {
				t.Error("Expected error")
			}

			if !strings.Contains(strings.ToLower(results[0].Error), strings.ToLower(tc.errorMsg)) {
				t.Errorf("Expected error containing '%s', got: %s", tc.errorMsg, results[0].Error)
			}
		})
	}
}

func TestExecutorUnsupportedTool(t *testing.T) {
	cfg := mockExecutorConfig{
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg, logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	call := ToolCall{
		ID:   "test-1",
		Name: "unsupported_tool",
		Args: json.RawMessage(`{}`),
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error == "" {
		t.Error("Expected error for unsupported tool")
	}

	if !strings.Contains(results[0].Error, "unsupported tool") {
		t.Errorf("Expected 'unsupported tool' error, got: %s", results[0].Error)
	}
}

// TestApprovalPolicy tests the ApprovalPolicy.Decide method
func TestApprovalPolicy(t *testing.T) {
	tests := []struct {
		name         string
		policy       ApprovalPolicy
		cmd          []string
		wantApproved bool
		wantReason   string
	}{
		{
			name:         "empty command",
			policy:       ApprovalPolicy{},
			cmd:          []string{},
			wantApproved: false,
			wantReason:   "invalid command",
		},
		{
			name: "blacklisted command",
			policy: ApprovalPolicy{
				Blacklist: [][]string{{"rm", "-rf"}},
			},
			cmd:          []string{"rm", "-rf", "/"},
			wantApproved: false,
			wantReason:   "denied: blacklisted",
		},
		{
			name: "whitelisted command",
			policy: ApprovalPolicy{
				Whitelist: [][]string{{"ls"}, {"cat"}},
			},
			cmd:          []string{"ls", "-la"},
			wantApproved: true,
			wantReason:   "",
		},
		{
			name: "command not in whitelist",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"ls"}, {"cat"}},
				Interactive: false,
			},
			cmd:          []string{"rm", "file.txt"},
			wantApproved: false,
			wantReason:   "denied: not in whitelist",
		},
		{
			name: "auto-approve enabled",
			policy: ApprovalPolicy{
				AutoApprove: true,
			},
			cmd:          []string{"any", "command"},
			wantApproved: true,
			wantReason:   "",
		},
		{
			name: "requires approval - interactive",
			policy: ApprovalPolicy{
				Interactive: true,
			},
			cmd:          []string{"some", "command"},
			wantApproved: false,
			wantReason:   "requires-approval",
		},
		{
			name: "requires approval - non-interactive",
			policy: ApprovalPolicy{
				Interactive: false,
			},
			cmd:          []string{"some", "command"},
			wantApproved: false,
			wantReason:   "denied: non-interactive",
		},
		{
			name: "blacklist takes precedence over whitelist",
			policy: ApprovalPolicy{
				Whitelist: [][]string{{"git"}},
				Blacklist: [][]string{{"git", "push", "--force"}},
			},
			cmd:          []string{"git", "push", "--force"},
			wantApproved: false,
			wantReason:   "denied: blacklisted",
		},
		{
			name: "partial whitelist match",
			policy: ApprovalPolicy{
				Whitelist: [][]string{{"docker", "ps"}},
			},
			cmd:          []string{"docker", "ps", "-a"},
			wantApproved: true,
			wantReason:   "",
		},
		{
			name: "blacklist partial match",
			policy: ApprovalPolicy{
				Blacklist: [][]string{{"rm", "-rf"}},
			},
			cmd:          []string{"rm", "-rf", "local-file"},
			wantApproved: false,
			wantReason:   "denied: blacklisted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := tt.policy.Decide(tt.cmd)
			approved := decision == DecisionAllow
			if approved != tt.wantApproved {
				t.Errorf("Decide() approved = %v, want %v", approved, tt.wantApproved)
			}

			// Check decision reason
			if tt.wantReason == "" && decision != DecisionAllow {
				t.Errorf("Decide() decision = %v, want DecisionAllow", decision)
			} else if tt.wantReason == "requires-approval" && decision != DecisionRequireApproval {
				t.Errorf("Decide() decision = %v, want DecisionRequireApproval", decision)
			} else if tt.wantReason == "denied: blacklisted" && decision != DecisionDenyBlacklist {
				t.Errorf("Decide() decision = %v, want DecisionDenyBlacklist", decision)
			} else if tt.wantReason == "denied: not whitelisted" && decision != DecisionDenyNotWhitelisted {
				t.Errorf("Decide() decision = %v, want DecisionDenyNotWhitelisted", decision)
			} else if tt.wantReason == "denied: non-interactive" && decision != DecisionDenyNonInteractive {
				t.Errorf("Decide() decision = %v, want DecisionDenyNonInteractive", decision)
			}
		})
	}
}

// TestCommandHasPrefix tests the commandHasPrefix helper function
func TestCommandHasPrefix(t *testing.T) {
	tests := []struct {
		name    string
		cmd     []string
		pattern []string
		want    bool
	}{
		{
			name:    "exact match",
			cmd:     []string{"ls"},
			pattern: []string{"ls"},
			want:    true,
		},
		{
			name:    "prefix match",
			cmd:     []string{"ls", "-la", "/tmp"},
			pattern: []string{"ls"},
			want:    true,
		},
		{
			name:    "multi-element prefix match",
			cmd:     []string{"git", "push", "--force"},
			pattern: []string{"git", "push"},
			want:    true,
		},
		{
			name:    "no match",
			cmd:     []string{"cat", "file.txt"},
			pattern: []string{"ls"},
			want:    false,
		},
		{
			name:    "pattern longer than command",
			cmd:     []string{"ls"},
			pattern: []string{"ls", "-la"},
			want:    false,
		},
		{
			name:    "empty pattern",
			cmd:     []string{"ls"},
			pattern: []string{},
			want:    false,
		},
		{
			name:    "empty command",
			cmd:     []string{},
			pattern: []string{"ls"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandHasPrefix(tt.cmd, tt.pattern); got != tt.want {
				t.Errorf("commandHasPrefix() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsInteractive tests the isInteractive function
func TestIsInteractive(t *testing.T) {
	// This test will pass or fail based on whether it's run in a terminal
	// We can at least verify it doesn't panic
	_ = isInteractive()
}

type gateApprover struct {
	gate  string
	calls [][]string
	mu    sync.Mutex
}

func (a *gateApprover) Approve(ctx context.Context, command []string) (bool, error) {
	a.mu.Lock()
	a.calls = append(a.calls, append([]string(nil), command...))
	a.mu.Unlock()

	if len(command) > 3 && command[3] == "second" {
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
		}
		if err := os.WriteFile(a.gate, []byte("approved"), constants.FilePerm); err != nil {
			return false, err
		}
	}

	return true, nil
}

func TestExecutorParallelApprovesSequentiallyBeforeLaunch(t *testing.T) {
	tmpDir := t.TempDir()
	gate := filepath.Join(tmpDir, "approved")
	approver := &gateApprover{gate: gate}

	cfg := mockExecutorConfig{
		toolTimeout:     5 * time.Second,
		enableTool:      true,
		toolAutoApprove: false,
	}
	logger := &mockLogger{debugEnabled: true}
	executor, err := NewExecutor(cfg, logger, NewTestNotifier(), approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}
	executor.policy.Interactive = true
	executor.policy.AutoApprove = false

	calls := []ToolCall{
		{
			ID:   "call_first",
			Name: "universal_command",
			Args: mustMarshalToolArgs(t, []string{"sh", "-c", `test -f "$1"`, "first", gate}),
		},
		{
			ID:   "call_second",
			Name: "universal_command",
			Args: mustMarshalToolArgs(t, []string{"sh", "-c", `test -f "$1"`, "second", gate}),
		},
	}

	results := executor.ExecuteParallel(context.Background(), calls)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, result := range results {
		if result.Error != "" {
			t.Fatalf("%s failed: %s", result.ID, result.Error)
		}
	}

	approver.mu.Lock()
	defer approver.mu.Unlock()
	if len(approver.calls) != 2 {
		t.Fatalf("got %d approval calls, want 2", len(approver.calls))
	}
	if approver.calls[0][3] != "first" || approver.calls[1][3] != "second" {
		t.Fatalf("approval order = %#v", approver.calls)
	}
}

func mustMarshalToolArgs(t *testing.T, command []string) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(map[string]interface{}{
		"command": command,
	})
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	return args
}
