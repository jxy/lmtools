package core

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestApproverContextCancellation tests that approval prompts respect context cancellation
func TestApproverContextCancellation(t *testing.T) {
	// Create a test approver that blocks until context is cancelled
	readyChan := make(chan struct{})
	approver := &blockingApprover{
		blockChan: make(chan struct{}),
		readyChan: readyChan,
	}

	// Create an executor with the blocking approver
	cfg := NewTestRequestConfig()
	log := NewTestLogger(true)
	notifier := NewTestNotifier()

	executor, err := NewExecutor(cfg, log, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create a context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Start the execution in a goroutine
	resultChan := make(chan ToolResult)
	go func() {
		result := executor.executeSingle(ctx, ToolCall{
			ID:   "test-1",
			Name: "universal_command",
			Args: []byte(`{"command": ["echo", "test"]}`),
		})
		resultChan <- result
	}()

	// Wait for the approval prompt to be reached
	select {
	case <-readyChan:
		// Approval prompt reached
	case <-time.After(time.Second):
		t.Fatal("Approval prompt not reached within timeout")
	}

	// Cancel the context
	cancel()

	// Wait for the result
	select {
	case result := <-resultChan:
		// Should have failed due to context cancellation
		if result.Error == "" {
			t.Error("Expected error due to context cancellation")
		}
		if result.Code != "CANCELLED" {
			t.Errorf("Expected CANCELLED code, got %s", result.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execution did not complete after context cancellation")
	}

	// Signal the approver to stop blocking (cleanup)
	close(approver.blockChan)
}

// TestApproverContextAlreadyCancelled tests behavior when context is already cancelled
func TestApproverContextAlreadyCancelled(t *testing.T) {
	approver := NewTestApprover(true) // Would approve if asked

	cfg := NewTestRequestConfig()
	log := NewTestLogger(true)
	notifier := NewTestNotifier()

	executor, err := NewExecutor(cfg, log, notifier, approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	// Create an already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Execute with cancelled context
	result := executor.executeSingle(ctx, ToolCall{
		ID:   "test-1",
		Name: "universal_command",
		Args: []byte(`{"command": ["echo", "test"]}`),
	})

	// Should fail immediately
	if result.Error == "" {
		t.Error("Expected error due to cancelled context")
	}
	if result.Code != "CANCELLED" {
		t.Errorf("Expected CANCELLED code, got %s", result.Code)
	}

	// Verify approver was never called
	if len(approver.ApprovalCalls) > 0 {
		t.Error("Approver should not have been called with cancelled context")
	}
}

// blockingApprover is a test approver that blocks until signaled
type blockingApprover struct {
	blockChan chan struct{}
	readyChan chan struct{}
	mu        sync.Mutex
}

func (a *blockingApprover) Approve(ctx context.Context, command []string) (bool, error) {
	// Signal that we've reached the approval prompt
	a.mu.Lock()
	if a.readyChan != nil {
		close(a.readyChan)
		a.readyChan = nil // Prevent double close
	}
	a.mu.Unlock()

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-a.blockChan:
		return true, nil
	}
}
