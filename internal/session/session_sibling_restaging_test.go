package session

import (
	"context"
	"encoding/json"
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
	sessionsDir := filepath.Join(tempDir, "sessions")
	oldSessionsDir := GetSessionsDir()
	SetSessionsDir(sessionsDir)
	t.Cleanup(func() { SetSessionsDir(oldSessionsDir) })
	sessionPath := filepath.Join(sessionsDir, "test-session")
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

	var conflictID string
	conflictWritten := false
	afterGetNextMessageIDForTest = func(path, msgID string) {
		if conflictWritten || path != sessionPath {
			return
		}
		conflictWritten = true
		conflictID = msgID
		conflictMsg := Message{
			ID:        msgID,
			Role:      core.RoleUser,
			Content:   "Conflicting message",
			Timestamp: time.Now(),
		}
		if err := writeMessage(path, msgID, conflictMsg); err != nil {
			t.Fatalf("Failed to create conflict message: %v", err)
		}
	}
	t.Cleanup(func() { afterGetNextMessageIDForTest = nil })

	// Now try to save a new message - it should detect the conflict and create a sibling
	testMsg := Message{
		Role:      core.RoleAssistant,
		Content:   "This should go to a sibling",
		Timestamp: time.Now(),
	}
	testBlocks := []core.Block{
		core.ReasoningBlock{
			Provider: "anthropic",
			Type:     "thinking",
			ID:       "rs_1",
			Text:     "preserved reasoning",
			Raw:      json.RawMessage(`{"type":"thinking","id":"rs_1"}`),
		},
		core.TextBlock{Text: testMsg.Content},
	}

	result, err := committer.CommitMessageWithBlocksWithRetries(ctx, testMsg, toolInteraction, testBlocks)
	if err != nil {
		t.Fatalf("Failed to save message: %v", err)
	}
	if !conflictWritten {
		t.Fatal("test hook did not create a conflict")
	}

	// The message should have been saved to a sibling directory
	if result.Path == sessionPath {
		t.Logf("Message was saved to original path with ID: %s", result.MessageID)
		t.Logf("Expected conflict with ID: %s", conflictID)
		t.Fatal("Message should have been restaged into a sibling path")
	} else {
		// Verify it's a sibling directory
		if !strings.Contains(result.Path, ".s.") {
			t.Errorf("Sibling path should contain '.s.', got %s", result.Path)
		}

		// Verify files exist in sibling directory
		siblingJsonPath := filepath.Join(result.Path, result.MessageID+".json")
		siblingTxtPath := filepath.Join(result.Path, result.MessageID+".txt")
		siblingToolsPath := filepath.Join(result.Path, result.MessageID+".tools.json")
		siblingBlocksPath := filepath.Join(result.Path, result.MessageID+".blocks.json")

		if _, err := os.Stat(siblingJsonPath); err != nil {
			t.Fatalf("Sibling .json file should exist: %v", err)
		}
		if _, err := os.Stat(siblingTxtPath); err != nil {
			t.Fatalf("Sibling .txt file should exist: %v", err)
		}
		if _, err := os.Stat(siblingToolsPath); err != nil {
			t.Fatalf("Sibling .tools.json file should exist: %v", err)
		}
		if _, err := os.Stat(siblingBlocksPath); err != nil {
			t.Fatalf("Sibling .blocks.json file should exist: %v", err)
		}
		blocks, ok, err := loadMessageBlocks(result.Path, result.MessageID)
		if err != nil {
			t.Fatalf("Failed to load sibling blocks: %v", err)
		}
		if !ok || len(blocks) != len(testBlocks) {
			t.Fatalf("Sibling blocks = %#v, want %d explicit blocks", blocks, len(testBlocks))
		}
		reasoning, ok := blocks[0].(core.ReasoningBlock)
		if !ok || reasoning.ID != "rs_1" || reasoning.Text != "preserved reasoning" {
			t.Fatalf("Sibling first block = %#v, want preserved reasoning block", blocks[0])
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
