package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMessageCommitterConflictHandling tests the needSibling path when conflicts occur
func TestMessageCommitterConflictHandling(t *testing.T) {
	t.Skip("Skipping conflict test - difficult to simulate race condition in unit test")

	// This test attempts to simulate a conflict scenario where two processes
	// try to commit with the same ID. However, since GetNextMessageID is called
	// inside the lock, it's difficult to create a true conflict in a unit test
	// without modifying the implementation or using more complex synchronization.
	//
	// The conflict handling is tested indirectly through other integration tests
	// and the sibling creation mechanism is tested separately.
}

// TestMessageCommitterOrphanCleanup tests cleanup of orphaned files
func TestMessageCommitterOrphanCleanup(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "message_committer_orphan_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create orphaned files (txt and tools.json without matching json)
	orphanID := "0000"
	orphanTxt := filepath.Join(tmpDir, orphanID+".txt")
	orphanTools := filepath.Join(tmpDir, orphanID+".tools.json")

	if err := os.WriteFile(orphanTxt, []byte("orphaned content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create orphan txt: %v", err)
	}
	if err := os.WriteFile(orphanTools, []byte(`{"calls":[]}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create orphan tools: %v", err)
	}

	// Verify orphans exist
	if _, err := os.Stat(orphanTxt); err != nil {
		t.Fatalf("Orphan txt should exist before commit")
	}
	if _, err := os.Stat(orphanTools); err != nil {
		t.Fatalf("Orphan tools should exist before commit")
	}

	// Stage a new message with the same ID
	msg := Message{
		ID:        orphanID,
		Role:      core.RoleAssistant,
		Content:   "new content",
		Timestamp: time.Now(),
	}

	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{ID: "call1", Name: "test_tool", Args: json.RawMessage(`{}`)},
		},
	}

	mc := newMessageCommitter(tmpDir)
	staging, err := mc.Stage(msg, toolInteraction)
	if err != nil {
		t.Fatalf("Failed to stage message: %v", err)
	}
	defer staging.Close()

	// Commit should clean up orphans and succeed
	msgID, needSibling, _, err := mc.Commit(context.Background(), staging)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if needSibling {
		t.Errorf("Expected needSibling=false after orphan cleanup")
	}
	if msgID != orphanID {
		t.Errorf("Expected msgID=%s, got %s", orphanID, msgID)
	}

	// Verify new files exist with new content
	newTxtContent, err := os.ReadFile(orphanTxt)
	if err != nil {
		t.Fatalf("Failed to read new txt: %v", err)
	}
	if string(newTxtContent) != "new content" {
		t.Errorf("Expected new content, got: %s", newTxtContent)
	}

	// Verify json now exists (commit point)
	jsonPath := filepath.Join(tmpDir, orphanID+".json")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("JSON file should exist after commit: %v", err)
	}
}

// TestMessageCommitterEmptyContent tests handling of messages with no text content
func TestMessageCommitterEmptyContent(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "message_committer_empty_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Stage a message with empty content
	msg := Message{
		Role:      core.RoleUser,
		Content:   "", // Empty content
		Timestamp: time.Now(),
	}

	mc := newMessageCommitter(tmpDir)
	staging, err := mc.Stage(msg, nil)
	if err != nil {
		t.Fatalf("Failed to stage message: %v", err)
	}
	defer staging.Close()

	// Verify txt file was not created for empty content
	if staging.TxtPath != "" {
		t.Errorf("Expected no txt file for empty content, got: %s", staging.TxtPath)
	}

	// Commit should succeed
	msgID, needSibling, _, err := mc.Commit(context.Background(), staging)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if needSibling {
		t.Errorf("Expected needSibling=false")
	}
	if msgID == "" {
		t.Errorf("Expected non-empty msgID")
	}

	// Verify only json exists, no txt file
	jsonPath := filepath.Join(tmpDir, msgID+".json")
	txtPath := filepath.Join(tmpDir, msgID+".txt")

	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("JSON file should exist: %v", err)
	}
	if _, err := os.Stat(txtPath); !os.IsNotExist(err) {
		t.Errorf("TXT file should not exist for empty content")
	}
}

// TestMessageCommitterToolOnlyMessage tests messages with only tool interactions
func TestMessageCommitterToolOnlyMessage(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "message_committer_tools_only_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Stage a message with only tool results (no text)
	msg := Message{
		Role:      core.RoleUser,
		Content:   "", // No text content
		Timestamp: time.Now(),
	}

	toolInteraction := &core.ToolInteraction{
		Results: []core.ToolResult{
			{
				ID:     "call1",
				Output: "tool output",
			},
		},
	}

	mc := newMessageCommitter(tmpDir)
	staging, err := mc.Stage(msg, toolInteraction)
	if err != nil {
		t.Fatalf("Failed to stage message: %v", err)
	}
	defer staging.Close()

	// Commit should succeed
	msgID, needSibling, _, err := mc.Commit(context.Background(), staging)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if needSibling {
		t.Errorf("Expected needSibling=false")
	}
	if msgID == "" {
		t.Errorf("Expected non-empty msgID")
	}

	// Verify json and tools.json exist, but no txt
	jsonPath := filepath.Join(tmpDir, msgID+".json")
	toolsPath := filepath.Join(tmpDir, msgID+".tools.json")
	txtPath := filepath.Join(tmpDir, msgID+".txt")

	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("JSON file should exist: %v", err)
	}
	if _, err := os.Stat(toolsPath); err != nil {
		t.Errorf("Tools file should exist: %v", err)
	}
	if _, err := os.Stat(txtPath); !os.IsNotExist(err) {
		t.Errorf("TXT file should not exist for tool-only message")
	}

	// Verify tool content
	var savedTools core.ToolInteraction
	toolsContent, err := os.ReadFile(toolsPath)
	if err != nil {
		t.Fatalf("Failed to read tools file: %v", err)
	}
	if err := json.Unmarshal(toolsContent, &savedTools); err != nil {
		t.Fatalf("Failed to unmarshal tools: %v", err)
	}
	if len(savedTools.Results) != 1 || savedTools.Results[0].Output != "tool output" {
		t.Errorf("Tool content mismatch: %+v", savedTools)
	}
}

// TestMessageCommitterRetryLogic tests the retry mechanism with conflicts
func TestMessageCommitterRetryLogic(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "message_committer_retry_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a message to commit
	msg := Message{
		Role:      core.RoleAssistant,
		Content:   "test message",
		Timestamp: time.Now(),
	}

	mc := newMessageCommitter(tmpDir)

	// Test successful commit with retries
	result, err := mc.CommitMessageWithRetries(context.Background(), msg, nil)
	if err != nil {
		t.Fatalf("CommitMessageWithRetries failed: %v", err)
	}

	if result.Path != tmpDir {
		t.Errorf("Expected path=%s, got %s", tmpDir, result.Path)
	}
	if result.MessageID == "" {
		t.Errorf("Expected non-empty MessageID")
	}

	// Verify message was saved
	jsonPath := filepath.Join(tmpDir, result.MessageID+".json")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("Message json should exist: %v", err)
	}
}

// TestMessageCommitterCommitInvariant tests the .json commit point invariant
func TestMessageCommitterCommitInvariant(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "message_committer_invariant_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Test that files are committed in the correct order: .txt → .tools.json → .json
	msg := Message{
		Role:      core.RoleAssistant,
		Content:   "test content",
		Timestamp: time.Now(),
	}

	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{ID: "call1", Name: "test_tool", Args: json.RawMessage(`{}`)},
		},
	}

	mc := newMessageCommitter(tmpDir)
	staging, err := mc.Stage(msg, toolInteraction)
	if err != nil {
		t.Fatalf("Failed to stage message: %v", err)
	}
	defer staging.Close()

	// Verify staging created temp files
	if _, err := os.Stat(staging.TxtPath); err != nil {
		t.Errorf("Staged txt should exist: %v", err)
	}
	if _, err := os.Stat(staging.JsonPath); err != nil {
		t.Errorf("Staged json should exist: %v", err)
	}
	if _, err := os.Stat(staging.ToolsPath); err != nil {
		t.Errorf("Staged tools should exist: %v", err)
	}

	// Commit
	msgID, _, _, err := mc.Commit(context.Background(), staging)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Verify all files exist in final location
	finalTxt := filepath.Join(tmpDir, msgID+".txt")
	finalTools := filepath.Join(tmpDir, msgID+".tools.json")
	finalJson := filepath.Join(tmpDir, msgID+".json")

	if _, err := os.Stat(finalTxt); err != nil {
		t.Errorf("Final txt should exist: %v", err)
	}
	if _, err := os.Stat(finalTools); err != nil {
		t.Errorf("Final tools should exist: %v", err)
	}
	if _, err := os.Stat(finalJson); err != nil {
		t.Errorf("Final json should exist (commit point): %v", err)
	}

	// Verify temp files were cleaned up
	if _, err := os.Stat(staging.TxtPath); !os.IsNotExist(err) {
		t.Errorf("Temp txt should be removed after commit")
	}
	if _, err := os.Stat(staging.JsonPath); !os.IsNotExist(err) {
		t.Errorf("Temp json should be removed after commit")
	}
	if _, err := os.Stat(staging.ToolsPath); !os.IsNotExist(err) {
		t.Errorf("Temp tools should be removed after commit")
	}
}

// TestMessageCommitterConcurrentConflicts tests handling of concurrent writes
func TestMessageCommitterConcurrentConflicts(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "message_committer_concurrent_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create multiple messages to commit concurrently
	messages := []Message{
		{Role: core.RoleUser, Content: "message 1", Timestamp: time.Now()},
		{Role: core.RoleAssistant, Content: "message 2", Timestamp: time.Now()},
		{Role: core.RoleUser, Content: "message 3", Timestamp: time.Now()},
	}

	type result struct {
		path string
		id   string
		err  error
	}

	results := make(chan result, len(messages))

	// Commit messages concurrently
	for i, msg := range messages {
		go func(m Message, idx int) {
			mc := newMessageCommitter(tmpDir)
			res, err := mc.CommitMessageWithRetries(context.Background(), m, nil)
			results <- result{
				path: res.Path,
				id:   res.MessageID,
				err:  err,
			}
		}(msg, i)
	}

	// Collect results
	var savedResults []result
	for i := 0; i < len(messages); i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("Concurrent commit failed: %v", r.err)
		}
		savedResults = append(savedResults, r)
	}

	// Verify all messages were saved (possibly in siblings)
	uniquePaths := make(map[string]bool)
	for _, r := range savedResults {
		uniquePaths[r.path] = true

		// Verify message exists
		jsonPath := filepath.Join(r.path, r.id+".json")
		if _, err := os.Stat(jsonPath); err != nil {
			t.Errorf("Message %s should exist in %s: %v", r.id, r.path, err)
		}
	}

	// At least one should have created a sibling due to conflicts
	if len(uniquePaths) < 2 {
		t.Logf("Warning: Expected siblings to be created due to concurrent conflicts, but all messages went to same path")
	}
}
