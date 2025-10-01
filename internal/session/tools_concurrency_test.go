package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"sync"
	"testing"
)

// TestConcurrentToolMessageWrites tests that concurrent writes with tool calls work correctly
func TestConcurrentToolMessageWrites(t *testing.T) {
	// Use a temporary directory for testing
	tmpDir := t.TempDir()
	SetSessionsDir(tmpDir)

	// Create a session with a user message
	sess, err := CreateSession("test user message", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Test concurrent writes
	numGoroutines := 10
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Create tool calls for this goroutine
			toolCalls := []core.ToolCall{
				{
					ID:   fmt.Sprintf("tool_%d", idx),
					Name: "test_tool",
					Args: json.RawMessage(fmt.Sprintf(`{"index": %d}`, idx)),
				},
			}

			// Save assistant response with tools
			_, err := SaveAssistantResponseWithTools(context.Background(),
				sess,
				fmt.Sprintf("Response %d", idx),
				toolCalls,
				"test-model",
			)
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// Verify all writes succeeded
	for i, err := range errors {
		if err != nil {
			t.Errorf("Concurrent write %d failed: %v", i, err)
		}
	}

	// Verify all messages were saved (plus the initial user message)
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// We expect numGoroutines assistant messages + 1 initial user message
	expectedTotal := numGoroutines + 1
	if len(messages) != expectedTotal {
		t.Errorf("Expected %d messages, got %d", expectedTotal, len(messages))
	}

	// Verify each assistant message has corresponding tool file
	assistantCount := 0
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue // Skip the initial user message
		}
		assistantCount++

		// Check for tool file
		toolInteraction, err := LoadToolInteraction(sess.Path, msg.ID)
		if err != nil {
			t.Errorf("Failed to load tool interaction for message %s: %v", msg.ID, err)
		}

		if toolInteraction == nil {
			t.Errorf("No tool interaction found for message %s", msg.ID)
		} else if len(toolInteraction.Calls) != 1 {
			t.Errorf("Expected 1 tool call for message %s, got %d", msg.ID, len(toolInteraction.Calls))
		}
	}

	// Verify we have the right number of assistant messages
	if assistantCount != numGoroutines {
		t.Errorf("Expected %d assistant messages, got %d", numGoroutines, assistantCount)
	}
}

// TestConcurrentToolResults tests concurrent writes of tool results
func TestConcurrentToolResults(t *testing.T) {
	// Use a temporary directory for testing
	tmpDir := t.TempDir()
	SetSessionsDir(tmpDir)

	// Create a session with a user message
	sess, err := CreateSession("test user message", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Test concurrent writes of tool results
	numGoroutines := 10
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Create tool results for this goroutine
			results := []core.ToolResult{
				{
					ID:      fmt.Sprintf("tool_%d", idx),
					Output:  fmt.Sprintf("Output from tool %d", idx),
					Elapsed: int64(100 + idx),
				},
			}

			// Save tool results
			_, err := SaveToolResults(context.Background(),
				sess,
				results,
				fmt.Sprintf("Additional text for result %d", idx),
			)
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// Verify all writes succeeded
	for i, err := range errors {
		if err != nil {
			t.Errorf("Concurrent write of tool results %d failed: %v", i, err)
		}
	}

	// Verify all messages were saved (plus the initial user message)
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// We expect numGoroutines user messages (with tool results) + 1 initial user message
	expectedTotal := numGoroutines + 1
	if len(messages) != expectedTotal {
		t.Errorf("Expected %d messages, got %d", expectedTotal, len(messages))
	}

	// Verify each user message (except the first) has corresponding tool results file
	resultCount := 0
	for i, msg := range messages {
		if i == 0 {
			continue // Skip the initial user message
		}
		resultCount++

		// Check for tool file
		toolInteraction, err := LoadToolInteraction(sess.Path, msg.ID)
		if err != nil {
			t.Errorf("Failed to load tool interaction for message %s: %v", msg.ID, err)
		}

		if toolInteraction == nil {
			t.Errorf("No tool interaction found for message %s", msg.ID)
		} else if len(toolInteraction.Results) != 1 {
			t.Errorf("Expected 1 tool result for message %s, got %d", msg.ID, len(toolInteraction.Results))
		}
	}

	// Verify we have the right number of tool result messages
	if resultCount != numGoroutines {
		t.Errorf("Expected %d tool result messages, got %d", numGoroutines, resultCount)
	}
}

// TestMixedConcurrentToolOperations tests mixed concurrent operations with tool calls and results
func TestMixedConcurrentToolOperations(t *testing.T) {
	// Use a temporary directory for testing
	tmpDir := t.TempDir()
	SetSessionsDir(tmpDir)

	// Create a session with a user message
	sess, err := CreateSession("test user message", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Test mixed concurrent operations
	numGoroutines := 20
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			if idx%2 == 0 {
				// Even indices: save tool calls
				toolCalls := []core.ToolCall{
					{
						ID:   fmt.Sprintf("call_%d", idx),
						Name: "test_tool",
						Args: json.RawMessage(fmt.Sprintf(`{"index": %d}`, idx)),
					},
				}

				_, err := SaveAssistantResponseWithTools(context.Background(),
					sess,
					fmt.Sprintf("Assistant response %d", idx),
					toolCalls,
					"test-model",
				)
				errors[idx] = err
			} else {
				// Odd indices: save tool results
				results := []core.ToolResult{
					{
						ID:     fmt.Sprintf("result_%d", idx),
						Output: fmt.Sprintf("Result output %d", idx),
					},
				}

				_, err := SaveToolResults(context.Background(),
					sess,
					results,
					fmt.Sprintf("User context %d", idx),
				)
				errors[idx] = err
			}
		}(i)
	}

	wg.Wait()

	// Verify all writes succeeded
	for i, err := range errors {
		if err != nil {
			t.Errorf("Concurrent mixed operation %d failed: %v", i, err)
		}
	}

	// Verify all messages were saved (plus the initial user message)
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// We expect numGoroutines messages + 1 initial user message
	expectedTotal := numGoroutines + 1
	if len(messages) != expectedTotal {
		t.Errorf("Expected %d messages, got %d", expectedTotal, len(messages))
	}

	// Count assistant and user messages (excluding the initial user message)
	assistantCount := 0
	userCount := 0
	for i, msg := range messages {
		if i == 0 {
			continue // Skip the initial user message
		}
		switch msg.Role {
		case "assistant":
			assistantCount++
		case "user":
			userCount++
		}
	}

	expectedAssistant := numGoroutines / 2
	expectedUser := numGoroutines / 2
	if assistantCount != expectedAssistant {
		t.Errorf("Expected %d assistant messages, got %d", expectedAssistant, assistantCount)
	}
	if userCount != expectedUser {
		t.Errorf("Expected %d user messages, got %d", expectedUser, userCount)
	}
}
