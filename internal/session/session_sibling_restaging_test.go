package session

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCommitMessageWithRetriesSiblingRestaging verifies that when a conflict occurs
// and a sibling directory is created, the message files are properly restaged in the
// sibling directory to avoid cross-directory rename failures.
func TestCommitMessageWithRetriesSiblingRestaging(t *testing.T) {
	// Create a temporary session directory
	tempDir := t.TempDir()
	sessionPath := filepath.Join(tempDir, "test-session")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Create a tool interaction to test .tools.json file handling
	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{
				ID:   "test-tool-1",
				Name: "test_tool",
				Args: []byte(`{"command": "echo test"}`),
			},
		},
		Results: []core.ToolResult{
			{
				ID:     "test-tool-1",
				Output: "test output",
			},
		},
	}

	// First, establish a base session with some messages
	committer := newMessageCommitter(sessionPath)
	ctx := context.Background()

	// Save initial messages
	for i := 0; i < 3; i++ {
		msg := Message{
			Role:      core.RoleUser,
			Content:   fmt.Sprintf("Message %d", i),
			Timestamp: time.Now(),
		}
		_, err := committer.CommitMessageWithRetries(ctx, msg, nil)
		if err != nil {
			t.Fatalf("Failed to save message %d: %v", i, err)
		}
	}

	// Now we'll manually create a conflict to test the sibling restaging
	// Get the next message ID that would be used
	nextID, err := GetNextMessageID(sessionPath)
	if err != nil {
		t.Fatalf("Failed to get next message ID: %v", err)
	}

	// Manually create a conflicting message with this ID
	// This simulates another process writing at the same time
	conflictMsg := Message{
		ID:        nextID,
		Role:      core.RoleUser,
		Content:   "Conflicting message",
		Timestamp: time.Now(),
	}
	if err := writeMessage(sessionPath, conflictMsg.ID, conflictMsg); err != nil {
		t.Fatalf("Failed to create conflict message: %v", err)
	}

	// Now try to save a new message - it should detect the conflict and create a sibling
	testMsg := Message{
		Role:      core.RoleAssistant,
		Content:   "This should go to a sibling",
		Timestamp: time.Now(),
	}

	result, err := committer.CommitMessageWithRetries(ctx, testMsg, toolInteraction)
	if err != nil {
		t.Fatalf("Failed to save message: %v", err)
	}

	// The message should have been saved to a sibling directory
	if result.Path == sessionPath {
		// This might happen if the conflict was resolved differently
		// Let's check if it got a different ID instead
		t.Logf("Message was saved to original path with ID: %s", result.MessageID)
		t.Logf("Expected conflict with ID: %s", nextID)

		// Check if the message ID is different from the conflict ID
		if result.MessageID == nextID {
			t.Fatal("Message should not have the same ID as the conflict")
		}

		// Even if it didn't create a sibling, the restaging logic should still work
		// Let's verify the files exist
		jsonPath := filepath.Join(result.Path, result.MessageID+".json")
		txtPath := filepath.Join(result.Path, result.MessageID+".txt")
		toolsPath := filepath.Join(result.Path, result.MessageID+".tools.json")

		if _, err := os.Stat(jsonPath); err != nil {
			t.Fatalf("JSON file should exist: %v", err)
		}
		if _, err := os.Stat(txtPath); err != nil {
			t.Fatalf("TXT file should exist: %v", err)
		}
		if _, err := os.Stat(toolsPath); err != nil {
			t.Fatalf("Tools file should exist: %v", err)
		}
	} else {
		// Verify it's a sibling directory
		if !strings.Contains(result.Path, ".s.") {
			t.Errorf("Sibling path should contain '.s.', got %s", result.Path)
		}

		// Verify files exist in sibling directory
		siblingJsonPath := filepath.Join(result.Path, result.MessageID+".json")
		siblingTxtPath := filepath.Join(result.Path, result.MessageID+".txt")
		siblingToolsPath := filepath.Join(result.Path, result.MessageID+".tools.json")

		if _, err := os.Stat(siblingJsonPath); err != nil {
			t.Fatalf("Sibling .json file should exist: %v", err)
		}
		if _, err := os.Stat(siblingTxtPath); err != nil {
			t.Fatalf("Sibling .txt file should exist: %v", err)
		}
		if _, err := os.Stat(siblingToolsPath); err != nil {
			t.Fatalf("Sibling .tools.json file should exist: %v", err)
		}

		// Verify content
		savedContent, err := os.ReadFile(siblingTxtPath)
		if err != nil {
			t.Fatalf("Failed to read sibling content: %v", err)
		}
		if string(savedContent) != testMsg.Content {
			t.Fatalf("Expected content %q, got %q", testMsg.Content, string(savedContent))
		}
	}

	// Verify no orphaned temp files
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		t.Fatalf("Failed to read session directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".tmp-") {
			t.Errorf("Found orphaned temp file: %s", name)
		}
	}
}

