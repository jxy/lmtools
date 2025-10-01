package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestForkOnlyWhenSystemDiffers verifies that forking only happens when the effective system prompt changes
func TestForkOnlyWhenSystemDiffers(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Test 1: Same system message should not fork
		t.Run("SameSystemNoFork", func(t *testing.T) {
			originalSystem := "You are a helpful assistant."
			sess, err := CreateSession(originalSystem, core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}

			// Add a message
			msg := Message{Role: "user", Content: "Hello", Timestamp: time.Now()}
			if _, err := AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}

			// Try to fork with the same system message
			forked, didFork, err := MaybeForkForSystem(context.Background(), sess, originalSystem)
			if err != nil {
				t.Fatalf("Failed to maybe fork: %v", err)
			}

			if didFork {
				t.Error("Should not have forked when system message is the same")
			}
			if forked.Path != sess.Path {
				t.Errorf("Expected same path %s, got %s", sess.Path, forked.Path)
			}
		})

		// Test 2: Different system message should fork
		t.Run("DifferentSystemForks", func(t *testing.T) {
			originalSystem := "You are a helpful assistant."
			sess, err := CreateSession(originalSystem, core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}

			// Add a message
			msg := Message{Role: "user", Content: "Hello", Timestamp: time.Now()}
			if _, err := AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}

			// Fork with different system message
			newSystem := "You are an expert programmer."
			forked, didFork, err := MaybeForkForSystem(context.Background(), sess, newSystem)
			if err != nil {
				t.Fatalf("Failed to maybe fork: %v", err)
			}

			if !didFork {
				t.Error("Should have forked when system message is different")
			}
			if forked.Path == sess.Path {
				t.Error("Forked session should have different path")
			}

			// Verify new system message
			savedSystem, err := GetSystemMessage(forked.Path)
			if err != nil {
				t.Fatalf("Failed to get system message: %v", err)
			}
			if savedSystem == nil || *savedSystem != newSystem {
				t.Errorf("Expected system message %q, got %v", newSystem, savedSystem)
			}
		})

		// Test 3: Tool prompt changes effective system
		t.Run("ToolPromptChangesEffectiveSystem", func(t *testing.T) {
			originalSystem := "You are a helpful assistant."
			sess, err := CreateSession(originalSystem, core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}

			// Add a message
			msg := Message{Role: "user", Content: "Hello", Timestamp: time.Now()}
			if _, err := AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}

			// Fork with same base system but with tools enabled (changes effective system)
			// Append tool prompt to simulate tools being enabled
			effectiveSystem := originalSystem + "\n\n" + prompts.ToolSystemPrompt
			forked, didFork, err := MaybeForkForSystem(context.Background(), sess, effectiveSystem)
			if err != nil {
				t.Fatalf("Failed to maybe fork: %v", err)
			}

			if !didFork {
				t.Error("Should have forked when tool prompt is added to system")
			}
			if forked.Path == sess.Path {
				t.Error("Forked session should have different path")
			}

			// Verify effective system includes tool prompt
			savedSystem, err := GetSystemMessage(forked.Path)
			if err != nil {
				t.Fatalf("Failed to get system message: %v", err)
			}
			if savedSystem == nil || *savedSystem != effectiveSystem {
				// The forked session should have the effective system with tool prompt
				t.Errorf("Expected effective system message %q, got %v", effectiveSystem, savedSystem)
			}
		})
	})
}

