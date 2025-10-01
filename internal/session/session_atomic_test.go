package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestCommitFilesAtomicity tests the atomic behavior of commitFiles
func TestCommitFilesAtomicity(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name        string
		files       []filePair
		setupFunc   func() error
		expectError bool
		checkFunc   func() error
	}{
		{
			name: "successful commit of multiple files",
			files: []filePair{
				{Tmp: filepath.Join(tmpDir, "tmp1.txt"), Final: filepath.Join(tmpDir, "final1.txt")},
				{Tmp: filepath.Join(tmpDir, "tmp2.json"), Final: filepath.Join(tmpDir, "final2.json")},
			},
			setupFunc: func() error {
				// Create temp files
				if err := os.WriteFile(filepath.Join(tmpDir, "tmp1.txt"), []byte("content1"), 0o644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(tmpDir, "tmp2.json"), []byte(`{"test":true}`), 0o644)
			},
			expectError: false,
			checkFunc: func() error {
				// Verify files were renamed
				if _, err := os.Stat(filepath.Join(tmpDir, "final1.txt")); err != nil {
					return fmt.Errorf("final1.txt should exist: %w", err)
				}
				if _, err := os.Stat(filepath.Join(tmpDir, "final2.json")); err != nil {
					return fmt.Errorf("final2.json should exist: %w", err)
				}
				// Verify temp files don't exist
				if _, err := os.Stat(filepath.Join(tmpDir, "tmp1.txt")); !os.IsNotExist(err) {
					return fmt.Errorf("tmp1.txt should not exist")
				}
				if _, err := os.Stat(filepath.Join(tmpDir, "tmp2.json")); !os.IsNotExist(err) {
					return fmt.Errorf("tmp2.json should not exist")
				}
				return nil
			},
		},
		{
			name: "rollback on conflict",
			files: []filePair{
				{Tmp: filepath.Join(tmpDir, "tmp3.txt"), Final: filepath.Join(tmpDir, "final3.txt")},
				{Tmp: filepath.Join(tmpDir, "tmp4.json"), Final: filepath.Join(tmpDir, "conflict.json")},
			},
			setupFunc: func() error {
				// Create temp files
				if err := os.WriteFile(filepath.Join(tmpDir, "tmp3.txt"), []byte("content3"), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(tmpDir, "tmp4.json"), []byte(`{"test":true}`), 0o644); err != nil {
					return err
				}
				// Create conflict file
				return os.WriteFile(filepath.Join(tmpDir, "conflict.json"), []byte(`{"existing":true}`), 0o644)
			},
			expectError: true,
			checkFunc: func() error {
				// Verify rollback - temp files should still exist
				if _, err := os.Stat(filepath.Join(tmpDir, "tmp3.txt")); err != nil {
					return fmt.Errorf("tmp3.txt should still exist after rollback: %w", err)
				}
				if _, err := os.Stat(filepath.Join(tmpDir, "tmp4.json")); err != nil {
					return fmt.Errorf("tmp4.json should still exist after rollback: %w", err)
				}
				// Verify no partial commit
				if _, err := os.Stat(filepath.Join(tmpDir, "final3.txt")); !os.IsNotExist(err) {
					return fmt.Errorf("final3.txt should not exist after rollback")
				}
				return nil
			},
		},
		{
			name: "orphaned file cleanup",
			files: []filePair{
				{Tmp: filepath.Join(tmpDir, "tmp5.txt"), Final: filepath.Join(tmpDir, "0001.txt")},
				{Tmp: filepath.Join(tmpDir, "tmp6.json"), Final: filepath.Join(tmpDir, "0001.json")},
			},
			setupFunc: func() error {
				// Create temp files
				if err := os.WriteFile(filepath.Join(tmpDir, "tmp5.txt"), []byte("content5"), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(tmpDir, "tmp6.json"), []byte(`{"role":"user"}`), 0o644); err != nil {
					return err
				}
				// Create orphaned .txt file (no corresponding .json)
				return os.WriteFile(filepath.Join(tmpDir, "0001.txt"), []byte("orphaned"), 0o644)
			},
			expectError: false,
			checkFunc: func() error {
				// Verify orphaned file was removed and replaced
				content, err := os.ReadFile(filepath.Join(tmpDir, "0001.txt"))
				if err != nil {
					return fmt.Errorf("0001.txt should exist: %w", err)
				}
				if string(content) != "content5" {
					return fmt.Errorf("0001.txt should contain new content, got: %s", content)
				}
				return nil
			},
		},
		{
			name: "skip empty files",
			files: []filePair{
				{Tmp: "", Final: ""},
				{Tmp: filepath.Join(tmpDir, "tmp7.json"), Final: filepath.Join(tmpDir, "final7.json")},
			},
			setupFunc: func() error {
				return os.WriteFile(filepath.Join(tmpDir, "tmp7.json"), []byte(`{"test":true}`), 0o644)
			},
			expectError: false,
			checkFunc: func() error {
				// Verify only the non-empty pair was processed
				if _, err := os.Stat(filepath.Join(tmpDir, "final7.json")); err != nil {
					return fmt.Errorf("final7.json should exist: %w", err)
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			if err := tt.setupFunc(); err != nil {
				t.Fatalf("Setup failed: %v", err)
			}

			// Execute
			_, err := commitFiles(context.Background(), tt.files)

			// Check error
			if (err != nil) != tt.expectError {
				t.Errorf("commitFiles() error = %v, expectError %v", err, tt.expectError)
			}

			// Check results
			if checkErr := tt.checkFunc(); checkErr != nil {
				t.Errorf("Check failed: %v", checkErr)
			}
		})
	}
}

