package core

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
	"slices"
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

func (m mockExecutorConfig) requestOptions() RequestOptions {
	return RequestOptions{
		User:               "testuser",
		Model:              "test-model",
		System:             "test system",
		EffectiveSystem:    "test system",
		Provider:           "test",
		ToolEnabled:        m.enableTool,
		ToolTimeout:        m.toolTimeout,
		ToolWhitelist:      m.toolWhitelist,
		ToolBlacklist:      m.toolBlacklist,
		ToolAutoApprove:    m.toolAutoApprove,
		ToolNonInteractive: true,
		MaxToolRounds:      DefaultMaxToolRounds,
		MaxToolParallel:    DefaultMaxToolParallel,
		ToolMaxOutputBytes: int(DefaultMaxOutputSize),
	}
}

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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)
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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, calls, nil)
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
	// Create temp whitelist file. environ is authority, so the grant names the
	// environment the call supplies; a bare ["sh"] rule does not cover it.
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	rule := `{"command":["sh"],"environ":{"TEST_VAR":"custom_value"}}`
	if err := os.WriteFile(whitelistPath, []byte(rule), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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
	// Create temp whitelist file. A grant names the whole environment it
	// authorizes, so all three variables appear in the rule.
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	rule := `{"command":["sh"],"environ":{"VAR0":"first","VAR1":"second","VAR2":"third"}}`
	if err := os.WriteFile(whitelistPath, []byte(rule), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLogger{debugEnabled: true}
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
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
			results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	call := ToolCall{
		ID:   "test-1",
		Name: "unsupported_tool",
		Args: json.RawMessage(`{}`),
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call}, nil)

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

type gateApprover struct {
	DeclineToolRoundLimitReset
	gate  string
	calls []string
	mu    sync.Mutex
}

func (a *gateApprover) Approve(ctx context.Context, args UniversalCommandArgs) (bool, error) {
	a.mu.Lock()
	call := args.Command[3]
	a.calls = append(a.calls, call)
	a.mu.Unlock()

	if call == "second" {
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
	opts := cfg.requestOptions()
	opts.ToolNonInteractive = false
	executor, err := NewExecutor(opts, logger, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	calls := []ToolCall{
		{
			ID:   "call_first",
			Name: "universal_command",
			Args: marshalCommandArgs(t, UniversalCommandArgs{
				Command: []string{"sh", "-c", `test -f "$1"`, "first", gate},
			}),
		},
		{
			ID:   "call_second",
			Name: "universal_command",
			Args: marshalCommandArgs(t, UniversalCommandArgs{
				Command: []string{"sh", "-c", `test -f "$1"`, "second", gate},
			}),
		},
	}

	results := executor.ExecuteParallel(context.Background(), calls, TestToolUI{})
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
	if !slices.Equal(approver.calls, []string{"first", "second"}) {
		t.Fatalf("approval calls = %v, want first then second", approver.calls)
	}
}
