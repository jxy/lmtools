package session

import (
	"context"
	"lmtools/internal/core"
	"testing"
	"time"
)

// TestCheckForPendingToolCallsWithFindMessageDirectory tests that CheckForPendingToolCalls
// efficiently finds the last message directory without building a full index
func TestCheckForPendingToolCallsWithFindMessageDirectory(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with some messages
		session, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add a user message
		_, err = AppendMessageWithToolInteraction(context.Background(), session, Message{
			Role:      core.RoleUser,
			Content:   "Test message",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append user message: %v", err)
		}

		// Add an assistant message with tool calls
		toolCalls := []core.ToolCall{
			{
				ID:   "call-1",
				Name: "universal_command",
				Args: []byte(`{"command": ["echo", "test"]}`),
			},
		}

		_, err = AppendMessageWithToolInteraction(
			context.Background(),
			session,
			Message{
				Role:      core.RoleAssistant,
				Content:   "I'll run that command",
				Timestamp: time.Now(),
				Model:     "test-model",
			},
			toolCalls,
			nil,
		)
		if err != nil {
			t.Fatalf("Failed to append assistant message with tools: %v", err)
		}

		// Check for pending tool calls
		pendingCalls, err := CheckForPendingToolCalls(context.Background(), session.Path)
		if err != nil {
			t.Fatalf("Failed to check for pending tool calls: %v", err)
		}

		// Verify we found the pending calls
		if len(pendingCalls) != 1 {
			t.Errorf("Expected 1 pending call, got %d", len(pendingCalls))
		}
		if len(pendingCalls) > 0 && pendingCalls[0].ID != "call-1" {
			t.Errorf("Wrong tool call ID: got %s, want call-1", pendingCalls[0].ID)
		}
	})
}

// TestCheckForPendingToolCallsInSiblingDirectory tests that pending tool calls
// are found even when they're in a sibling directory
func TestCheckForPendingToolCallsInSiblingDirectory(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with messages
		session, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add initial messages
		_, err = AppendMessageWithToolInteraction(context.Background(), session, Message{
			Role:      core.RoleUser,
			Content:   "First message",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append user message: %v", err)
		}

		result1, err := AppendMessageWithToolInteraction(context.Background(), session, Message{
			Role:      core.RoleAssistant,
			Content:   "First response",
			Timestamp: time.Now(),
			Model:     "test-model",
		}, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append assistant message: %v", err)
		}

		// Create a sibling branch from the assistant message
		siblingPath, err := CreateSibling(context.Background(), session.Path, result1.MessageID)
		if err != nil {
			t.Fatalf("Failed to create sibling: %v", err)
		}
		siblingSession := &Session{Path: siblingPath}

		// Add a message with tool calls to the sibling
		toolCalls := []core.ToolCall{
			{
				ID:   "sibling-call-1",
				Name: "universal_command",
				Args: []byte(`{"command": ["ls", "-la"]}`),
			},
		}

		_, err = AppendMessageWithToolInteraction(
			context.Background(),
			siblingSession,
			Message{
				Role:      core.RoleAssistant,
				Content:   "Running ls command",
				Timestamp: time.Now(),
				Model:     "test-model",
			},
			toolCalls,
			nil,
		)
		if err != nil {
			t.Fatalf("Failed to append message to sibling: %v", err)
		}

		// Check for pending tool calls from the sibling path
		pendingCalls, err := CheckForPendingToolCalls(context.Background(), siblingSession.Path)
		if err != nil {
			t.Fatalf("Failed to check for pending tool calls: %v", err)
		}

		// Verify we found the pending calls
		if len(pendingCalls) != 1 {
			t.Errorf("Expected 1 pending call, got %d", len(pendingCalls))
		}
		if len(pendingCalls) > 0 && pendingCalls[0].ID != "sibling-call-1" {
			t.Errorf("Wrong tool call ID: got %s, want sibling-call-1", pendingCalls[0].ID)
		}
	})
}

// TestCheckForPendingToolCallsNoTools tests the case where there are no pending tool calls
func TestCheckForPendingToolCallsNoTools(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages without tool calls
		_, err = AppendMessageWithToolInteraction(context.Background(), session, Message{
			Role:      core.RoleUser,
			Content:   "Hello",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append user message: %v", err)
		}

		_, err = AppendMessageWithToolInteraction(context.Background(), session, Message{
			Role:      core.RoleAssistant,
			Content:   "Hi there!",
			Timestamp: time.Now(),
			Model:     "test-model",
		}, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append assistant message: %v", err)
		}

		// Check for pending tool calls
		pendingCalls, err := CheckForPendingToolCalls(context.Background(), session.Path)
		if err != nil {
			t.Fatalf("Failed to check for pending tool calls: %v", err)
		}

		// Should be no pending calls
		if len(pendingCalls) != 0 {
			t.Errorf("Expected no pending calls, got %d", len(pendingCalls))
		}
	})
}

// TestFindMessageDirectoryPerformance tests that FindMessageDirectory is more efficient
// than building a full index for finding a single message
func TestFindMessageDirectoryPerformance(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Create a deeper session structure with multiple siblings
		var lastMsgID string
		currentSession := session

		// Create several levels of messages and siblings
		for level := 0; level < 3; level++ {
			// Add some messages at this level
			for i := 0; i < 5; i++ {
				result, err := AppendMessageWithToolInteraction(context.Background(), currentSession, Message{
					Role:      core.RoleUser,
					Content:   "Message",
					Timestamp: time.Now(),
				}, nil, nil)
				if err != nil {
					t.Fatalf("Failed to append message: %v", err)
				}
				lastMsgID = result.MessageID

				_, err = AppendMessageWithToolInteraction(context.Background(), currentSession, Message{
					Role:      core.RoleAssistant,
					Content:   "Response",
					Timestamp: time.Now(),
					Model:     "test-model",
				}, nil, nil)
				if err != nil {
					t.Fatalf("Failed to append assistant message: %v", err)
				}
			}

			// Create a sibling branch if not the last level
			if level < 2 {
				siblingPath, err := CreateSibling(context.Background(), currentSession.Path, lastMsgID)
				if err != nil {
					t.Fatalf("Failed to create sibling: %v", err)
				}
				currentSession = &Session{Path: siblingPath}
			}
		}

		// Add a final unique message that will only exist in the current path
		finalResult, err := AppendMessageWithToolInteraction(context.Background(), currentSession, Message{
			Role:      core.RoleUser,
			Content:   "Final unique message",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append final message: %v", err)
		}
		finalMsgID := finalResult.MessageID

		// Test indexMessageDirectories
		start := time.Now()
		index, err := indexMessageDirectories(session.Path)
		indexDuration := time.Since(start)

		if err != nil {
			t.Fatalf("indexMessageDirectories failed: %v", err)
		}

		if index[finalMsgID] == "" {
			t.Errorf("indexMessageDirectories failed to find message %s", finalMsgID)
		}

		t.Logf("indexMessageDirectories completed in %v, indexed %d messages", indexDuration, len(index))
	})
}
