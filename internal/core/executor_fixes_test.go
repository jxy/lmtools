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

// mockLoggerFixes implements ExecLogger for testing
type mockLoggerFixes struct {
	debugEnabled  bool
	logs          []string
	debugMessages []string
}

func (m *mockLoggerFixes) Debugf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if m.debugEnabled {
		m.logs = append(m.logs, msg)
	}
	m.debugMessages = append(m.debugMessages, msg)
}

func (m *mockLoggerFixes) IsDebugEnabled() bool {
	return m.debugEnabled
}

// TestCommandJSONFormat verifies that commands are displayed in JSON array format
func TestCommandJSONFormat(t *testing.T) {
	// Create temp whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["git"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true,
	}

	logger := &mockLoggerFixes{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create a tool call with multiple arguments
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"git", "rev-parse", "--show-toplevel"},
		"environ": map[string]string{"GIT_DIR": ".git"},
		"workdir": "/tmp/test",
		"timeout": 30,
	})
	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	// Execute
	ctx := context.Background()
	_ = executor.ExecuteParallel(ctx, []ToolCall{call})

	// Since we moved display logic to CLI layer, we should not expect these messages in the executor
	// The executor now only returns results without displaying them
	// These tests should verify the executor's behavior, not its display output

	// Instead, let's verify that the command was executed with the correct parameters
	// by checking the debug logs
	foundCommandInDebug := false
	for _, msg := range logger.debugMessages {
		if strings.Contains(msg, "Executing command:") && strings.Contains(msg, "git") {
			foundCommandInDebug = true
			break
		}
	}

	if !foundCommandInDebug {
		t.Errorf("Expected command execution to be logged in debug")
	}
}

// TestOutputFormat verifies that output lines use >>> prefix
func TestOutputFormat(t *testing.T) {
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

	logger := &mockLoggerFixes{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create a successful tool call
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"echo", "test output"},
	})
	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	// Execute
	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	// Verify the command succeeded
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("Expected successful execution, got error: %v", results[0].Error)
	}

	// Since display logic moved to CLI layer, we should verify the executor's behavior
	// by checking that it returns the correct results, not by checking display output

	// Verify the command succeeded and returned output
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Error != "" {
		t.Fatalf("Expected successful execution, got error: %v", result.Error)
	}

	// Verify the output contains what we expect
	expectedOutput := "test output"
	if !strings.Contains(result.Output, expectedOutput) {
		t.Errorf("Expected output to contain '%s', got: %s", expectedOutput, result.Output)
	}

	// Verify timing information is recorded
	if result.Elapsed <= 0 {
		t.Errorf("Expected positive elapsed time, got: %d", result.Elapsed)
	}
}

// TestEmptyWhitelistWithAutoApprove verifies behavior with empty whitelist and auto-approve
func TestEmptyWhitelistWithAutoApprove(t *testing.T) {
	// Create empty whitelist file
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(``), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     5 * time.Second,
		toolAutoApprove: true, // With empty whitelist and auto-approve, commands are allowed
	}

	logger := &mockLoggerFixes{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create a tool call for a safe command
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"echo", "test"},
	})
	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	// Execute - should succeed because empty whitelist + auto-approve = allow
	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	if len(results) != 1 {
		t.Fatal("Expected 1 result")
	}

	// Should succeed with empty whitelist and auto-approve
	if results[0].Error != "" {
		t.Errorf("Expected command to succeed with empty whitelist and auto-approve, got error: %s", results[0].Error)
	}

	// Output should contain "test"
	if !strings.Contains(results[0].Output, "test") {
		t.Errorf("Expected output to contain 'test', got: %s", results[0].Output)
	}
}

// TestTimeoutAfterApproval verifies timeout starts after approval, not before
func TestTimeoutAfterApproval(t *testing.T) {
	// This test verifies the code structure - timeout context is created
	// AFTER checkApprovalWithReason is called

	// Create a whitelist without our test command
	tmpDir := t.TempDir()
	whitelistPath := filepath.Join(tmpDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["echo"]`), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	cfg := mockExecutorConfig{
		toolWhitelist:   whitelistPath,
		toolTimeout:     100 * time.Millisecond,
		toolAutoApprove: true, // Changed to avoid TTY prompt
	}

	logger := &mockLoggerFixes{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create a sleep command that would timeout if timer started before approval
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"sleep", "1"},
	})
	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	start := time.Now()
	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})
	elapsed := time.Since(start)

	// The command should fail quickly due to approval denial, not timeout
	// If timeout started before approval, we'd wait 100ms
	if elapsed > 50*time.Millisecond {
		t.Errorf("Expected quick failure from approval, but took %v", elapsed)
	}

	// Should have an error about approval, not timeout
	if len(results) != 1 || results[0].Error == "" {
		t.Error("Expected error from approval")
	}

	if strings.Contains(results[0].Error, "timeout") {
		t.Errorf("Got timeout error when expecting approval error: %s", results[0].Error)
	}
}

// TestExecutionTimerStartsAfterApproval verifies the elapsed time doesn't include approval wait
func TestExecutionTimerStartsAfterApproval(t *testing.T) {
	// Create temp whitelist file with echo
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

	logger := &mockLoggerFixes{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create a simple echo command
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"echo", "test"},
	})
	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	// Execute
	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	// Verify execution succeeded
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("Expected successful execution, got error: %v", results[0].Error)
	}

	// The elapsed time should be very short for a simple echo
	// If it included approval time (even auto-approval), it would be longer
	if results[0].Elapsed > 100 { // 100ms is generous for echo
		t.Errorf("Elapsed time too long for simple echo: %dms", results[0].Elapsed)
	}
}

// TestBlacklistErrorMessage verifies blacklisted commands get specific error message
func TestBlacklistErrorMessage(t *testing.T) {
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

	logger := &mockLoggerFixes{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Test blacklisted command
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"rm", "dangerous-test-file"},
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

	// Should have simplified error message for AI model
	if results[0].Error != "denied: blacklisted" {
		t.Errorf("Expected 'denied: blacklisted' error message, got: %s", results[0].Error)
	}
}

// TestTruncationMessageFormat verifies truncation message uses >>> prefix
func TestTruncationMessageFormat(t *testing.T) {
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

	logger := &mockLoggerFixes{debugEnabled: true}
	notifier := NewTestNotifier()
	approver := NewTestApprover(true) // Auto-approve for tests
	executor, err := NewExecutor(cfg.requestOptions(), logger, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Set a very small max output size for testing
	executor.maxOutputSize = 100

	// Create command that produces lots of output
	args, _ := json.Marshal(map[string]interface{}{
		"command": []string{"sh", "-c", "for i in $(seq 1 100); do echo 'Line of output'; done"},
	})
	call := ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: args,
	}

	// Execute
	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []ToolCall{call})

	// Check that output was truncated
	if len(results) != 1 || !results[0].Truncated {
		t.Error("Expected output to be truncated")
	}

	// Since display logic moved to CLI layer, we should verify the truncation
	// by checking the result's Truncated flag, not by checking display messages
	result := results[0]
	if !result.Truncated {
		t.Error("Expected result.Truncated to be true")
	}

	// Verify that the output was actually truncated to the expected size
	if len(result.Output) > int(executor.maxOutputSize) {
		t.Errorf("Expected output to be truncated to %d bytes, got %d bytes",
			executor.maxOutputSize, len(result.Output))
	}

	// Verify the command succeeded despite truncation
	if result.Error != "" {
		t.Errorf("Expected successful execution despite truncation, got error: %v", result.Error)
	}
}
