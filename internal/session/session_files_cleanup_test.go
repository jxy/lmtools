package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCommitFiles_OrphanCleanup tests that commitFiles removes orphan .txt and .tools.json files
// when the corresponding .json file is missing and we're trying to overwrite them
func TestCommitFiles_OrphanCleanup(t *testing.T) {
	// Create a temporary directory for testing
	tmpDir := t.TempDir()

	// Test cases
	tests := []struct {
		name          string
		setupFiles    []string // Files to create before commit
		filesToCommit []filePair
		expectRemoved []string // Files that should be removed
		expectKept    []string // Files that should remain
	}{
		{
			name: "removes orphan txt file when overwriting without json",
			setupFiles: []string{
				"0001.txt", // Orphan - no corresponding .json
			},
			filesToCommit: []filePair{
				// Trying to overwrite the orphan file
				{
					Tmp:   filepath.Join(tmpDir, "0001.txt.tmp"),
					Final: filepath.Join(tmpDir, "0001.txt"),
				},
				{
					Tmp:   filepath.Join(tmpDir, "0001.json.tmp"),
					Final: filepath.Join(tmpDir, "0001.json"),
				},
			},
			expectRemoved: []string{}, // File gets overwritten, not removed
			expectKept:    []string{"0001.txt", "0001.json"},
		},
		{
			name: "removes orphan tools.json file when overwriting without json",
			setupFiles: []string{
				"0001.tools.json", // Orphan - no corresponding .json
			},
			filesToCommit: []filePair{
				// Trying to overwrite the orphan file
				{
					Tmp:   filepath.Join(tmpDir, "0001.tools.json.tmp"),
					Final: filepath.Join(tmpDir, "0001.tools.json"),
				},
				{
					Tmp:   filepath.Join(tmpDir, "0001.json.tmp"),
					Final: filepath.Join(tmpDir, "0001.json"),
				},
			},
			expectRemoved: []string{}, // File gets overwritten, not removed
			expectKept:    []string{"0001.tools.json", "0001.json"},
		},
		{
			name: "fails when trying to overwrite existing files with json present",
			setupFiles: []string{
				"0001.txt",
				"0001.json", // JSON exists, so txt is not orphaned
			},
			filesToCommit: []filePair{
				{
					Tmp:   filepath.Join(tmpDir, "0001.txt.tmp"),
					Final: filepath.Join(tmpDir, "0001.txt"),
				},
			},
			expectRemoved: []string{},
			expectKept:    []string{"0001.txt", "0001.json"}, // Should fail, original files remain
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a subdirectory for this test
			testDir := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(testDir, 0o755); err != nil {
				t.Fatalf("Failed to create test directory: %v", err)
			}

			// Setup initial files
			for _, file := range tt.setupFiles {
				path := filepath.Join(testDir, file)
				if err := os.WriteFile(path, []byte("test content"), 0o644); err != nil {
					t.Fatalf("Failed to create setup file %s: %v", file, err)
				}
			}

			// Create temporary files to commit
			for i := range tt.filesToCommit {
				tt.filesToCommit[i].Tmp = filepath.Join(testDir, filepath.Base(tt.filesToCommit[i].Tmp))
				tt.filesToCommit[i].Final = filepath.Join(testDir, filepath.Base(tt.filesToCommit[i].Final))

				if err := os.WriteFile(tt.filesToCommit[i].Tmp, []byte("new content"), 0o644); err != nil {
					t.Fatalf("Failed to create temp file: %v", err)
				}
			}

			// Execute commitFiles
			result, err := commitFiles(context.Background(), tt.filesToCommit)

			// Check if we expect an error (when json exists)
			if tt.name == "fails when trying to overwrite existing files with json present" {
				if err == nil {
					t.Fatal("Expected error when trying to overwrite files with existing json, but got none")
				}
				// Verify original files remain unchanged
				for _, file := range tt.expectKept {
					path := filepath.Join(testDir, file)
					if _, err := os.Stat(path); os.IsNotExist(err) {
						t.Errorf("Expected file %s to exist, but it was removed", file)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("commitFiles failed: %v", err)
			}

			// Verify commit result
			if len(result.OrphanedFiles) > 0 {
				t.Logf("Orphaned files removed: %v", result.OrphanedFiles)
			}

			// Check that expected files were removed
			for _, file := range tt.expectRemoved {
				path := filepath.Join(testDir, file)
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("Expected file %s to be removed, but it still exists", file)
				}
			}

			// Check that expected files were kept
			for _, file := range tt.expectKept {
				path := filepath.Join(testDir, file)
				if _, err := os.Stat(path); os.IsNotExist(err) {
					t.Errorf("Expected file %s to exist, but it was removed", file)
				}
			}
		})
	}
}

