package session

import (
	"context"
	"lmtools/internal/core"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestBuildMessagesWithIndexMatchesLineageBuilder(t *testing.T) {
	WithTestManager(t, func(manager *Manager, sessionsDir string) {
		ctx := context.Background()
		_ = sessionsDir
		sess, err := manager.CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("CreateSession() error = %v", err)
		}
		if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{
			Role:      core.RoleUser,
			Content:   "list files",
			Timestamp: time.Now(),
		}, nil, nil); err != nil {
			t.Fatalf("append user: %v", err)
		}
		if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{
			Role:      core.RoleAssistant,
			Content:   "I'll inspect the directory.",
			Timestamp: time.Now(),
			Model:     "test-model",
		}, []core.ToolCall{{
			ID:   "call_1",
			Name: "universal_command",
			Args: []byte(`{"command":["ls"]}`),
		}}, nil); err != nil {
			t.Fatalf("append assistant: %v", err)
		}

		lineage, err := GetLineageWithManager(manager, sess.Path)
		if err != nil {
			t.Fatalf("GetLineage() error = %v", err)
		}
		index, err := indexMessagesAlongPathWithManager(manager, sess.Path)
		if err != nil {
			t.Fatalf("indexMessagesAlongPath() error = %v", err)
		}
		withIndex, err := BuildMessagesWithIndex(ctx, lineage, index, sess.Path)
		if err != nil {
			t.Fatalf("BuildMessagesWithIndex() error = %v", err)
		}
		fromLineage, err := BuildMessagesWithToolInteractionsWithManager(ctx, manager, sess.Path)
		if err != nil {
			t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
		}
		if !reflect.DeepEqual(withIndex, fromLineage) {
			t.Fatalf("BuildMessagesWithIndex() = %#v, want %#v", withIndex, fromLineage)
		}
	})
}

func TestBuildMessagesWithoutStoredBlocksUsesCanonicalProjection(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	call := core.ToolCall{
		ID:           "legacy-call",
		Type:         "custom",
		Namespace:    "shell",
		OriginalName: "command",
		Name:         "shell_command",
		Input:        "echo legacy",
	}
	assistant, err := SaveAssistantResponseWithTools(ctx, sess, "running", []core.ToolCall{call}, "test-model")
	if err != nil {
		t.Fatalf("save assistant: %v", err)
	}
	result, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: call.ID, Output: "legacy"}}, "output was truncated")
	if err != nil {
		t.Fatalf("save tool result: %v", err)
	}
	for _, saved := range []SaveResult{assistant, result} {
		if err := os.Remove(buildMessageFilePaths(saved.Path, saved.MessageID).BlocksPath); err != nil {
			t.Fatalf("remove stored blocks for %s: %v", saved.MessageID, err)
		}
	}

	messages, err := BuildMessagesWithToolInteractionsWithManager(ctx, manager, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractionsWithManager() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	toolUse, ok := messages[0].Blocks[1].(core.ToolUseBlock)
	if !ok {
		t.Fatalf("assistant tool block = %T, want ToolUseBlock", messages[0].Blocks[1])
	}
	if toolUse.ID != call.ID || toolUse.Type != call.Type || toolUse.Namespace != call.Namespace ||
		toolUse.OriginalName != call.OriginalName || toolUse.Name != call.Name || toolUse.InputString != call.Input {
		t.Fatalf("assistant tool block = %#v, want metadata from %#v", toolUse, call)
	}
	toolResult, ok := messages[1].Blocks[0].(core.ToolResultBlock)
	if !ok || toolResult.ToolUseID != call.ID || toolResult.Name != call.Name {
		t.Fatalf("first result block = %#v, want named result for %s", messages[1].Blocks[0], call.ID)
	}
	note, ok := messages[1].Blocks[1].(core.TextBlock)
	if !ok || note.Text != "output was truncated" {
		t.Fatalf("second result block = %#v, want trailing truncation note", messages[1].Blocks[1])
	}
}

