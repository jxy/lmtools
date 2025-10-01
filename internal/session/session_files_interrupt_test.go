package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCommitFilesWithOrphanedFiles tests that commitFiles handles orphaned .txt/.tools.json files correctly
func TestCommitFilesWithOrphanedFiles(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "commit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Test case 1: Orphaned .txt file (no corresponding .json)
	t.Run("OrphanedTxtFile", func(t *testing.T) {
		// Create an orphaned .txt file
		orphanedTxt := filepath.Join(tmpDir, "message.txt")
		if err := os.WriteFile(orphanedTxt, []byte("orphaned content"), 0o644); err != nil {
			t.Fatal(err)
		}

		// Create temp files to commit
		tmpTxt := filepath.Join(tmpDir, "message.txt.tmp")
		tmpJSON := filepath.Join(tmpDir, "message.json.tmp")

		if err := os.WriteFile(tmpTxt, []byte("new content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tmpJSON, []byte(`{"role":"user","content":"test"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Commit files - should succeed by removing orphaned .txt
		files := []filePair{
			{Tmp: tmpTxt, Final: orphanedTxt},
			{Tmp: tmpJSON, Final: filepath.Join(tmpDir, "message.json")},
		}

		_, err := commitFiles(context.Background(), files)
		if err != nil {
			t.Errorf("Expected success when overwriting orphaned .txt, got error: %v", err)
		}

		// Verify files were committed
		content, err := os.ReadFile(orphanedTxt)
		if err != nil || string(content) != "new content" {
			t.Error("Failed to overwrite orphaned .txt file")
		}
	})

	// Test case 2: Orphaned .tools.json file (no corresponding .json)
	t.Run("OrphanedToolsFile", func(t *testing.T) {
		// Create an orphaned .tools.json file
		orphanedTools := filepath.Join(tmpDir, "message2.tools.json")
		if err := os.WriteFile(orphanedTools, []byte(`{"tools":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Create temp files to commit
		tmpTools := filepath.Join(tmpDir, "message2.tools.json.tmp")
		tmpJSON := filepath.Join(tmpDir, "message2.json.tmp")

		if err := os.WriteFile(tmpTools, []byte(`{"tools":["new"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tmpJSON, []byte(`{"role":"assistant","content":"test"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Commit files - should succeed by removing orphaned .tools.json
		files := []filePair{
			{Tmp: tmpTools, Final: orphanedTools},
			{Tmp: tmpJSON, Final: filepath.Join(tmpDir, "message2.json")},
		}

		// Debug: Log what files we're trying to commit
		t.Logf("Files to commit:")
		for _, f := range files {
			t.Logf("  %s -> %s", f.Tmp, f.Final)
		}

		_, err := commitFiles(context.Background(), files)
		if err != nil {
			t.Errorf("Expected success when overwriting orphaned .tools.json, got error: %v", err)
		}

		// Verify files were committed
		content, err := os.ReadFile(orphanedTools)
		if err != nil || string(content) != `{"tools":["new"]}` {
			t.Error("Failed to overwrite orphaned .tools.json file")
		}
	})

	// Test case 3: .txt/.tools.json with existing .json should fail
	t.Run("ExistingJsonPreventsOverwrite", func(t *testing.T) {
		// Create existing files including .json
		existingTxt := filepath.Join(tmpDir, "message3.txt")
		existingJSON := filepath.Join(tmpDir, "message3.json")

		if err := os.WriteFile(existingTxt, []byte("existing content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(existingJSON, []byte(`{"role":"user","content":"existing"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Create temp file to commit
		tmpTxt := filepath.Join(tmpDir, "message3.txt.tmp")
		if err := os.WriteFile(tmpTxt, []byte("new content"), 0o644); err != nil {
			t.Fatal(err)
		}

		// Commit should fail because .json exists
		files := []filePair{
			{Tmp: tmpTxt, Final: existingTxt},
		}

		_, err := commitFiles(context.Background(), files)
		if err == nil {
			t.Error("Expected error when .json exists, got success")
		}
		// The error should indicate that the destination already exists
	})

	// Test case 4: Interrupted write recovery scenario
	t.Run("InterruptedWriteRecovery", func(t *testing.T) {
		// Simulate interrupted write: .txt and .tools.json exist but no .json
		basePath := filepath.Join(tmpDir, "interrupted")
		orphanedTxt := basePath + ".txt"
		orphanedTools := basePath + ".tools.json"

		if err := os.WriteFile(orphanedTxt, []byte("interrupted content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(orphanedTools, []byte(`{"tools":["interrupted"]}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Create new temp files to commit
		tmpTxt := orphanedTxt + ".tmp"
		tmpTools := orphanedTools + ".tmp"
		tmpJSON := basePath + ".json.tmp"

		if err := os.WriteFile(tmpTxt, []byte("recovered content"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tmpTools, []byte(`{"tools":["recovered"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tmpJSON, []byte(`{"role":"user","content":"recovered"}`), 0o644); err != nil {
			t.Fatal(err)
		}

		// Commit files - should succeed by overwriting orphaned files
		files := []filePair{
			{Tmp: tmpTxt, Final: orphanedTxt},
			{Tmp: tmpTools, Final: orphanedTools},
			{Tmp: tmpJSON, Final: basePath + ".json"},
		}

		_, err := commitFiles(context.Background(), files)
		if err != nil {
			t.Errorf("Expected success in recovery scenario, got error: %v", err)
		}

		// Verify all files were committed
		txtContent, _ := os.ReadFile(orphanedTxt)
		toolsContent, _ := os.ReadFile(orphanedTools)
		jsonContent, _ := os.ReadFile(basePath + ".json")

		if string(txtContent) != "recovered content" {
			t.Error("Failed to recover .txt file")
		}
		if string(toolsContent) != `{"tools":["recovered"]}` {
			t.Error("Failed to recover .tools.json file")
		}
		if string(jsonContent) != `{"role":"user","content":"recovered"}` {
			t.Error("Failed to write .json file")
		}
	})
}

// TestCommitFilesRollbackInterrupted tests that rollback works correctly on failure
func TestCommitFilesRollbackInterrupted(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "rollback-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temp files
	tmp1 := filepath.Join(tmpDir, "file1.tmp")
	tmp2 := filepath.Join(tmpDir, "file2.tmp")
	tmp3 := filepath.Join(tmpDir, "file3.tmp")

	final1 := filepath.Join(tmpDir, "file1")
	final2 := filepath.Join(tmpDir, "file2")
	final3 := filepath.Join(tmpDir, "file3")

	// Write temp files
	if err := os.WriteFile(tmp1, []byte("content1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp2, []byte("content2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp3, []byte("content3"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a conflict on the third file
	if err := os.WriteFile(final3, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := []filePair{
		{Tmp: tmp1, Final: final1},
		{Tmp: tmp2, Final: final2},
		{Tmp: tmp3, Final: final3}, // This will fail
	}

	// Attempt commit - should fail and rollback
	_, err = commitFiles(context.Background(), files)
	if err == nil {
		t.Fatal("Expected error due to existing file")
	}

	// Verify rollback: temp files should be back
	if _, err := os.Stat(tmp1); os.IsNotExist(err) {
		t.Error("Temp file 1 should exist after rollback")
	}
	if _, err := os.Stat(tmp2); os.IsNotExist(err) {
		t.Error("Temp file 2 should exist after rollback")
	}

	// Final files should not exist (except the conflicting one)
	if _, err := os.Stat(final1); !os.IsNotExist(err) {
		t.Error("Final file 1 should not exist after rollback")
	}
	if _, err := os.Stat(final2); !os.IsNotExist(err) {
		t.Error("Final file 2 should not exist after rollback")
	}
}