// TestConcurrentSessionAccess tests concurrent access to sessions with locking
func TestConcurrentSessionAccess(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")

	// Create session directory
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Number of concurrent writers
	numWriters := 10
	messagesPerWriter := 5

	var wg sync.WaitGroup
	errors := make(chan error, numWriters)

	// Launch concurrent writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()

			for j := 0; j < messagesPerWriter; j++ {
				// Use WithSessionLock to ensure atomic operations
				err := WithSessionLock(sessionPath, 5*time.Second, func() error {
					// Get next message ID
					msgID, err := GetNextMessageID(sessionPath)
					if err != nil {
						return fmt.Errorf("writer %d: failed to get next ID: %w", writerID, err)
					}

					// Write message atomically
					msg := Message{
						ID:        msgID,
						Role:      "user",
						Content:   fmt.Sprintf("Message from writer %d, iteration %d", writerID, j),
						Timestamp: time.Now(),
					}

					return writeMessage(sessionPath, msgID, msg)
				})
				if err != nil {
					errors <- err
					return
				}
			}
		}(i)
	}

	// Wait for all writers to complete
	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent write error: %v", err)
	}

	// Verify all messages were written
	messages, err := listMessages(sessionPath)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}

	expectedCount := numWriters * messagesPerWriter
	if len(messages) != expectedCount {
		t.Errorf("Expected %d messages, got %d", expectedCount, len(messages))
	}

	// Verify message IDs are unique and sequential
	seen := make(map[string]bool)
	for _, msgID := range messages {
		if seen[msgID] {
			t.Errorf("Duplicate message ID: %s", msgID)
		}
		seen[msgID] = true
	}
}

// TestMessageExistsInvariant tests the "message exists iff .json exists" invariant
func TestMessageExistsInvariant(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")

	// Create session directory
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Test 1: Write complete message
	msg1 := Message{
		ID:        "0001",
		Role:      "user",
		Content:   "Test message",
		Timestamp: time.Now(),
	}

	if err := writeMessage(sessionPath, msg1.ID, msg1); err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	// Verify message is listed
	messages, err := listMessages(sessionPath)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}
	if len(messages) != 1 || messages[0] != "0001" {
		t.Errorf("Expected message 0001 to be listed, got: %v", messages)
	}

	// Test 2: Create orphaned .txt file (no .json)
	orphanedTxtPath := filepath.Join(sessionPath, "0002.txt")
	if err := os.WriteFile(orphanedTxtPath, []byte("orphaned content"), 0o644); err != nil {
		t.Fatalf("Failed to create orphaned .txt: %v", err)
	}

	// Verify orphaned file is not listed as a message
	messages, err = listMessages(sessionPath)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Errorf("Orphaned .txt should not be listed as message, got: %v", messages)
	}

	// Test 3: Create orphaned .tools.json file (no .json)
	orphanedToolsPath := filepath.Join(sessionPath, "0003.tools.json")
	if err := os.WriteFile(orphanedToolsPath, []byte(`{"calls":[]}`), 0o644); err != nil {
		t.Fatalf("Failed to create orphaned .tools.json: %v", err)
	}

	// Verify orphaned file is not listed as a message
	messages, err = listMessages(sessionPath)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}
	if len(messages) != 1 {
		t.Errorf("Orphaned .tools.json should not be listed as message, got: %v", messages)
	}

	// Test 4: Remove .json file to simulate incomplete write
	jsonPath := filepath.Join(sessionPath, "0001.json")
	if err := os.Remove(jsonPath); err != nil {
		t.Fatalf("Failed to remove .json: %v", err)
	}

	// Verify message is no longer listed
	messages, err = listMessages(sessionPath)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}
	if len(messages) != 0 {
		t.Errorf("Message without .json should not be listed, got: %v", messages)
	}
}