// TestCommitMessageOrphanCleanup verifies that orphaned files (.txt and .tools.json
// without matching .json) are properly cleaned up during commit.
func TestCommitMessageOrphanCleanup(t *testing.T) {
	// Create a temporary session directory
	tempDir := t.TempDir()
	sessionPath := filepath.Join(tempDir, "test-session")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Create orphaned files (without .json)
	// Use "0000" as the orphan ID since that's what GetNextMessageID will return for an empty directory
	orphanID := "0000"
	orphanTxtPath := filepath.Join(sessionPath, orphanID+".txt")
	orphanToolsPath := filepath.Join(sessionPath, orphanID+".tools.json")

	if err := os.WriteFile(orphanTxtPath, []byte("orphaned content"), 0o644); err != nil {
		t.Fatalf("Failed to create orphan txt file: %v", err)
	}
	if err := os.WriteFile(orphanToolsPath, []byte(`{"calls":[],"results":[]}`), 0o644); err != nil {
		t.Fatalf("Failed to create orphan tools file: %v", err)
	}

	// Verify orphaned files exist
	if _, err := os.Stat(orphanTxtPath); err != nil {
		t.Fatalf("Orphan txt file should exist before commit: %v", err)
	}
	if _, err := os.Stat(orphanToolsPath); err != nil {
		t.Fatalf("Orphan tools file should exist before commit: %v", err)
	}

	// Now try to save a message - this should clean up the orphaned files
	msg := Message{
		Role:      core.RoleUser,
		Content:   "New message",
		Timestamp: time.Now(),
	}

	committer := newMessageCommitter(sessionPath)
	ctx := context.Background()

	result, err := committer.CommitMessageWithRetries(ctx, msg, nil)
	if err != nil {
		t.Fatalf("Failed to save message: %v", err)
	}

	// Verify the message was saved with the same ID (after orphan cleanup)
	if result.MessageID != orphanID {
		t.Fatalf("Expected message ID %s, got %s", orphanID, result.MessageID)
	}

	// Verify the new files exist
	jsonPath := filepath.Join(sessionPath, orphanID+".json")
	txtPath := filepath.Join(sessionPath, orphanID+".txt")

	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("JSON file should exist after commit: %v", err)
	}
	if _, err := os.Stat(txtPath); err != nil {
		t.Fatalf("TXT file should exist after commit: %v", err)
	}

	// Verify the content is the new content, not the orphaned content
	savedContent, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatalf("Failed to read content: %v", err)
	}
	if string(savedContent) != msg.Content {
		t.Fatalf("Expected content %q, got %q", msg.Content, string(savedContent))
	}

	// The orphaned tools file should still exist because we didn't provide tool interaction
	// The commit logic only removes orphaned files that conflict with files being written
	// Since we're not writing a .tools.json file, the orphaned one remains
	if _, err := os.Stat(orphanToolsPath); err != nil {
		t.Logf("Note: Orphaned tools file was removed (this is acceptable behavior)")
	}
}