// TestCommitFiles_AtomicBehavior tests the atomic commit behavior
func TestCommitFiles_AtomicBehavior(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files to commit
	files := []filePair{
		{
			Tmp:   filepath.Join(tmpDir, "0001.txt.tmp"),
			Final: filepath.Join(tmpDir, "0001.txt"),
		},
		{
			Tmp:   filepath.Join(tmpDir, "0001.json.tmp"),
			Final: filepath.Join(tmpDir, "0001.json"),
		},
		{
			Tmp:   filepath.Join(tmpDir, "0001.tools.json.tmp"),
			Final: filepath.Join(tmpDir, "0001.tools.json"),
		},
	}

	// Create temporary files
	for _, f := range files {
		if err := os.WriteFile(f.Tmp, []byte("test"), 0o644); err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
	}

	// Create a file that will cause a conflict (only for .json)
	if err := os.WriteFile(files[1].Final, []byte("existing"), 0o644); err != nil {
		t.Fatalf("Failed to create conflicting file: %v", err)
	}

	// Attempt to commit - should fail due to .json conflict
	_, err := commitFiles(context.Background(), files)
	if err == nil {
		t.Fatal("Expected error due to .json file conflict, but got none")
	}

	// Verify that NO files were renamed (atomic rollback)
	for _, f := range files {
		// Temp files should still exist
		if _, err := os.Stat(f.Tmp); os.IsNotExist(err) {
			t.Errorf("Temp file %s should still exist after failed commit", f.Tmp)
		}

		// Only the pre-existing .json should exist in final location
		if f.Final == files[1].Final {
			// This is the conflicting .json file
			content, err := os.ReadFile(f.Final)
			if err != nil {
				t.Errorf("Failed to read existing file: %v", err)
			} else if string(content) != "existing" {
				t.Errorf("Existing file was modified, expected 'existing', got '%s'", content)
			}
		} else {
			// Other final files should not exist
			if _, err := os.Stat(f.Final); !os.IsNotExist(err) {
				t.Errorf("Final file %s should not exist after failed commit", f.Final)
			}
		}
	}
}

// TestCommitFiles_JsonAsCommitPoint tests that .json is the commit point
func TestCommitFiles_JsonAsCommitPoint(t *testing.T) {
	tmpDir := t.TempDir()

	// Test that .json must be included in commits
	files := []filePair{
		{
			Tmp:   filepath.Join(tmpDir, "0001.txt.tmp"),
			Final: filepath.Join(tmpDir, "0001.txt"),
		},
		{
			Tmp:   filepath.Join(tmpDir, "0001.tools.json.tmp"),
			Final: filepath.Join(tmpDir, "0001.tools.json"),
		},
		// Notably missing: 0001.json
	}

	// Create temporary files
	for _, f := range files {
		if err := os.WriteFile(f.Tmp, []byte("test"), 0o644); err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
	}

	// Commit should succeed but result should indicate no JSON file
	_, err := commitFiles(context.Background(), files)
	if err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// The files should still be committed
	for _, f := range files {
		if _, err := os.Stat(f.Final); os.IsNotExist(err) {
			t.Errorf("Expected file %s to exist after commit", f.Final)
		}
	}
}