// TestTryPlaceMessageFiles tests the atomic message placement with conflict handling
func TestTryPlaceMessageFiles(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "test-session")

	// Create session directory
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Test 1: Successful placement
	tmpTxt1 := filepath.Join(tmpDir, "tmp1.txt")
	tmpJson1 := filepath.Join(tmpDir, "tmp1.json")

	if err := os.WriteFile(tmpTxt1, []byte("content1"), 0o644); err != nil {
		t.Fatalf("Failed to create temp txt: %v", err)
	}
	if err := os.WriteFile(tmpJson1, []byte(`{"role":"user"}`), 0o644); err != nil {
		t.Fatalf("Failed to create temp json: %v", err)
	}

	// Use messageCommitter directly
	mc := newMessageCommitter(sessionPath)
	staging := &MessageStaging{
		TxtPath:  tmpTxt1,
		JsonPath: tmpJson1,
	}
	msgID, needSibling, siblingPath, err := mc.Commit(context.Background(), staging)
	if err != nil {
		t.Fatalf("messageCommitter.Commit failed: %v", err)
	}
	if needSibling {
		t.Errorf("Expected needSibling=false, got true")
	}
	if msgID == "" {
		t.Errorf("Expected non-empty msgID")
	}
	if siblingPath != "" {
		t.Errorf("Expected empty siblingPath, got: %s", siblingPath)
	}

	// Verify files were placed
	if _, err := os.Stat(filepath.Join(sessionPath, msgID+".txt")); err != nil {
		t.Errorf("Message .txt file should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, msgID+".json")); err != nil {
		t.Errorf("Message .json file should exist: %v", err)
	}

	// Test 2: Test with tools file
	tmpTxt2 := filepath.Join(tmpDir, "tmp2.txt")
	tmpJson2 := filepath.Join(tmpDir, "tmp2.json")
	tmpTools2 := filepath.Join(tmpDir, "tmp2.tools.json")

	if err := os.WriteFile(tmpTxt2, []byte("content2"), 0o644); err != nil {
		t.Fatalf("Failed to create temp txt: %v", err)
	}
	if err := os.WriteFile(tmpJson2, []byte(`{"role":"assistant"}`), 0o644); err != nil {
		t.Fatalf("Failed to create temp json: %v", err)
	}
	if err := os.WriteFile(tmpTools2, []byte(`{"calls":[{"id":"1","name":"test"}]}`), 0o644); err != nil {
		t.Fatalf("Failed to create temp tools: %v", err)
	}

	// Use messageCommitter directly for second message
	staging2 := &MessageStaging{
		TxtPath:   tmpTxt2,
		JsonPath:  tmpJson2,
		ToolsPath: tmpTools2,
	}
	msgID2, needSibling, siblingPath, err := mc.Commit(context.Background(), staging2)
	if err != nil {
		t.Fatalf("messageCommitter.Commit with tools failed: %v", err)
	}
	if needSibling {
		t.Errorf("Expected needSibling=false for tools placement")
	}
	if msgID2 == "" {
		t.Errorf("Expected non-empty msgID")
	}
	if siblingPath != "" {
		t.Errorf("Expected empty siblingPath, got: %s", siblingPath)
	}

	// Verify all three files were placed
	if _, err := os.Stat(filepath.Join(sessionPath, msgID2+".txt")); err != nil {
		t.Errorf("Message .txt file should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, msgID2+".json")); err != nil {
		t.Errorf("Message .json file should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, msgID2+".tools.json")); err != nil {
		t.Errorf("Message .tools.json file should exist: %v", err)
	}
}