func TestCachedMessageBuilderSeesSamePathToolAppends(t *testing.T) {
	WithTestSessionDir(t, func(_ string) {
		ctx := context.Background()
		sess, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("CreateSession() error = %v", err)
		}
		if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{
			Role:      core.RoleUser,
			Content:   "run the tools",
			Timestamp: time.Now(),
		}, nil, nil); err != nil {
			t.Fatalf("append user: %v", err)
		}

		builder, err := CreateCachedMessageBuilder(ctx, sess.Path)
		if err != nil {
			t.Fatalf("CreateCachedMessageBuilder() error = %v", err)
		}

		firstCall := core.ToolCall{ID: "call-1", Name: "universal_command", Args: []byte(`{"command":["echo","one"]}`)}
		if _, err := SaveAssistantResponseWithTools(ctx, sess, "first", []core.ToolCall{firstCall}, "test-model"); err != nil {
			t.Fatalf("save first assistant round: %v", err)
		}
		if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: firstCall.ID, Output: "one"}}, "tool output was truncated"); err != nil {
			t.Fatalf("save first tool results: %v", err)
		}

		firstBuild, err := builder(sess.Path)
		if err != nil {
			t.Fatalf("first cached build: %v", err)
		}
		if len(firstBuild) != 3 {
			t.Fatalf("first build message count = %d, want 3", len(firstBuild))
		}
		if firstBuild[0].Role != string(core.RoleUser) || firstBuild[1].Role != string(core.RoleAssistant) || firstBuild[2].Role != string(core.RoleUser) {
			t.Fatalf("first build roles = [%s %s %s]", firstBuild[0].Role, firstBuild[1].Role, firstBuild[2].Role)
		}
		toolUse, ok := firstBuild[1].Blocks[1].(core.ToolUseBlock)
		if !ok || toolUse.ID != firstCall.ID {
			t.Fatalf("first assistant tool block = %#v", firstBuild[1].Blocks[1])
		}
		toolResult, ok := firstBuild[2].Blocks[0].(core.ToolResultBlock)
		if !ok || toolResult.ToolUseID != firstCall.ID || toolResult.Name != firstCall.Name {
			t.Fatalf("first tool result block = %#v", firstBuild[2].Blocks[0])
		}
		if note, ok := firstBuild[2].Blocks[1].(core.TextBlock); !ok || note.Text != "tool output was truncated" {
			t.Fatalf("truncation note = %#v, want final text block", firstBuild[2].Blocks[1])
		}

		secondCall := core.ToolCall{ID: "call-2", Name: "universal_command", Args: []byte(`{"command":["echo","two"]}`)}
		if _, err := SaveAssistantResponseWithTools(ctx, sess, "one more", []core.ToolCall{secondCall}, "test-model"); err != nil {
			t.Fatalf("save second assistant round: %v", err)
		}
		if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: secondCall.ID, Output: "two"}}, ""); err != nil {
			t.Fatalf("save second tool results: %v", err)
		}

		secondBuild, err := builder(sess.Path)
		if err != nil {
			t.Fatalf("second cached build: %v", err)
		}
		if len(secondBuild) != 5 {
			t.Fatalf("second build message count = %d, want 5", len(secondBuild))
		}
		secondUse, ok := secondBuild[3].Blocks[1].(core.ToolUseBlock)
		if !ok || secondUse.ID != secondCall.ID {
			t.Fatalf("second assistant tool block = %#v", secondBuild[3].Blocks[1])
		}
		secondResult, ok := secondBuild[4].Blocks[0].(core.ToolResultBlock)
		if !ok || secondResult.ToolUseID != secondCall.ID || secondResult.Content != "two" {
			t.Fatalf("second tool result block = %#v", secondBuild[4].Blocks[0])
		}

		repeatedBuild, err := builder(sess.Path)
		if err != nil {
			t.Fatalf("repeated cached build: %v", err)
		}
		if !reflect.DeepEqual(repeatedBuild, secondBuild) {
			t.Fatalf("repeated build introduced a change\ngot:  %#v\nwant: %#v", repeatedBuild, secondBuild)
		}
	})
}

func TestCachedMessageBuilderRebuildsForSiblingPath(t *testing.T) {
	WithTestSessionDir(t, func(_ string) {
		ctx := context.Background()
		sess, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("CreateSession() error = %v", err)
		}
		if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{
			Role:      core.RoleUser,
			Content:   "choose a tool",
			Timestamp: time.Now(),
		}, nil, nil); err != nil {
			t.Fatalf("append user: %v", err)
		}
		rootResponse, err := SaveAssistantResponseWithTools(ctx, sess, "root choice", []core.ToolCall{{
			ID: "root-call", Name: "universal_command", Args: []byte(`{"command":["echo","root"]}`),
		}}, "test-model")
		if err != nil {
			t.Fatalf("save root assistant: %v", err)
		}

		builder, err := CreateCachedMessageBuilder(ctx, sess.Path)
		if err != nil {
			t.Fatalf("CreateCachedMessageBuilder() error = %v", err)
		}
		siblingPath, err := CreateSibling(ctx, sess.Path, rootResponse.MessageID)
		if err != nil {
			t.Fatalf("CreateSibling() error = %v", err)
		}
		sibling := &Session{Path: siblingPath}
		if _, err := SaveAssistantResponseWithTools(ctx, sibling, "branch choice", []core.ToolCall{{
			ID: "branch-call", Name: "universal_command", Args: []byte(`{"command":["echo","branch"]}`),
		}}, "test-model"); err != nil {
			t.Fatalf("save branch assistant: %v", err)
		}
		if _, err := SaveToolResults(ctx, sibling, []core.ToolResult{{ID: "branch-call", Output: "branch"}}, ""); err != nil {
			t.Fatalf("save branch result: %v", err)
		}

		messages, err := builder(sibling.Path)
		if err != nil {
			t.Fatalf("build sibling messages: %v", err)
		}
		if len(messages) != 3 {
			t.Fatalf("sibling message count = %d, want 3", len(messages))
		}
		branchUse, ok := messages[1].Blocks[1].(core.ToolUseBlock)
		if !ok || branchUse.ID != "branch-call" {
			t.Fatalf("branch tool use = %#v", messages[1].Blocks[1])
		}
		branchResult, ok := messages[2].Blocks[0].(core.ToolResultBlock)
		if !ok || branchResult.ToolUseID != "branch-call" || branchResult.Content != "branch" {
			t.Fatalf("branch tool result = %#v", messages[2].Blocks[0])
		}
	})
}

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
