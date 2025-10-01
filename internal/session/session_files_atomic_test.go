package session

import (
	"context"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCommitFilesSuccess tests successful atomic rename of multiple files
func TestCommitFilesSuccess(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "commit_files_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temporary files
	tmp1 := filepath.Join(tmpDir, "tmp1.txt")
	tmp2 := filepath.Join(tmpDir, "tmp2.json")
	tmp3 := filepath.Join(tmpDir, "tmp3.tools.json")

	if err := os.WriteFile(tmp1, []byte("content1"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp1: %v", err)
	}
	if err := os.WriteFile(tmp2, []byte("content2"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp2: %v", err)
	}
	if err := os.WriteFile(tmp3, []byte("content3"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp3: %v", err)
	}

	// Define final paths
	final1 := filepath.Join(tmpDir, "final1.txt")
	final2 := filepath.Join(tmpDir, "final2.json")
	final3 := filepath.Join(tmpDir, "final3.tools.json")

	// Test successful commit
	files := []filePair{
		{Tmp: tmp1, Final: final1},
		{Tmp: tmp2, Final: final2},
		{Tmp: tmp3, Final: final3},
	}

	_, err = commitFiles(context.Background(), files)
	if err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// Verify all files were renamed
	for _, f := range files {
		if _, err := os.Stat(f.Tmp); !os.IsNotExist(err) {
			t.Errorf("Temporary file still exists: %s", f.Tmp)
		}
		if _, err := os.Stat(f.Final); err != nil {
			t.Errorf("Final file does not exist: %s", f.Final)
		}
	}
}

// TestCommitFilesRollbackAtomic tests rollback on failure (partial rename scenario)
func TestCommitFilesRollbackAtomic(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "commit_files_rollback_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temporary files
	tmp1 := filepath.Join(tmpDir, "tmp1.txt")
	tmp2 := filepath.Join(tmpDir, "tmp2.json")

	if err := os.WriteFile(tmp1, []byte("content1"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp1: %v", err)
	}
	if err := os.WriteFile(tmp2, []byte("content2"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp2: %v", err)
	}

	// Define final paths
	final1 := filepath.Join(tmpDir, "final1.txt")
	final2 := filepath.Join(tmpDir, "final2.json")

	// Create a conflict for the second file
	if err := os.WriteFile(final2, []byte("existing"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create conflict file: %v", err)
	}

	// Test commit with conflict
	files := []filePair{
		{Tmp: tmp1, Final: final1},
		{Tmp: tmp2, Final: final2}, // This will fail due to existing file
	}

	_, err = commitFiles(context.Background(), files)
	if err == nil {
		t.Fatal("Expected commitFiles to fail due to conflict")
	}

	// Verify rollback: tmp1 should be back, final1 should not exist
	if _, err := os.Stat(tmp1); err != nil {
		t.Errorf("Temporary file should exist after rollback: %s", tmp1)
	}
	if _, err := os.Stat(final1); !os.IsNotExist(err) {
		t.Errorf("Final file should not exist after rollback: %s", final1)
	}

	// tmp2 should still exist
	if _, err := os.Stat(tmp2); err != nil {
		t.Errorf("Temporary file 2 should still exist: %s", tmp2)
	}
}

// TestCommitFilesOrphanedFileCleanup tests orphaned file cleanup (.txt/.tools.json without .json)
func TestCommitFilesOrphanedFileCleanup(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "commit_files_orphan_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create orphaned files (no matching .json)
	orphanedTxt := filepath.Join(tmpDir, "msg001.txt")
	orphanedTools := filepath.Join(tmpDir, "msg001.tools.json")

	if err := os.WriteFile(orphanedTxt, []byte("orphaned text"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create orphaned txt: %v", err)
	}
	if err := os.WriteFile(orphanedTools, []byte("orphaned tools"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create orphaned tools: %v", err)
	}

	// Create temporary files to commit
	tmpTxt := filepath.Join(tmpDir, "tmp.txt")
	tmpJson := filepath.Join(tmpDir, "tmp.json")
	tmpTools := filepath.Join(tmpDir, "tmp.tools.json")

	if err := os.WriteFile(tmpTxt, []byte("new text"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp txt: %v", err)
	}
	if err := os.WriteFile(tmpJson, []byte("new json"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp json: %v", err)
	}
	if err := os.WriteFile(tmpTools, []byte("new tools"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp tools: %v", err)
	}

	// Test commit - should clean up orphaned files
	files := []filePair{
		{Tmp: tmpTxt, Final: orphanedTxt},                           // Should overwrite orphaned txt
		{Tmp: tmpTools, Final: orphanedTools},                       // Should overwrite orphaned tools
		{Tmp: tmpJson, Final: filepath.Join(tmpDir, "msg001.json")}, // Commit point
	}

	_, err = commitFiles(context.Background(), files)
	if err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// Verify orphaned files were replaced
	content, err := os.ReadFile(orphanedTxt)
	if err != nil {
		t.Fatalf("Failed to read txt file: %v", err)
	}
	if string(content) != "new text" {
		t.Errorf("Orphaned txt file was not replaced, content: %s", content)
	}

	content, err = os.ReadFile(orphanedTools)
	if err != nil {
		t.Fatalf("Failed to read tools file: %v", err)
	}
	if string(content) != "new tools" {
		t.Errorf("Orphaned tools file was not replaced, content: %s", content)
	}
}

// TestCommitFilesEmptyEntries tests handling of empty entries in file list
func TestCommitFilesEmptyEntries(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "commit_files_empty_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create one valid file
	tmp1 := filepath.Join(tmpDir, "tmp1.txt")
	if err := os.WriteFile(tmp1, []byte("content1"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp1: %v", err)
	}

	// Test with empty entries
	files := []filePair{
		{Tmp: "", Final: ""}, // Empty entry - should be skipped
		{Tmp: tmp1, Final: filepath.Join(tmpDir, "final1.txt")}, // Valid entry
		{Tmp: "nonexistent", Final: "shouldskip"},               // Non-existent file - should be skipped
	}

	_, err = commitFiles(context.Background(), files)
	if err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// Verify only the valid file was processed
	if _, err := os.Stat(filepath.Join(tmpDir, "final1.txt")); err != nil {
		t.Errorf("Valid file was not renamed: %v", err)
	}
}

// TestTryPlaceMessageFilesSuccess tests successful atomic placement of message files
func TestTryPlaceMessageFilesSuccess(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "try_place_success_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temporary files
	tmpTxt := filepath.Join(tmpDir, "tmp.txt")
	tmpJson := filepath.Join(tmpDir, "tmp.json")
	tmpTools := filepath.Join(tmpDir, "tmp.tools.json")

	if err := os.WriteFile(tmpTxt, []byte("message content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp txt: %v", err)
	}
	if err := os.WriteFile(tmpJson, []byte(`{"role":"user","timestamp":"2024-01-01T00:00:00Z"}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp json: %v", err)
	}
	if err := os.WriteFile(tmpTools, []byte(`{"calls":[],"results":[]}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp tools: %v", err)
	}

	// Test successful placement using messageCommitter
	mc := newMessageCommitter(tmpDir)
	staging := &MessageStaging{
		TxtPath:   tmpTxt,
		JsonPath:  tmpJson,
		ToolsPath: tmpTools,
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
		t.Errorf("Expected empty siblingPath, got %s", siblingPath)
	}

	// Verify files were placed correctly
	finalTxt := filepath.Join(tmpDir, msgID+".txt")
	finalJson := filepath.Join(tmpDir, msgID+".json")
	finalTools := filepath.Join(tmpDir, msgID+".tools.json")

	if _, err := os.Stat(finalTxt); err != nil {
		t.Errorf("Final txt file does not exist: %v", err)
	}
	if _, err := os.Stat(finalJson); err != nil {
		t.Errorf("Final json file does not exist: %v", err)
	}
	if _, err := os.Stat(finalTools); err != nil {
		t.Errorf("Final tools file does not exist: %v", err)
	}

	// Verify temp files were removed
	if _, err := os.Stat(tmpTxt); !os.IsNotExist(err) {
		t.Errorf("Temp txt file still exists")
	}
	if _, err := os.Stat(tmpJson); !os.IsNotExist(err) {
		t.Errorf("Temp json file still exists")
	}
	if _, err := os.Stat(tmpTools); !os.IsNotExist(err) {
		t.Errorf("Temp tools file still exists")
	}
}

// TestTryPlaceMessageFilesNoTools tests placement without tools file
func TestTryPlaceMessageFilesNoTools(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "try_place_no_tools_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temporary files (no tools)
	tmpTxt := filepath.Join(tmpDir, "tmp.txt")
	tmpJson := filepath.Join(tmpDir, "tmp.json")
	tmpTools := "" // Empty path means no tools file

	if err := os.WriteFile(tmpTxt, []byte("message without tools"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp txt: %v", err)
	}
	if err := os.WriteFile(tmpJson, []byte(`{"role":"user","timestamp":"2024-01-01T00:00:00Z"}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp json: %v", err)
	}

	// Test successful placement without tools using messageCommitter
	mc := newMessageCommitter(tmpDir)
	staging := &MessageStaging{
		TxtPath:   tmpTxt,
		JsonPath:  tmpJson,
		ToolsPath: tmpTools, // Empty path means no tools file
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
		t.Errorf("Expected empty siblingPath, got %s", siblingPath)
	}

	// Verify only txt and json files were placed
	finalTxt := filepath.Join(tmpDir, msgID+".txt")
	finalJson := filepath.Join(tmpDir, msgID+".json")
	finalTools := filepath.Join(tmpDir, msgID+".tools.json")

	if _, err := os.Stat(finalTxt); err != nil {
		t.Errorf("Final txt file does not exist: %v", err)
	}
	if _, err := os.Stat(finalJson); err != nil {
		t.Errorf("Final json file does not exist: %v", err)
	}
	if _, err := os.Stat(finalTools); !os.IsNotExist(err) {
		t.Errorf("Tools file should not exist")
	}
}

// TestTryPlaceMessageFilesLockTimeout tests lock timeout handling
func TestTryPlaceMessageFilesLockTimeout(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "try_place_lock_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temporary files
	tmpTxt := filepath.Join(tmpDir, "tmp.txt")
	tmpJson := filepath.Join(tmpDir, "tmp.json")

	if err := os.WriteFile(tmpTxt, []byte("content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp txt: %v", err)
	}
	if err := os.WriteFile(tmpJson, []byte(`{"role":"user"}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp json: %v", err)
	}

	// Acquire lock to simulate contention
	// We'll use a goroutine to hold the lock
	lockHeld := make(chan struct{})
	lockReleased := make(chan struct{})

	go func() {
		err := WithSessionLock(tmpDir, 10*time.Second, func() error {
			close(lockHeld)
			<-lockReleased // Wait for signal to release
			return nil
		})
		if err != nil {
			t.Errorf("Failed to hold lock: %v", err)
		}
	}()

	// Wait for lock to be acquired
	<-lockHeld

	// Try to place files (should fail due to lock)
	done := make(chan struct{})
	var placeErr error

	go func() {
		mc := newMessageCommitter(tmpDir)
		staging := &MessageStaging{
			TxtPath:  tmpTxt,
			JsonPath: tmpJson,
		}
		_, _, _, placeErr = mc.Commit(context.Background(), staging)
		close(done)
	}()

	// Since we can't easily detect when the lock is attempted,
	// we'll just wait for the operation to complete or timeout

	select {
	case <-done:
		if placeErr == nil {
			t.Errorf("Expected error due to lock timeout, got nil")
		}
	case <-time.After(6 * time.Second):
		t.Errorf("tryPlaceMessageFiles did not timeout as expected")
	}

	// Release the lock
	close(lockReleased)
}