// TestSessionAtomicityInvariant verifies the ".json is commit point" invariant
func TestSessionAtomicityInvariant(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Test 1: JSON file creation is atomic
		t.Run("JSONFileIsCommitPoint", func(t *testing.T) {
			sess, err := CreateSession("Test system", core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}

			// Add a message and get the result
			msg := Message{
				Role:      "user",
				Content:   "Test message",
				Timestamp: time.Now(),
			}
			result, err := AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil)
			if err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}

			// Verify JSON file exists
			jsonPath := filepath.Join(result.Path, result.MessageID+".json")
			if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
				t.Error("JSON file should exist after successful append")
			}

			// Verify content file exists
			contentPath := filepath.Join(result.Path, result.MessageID+".txt")
			if _, err := os.Stat(contentPath); os.IsNotExist(err) {
				t.Error("Content file should exist after successful append")
			}

			// Test reading back
			messages, err := GetLineage(result.Path)
			if err != nil {
				t.Fatalf("Failed to read messages: %v", err)
			}
			// Should have system message + user message
			if len(messages) != 2 {
				t.Errorf("Expected 2 messages (system + user), got %d", len(messages))
			}
			// Verify the user message
			if len(messages) >= 2 && messages[1].Content != "Test message" {
				t.Errorf("Expected user message content 'Test message', got %q", messages[1].Content)
			}
		})

		// Test 2: Tool interactions respect atomicity
		t.Run("ToolInteractionAtomicity", func(t *testing.T) {
			sess, err := CreateSession("Test system", core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}

			// Add assistant message with tool calls
			toolCalls := []core.ToolCall{
				{ID: "test_call", Name: "test_tool", Args: []byte(`{"test": "data"}`)},
			}
			result, err := SaveAssistantResponseWithTools(context.Background(), sess, "Testing tools", toolCalls, "test-model")
			if err != nil {
				t.Fatalf("Failed to save assistant with tools: %v", err)
			}

			// Verify both JSON and tools file exist
			jsonPath := filepath.Join(result.Path, result.MessageID+".json")
			toolsPath := filepath.Join(result.Path, result.MessageID+".tools.json")

			if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
				t.Error("JSON file should exist for assistant message")
			}
			if _, err := os.Stat(toolsPath); os.IsNotExist(err) {
				t.Error("Tools file should exist for message with tool calls")
			}

			// Verify reading with tool interactions works
			typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), result.Path)
			if err != nil {
				t.Fatalf("Failed to build messages with tools: %v", err)
			}

			// Should have assistant message with tool use block
			found := false
			for _, msg := range typedMessages {
				if msg.Role == "assistant" {
					for _, block := range msg.Blocks {
						if _, ok := block.(core.ToolUseBlock); ok {
							found = true
							break
						}
					}
				}
			}
			if !found {
				t.Error("Tool use block not found in rebuilt messages")
			}
		})
	})
}

// TestDeleteNodeRespectsAtomicity verifies that DeleteNode maintains the atomicity invariant
func TestDeleteNodeRespectsAtomicity(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with multiple messages
		sess, err := CreateSession("Test system", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add several messages
		messages := []Message{
			{Role: "user", Content: "Message 1", Timestamp: time.Now()},
			{Role: "assistant", Content: "Response 1", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "Message 2", Timestamp: time.Now()},
			{Role: "assistant", Content: "Response 2", Timestamp: time.Now(), Model: "test-model"},
		}

		for _, msg := range messages {
			result, err := AppendMessageWithToolInteraction(context.Background(), sess, msg, nil, nil)
			if err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
			sess.Path = result.Path // Update session path to follow the conversation
		}

		// Get the current session path which should have all messages
		currentPath := sess.Path

		// Verify files exist before deletion
		entries, err := os.ReadDir(currentPath)
		if err != nil {
			t.Fatalf("Failed to read directory: %v", err)
		}

		fileCount := 0
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
				fileCount++
			}
		}
		// Should have system message + 4 messages = 5 JSON files
		if fileCount < 5 {
			t.Errorf("Expected at least 5 JSON files before deletion, found %d", fileCount)
		}

		// Delete the current node (which has all messages)
		err = DeleteNode(currentPath)
		if err != nil {
			t.Fatalf("Failed to delete node: %v", err)
		}

		// Verify the directory and all files are gone
		if _, err := os.Stat(currentPath); !os.IsNotExist(err) {
			t.Error("Directory should not exist after deletion")
		}

		// Verify parent still exists (the root session directory)
		parentPath := filepath.Dir(currentPath)
		if _, err := os.Stat(parentPath); os.IsNotExist(err) {
			t.Error("Parent directory should still exist after deletion")
		}
	})
}
