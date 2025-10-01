package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCommitFilesEmptyTxtSkip tests that commitFiles skips empty txt files
func TestCommitFilesEmptyTxtSkip(t *testing.T) {
	tmpDir := t.TempDir()

	// Create temp files
	txtTmp := filepath.Join(tmpDir, "msg.txt.tmp")
	jsonTmp := filepath.Join(tmpDir, "msg.json.tmp")
	txtFinal := filepath.Join(tmpDir, "msg.txt")
	jsonFinal := filepath.Join(tmpDir, "msg.json")

	// Write empty txt and non-empty json
	if err := os.WriteFile(txtTmp, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonTmp, []byte(`{"role":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Call commitFiles
	files := []filePair{
		{Tmp: txtTmp, Final: txtFinal},
		{Tmp: jsonTmp, Final: jsonFinal},
	}
	if _, err := commitFiles(context.Background(), files); err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// Check results
	if fileExists(txtFinal) {
		info, _ := os.Stat(txtFinal)
		t.Errorf("Empty txt file should not be created, but found with size %d", info.Size())
	}
	if !fileExists(jsonFinal) {
		t.Error("json file should exist")
	}
	if !fileExists(txtTmp) {
		t.Error("Empty txt temp file should still exist (not renamed)")
	}
	if fileExists(jsonTmp) {
		t.Error("json temp file should not exist (should be renamed)")
	}
}

// TestCommitFilesAtomicityExtended tests the atomic commit behavior of message files
func TestCommitFilesAtomicityExtended(t *testing.T) {
	tmpDir := t.TempDir()

	// Debug the directory structure
	t.Logf("Using tmpDir: %s", tmpDir)

	tests := []struct {
		name        string
		setup       func() ([]filePair, error)
		wantErr     bool
		errContains string
		verify      func(t *testing.T)
	}{
		{
			name: "successful commit of all three files",
			setup: func() ([]filePair, error) {
				// Create temp files
				txtFile := filepath.Join(tmpDir, "test.txt.tmp")
				toolsFile := filepath.Join(tmpDir, "test.tools.json.tmp")
				jsonFile := filepath.Join(tmpDir, "test.json.tmp")

				if err := os.WriteFile(txtFile, []byte("content"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(toolsFile, []byte(`{"calls":[]}`), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(jsonFile, []byte(`{"role":"user"}`), 0o644); err != nil {
					return nil, err
				}

				return []filePair{
					{Tmp: txtFile, Final: filepath.Join(tmpDir, "0001.txt")},
					{Tmp: toolsFile, Final: filepath.Join(tmpDir, "0001.tools.json")},
					{Tmp: jsonFile, Final: filepath.Join(tmpDir, "0001.json")},
				}, nil
			},
			wantErr: false,
			verify: func(t *testing.T) {
				// All files should exist
				if !fileExists(filepath.Join(tmpDir, "0001.txt")) {
					t.Error("0001.txt should exist")
				}
				if !fileExists(filepath.Join(tmpDir, "0001.tools.json")) {
					t.Error("0001.tools.json should exist")
				}
				if !fileExists(filepath.Join(tmpDir, "0001.json")) {
					t.Error("0001.json should exist")
				}
			},
		},
		{
			name: "orphaned txt and tools.json files are overwritten",
			setup: func() ([]filePair, error) {
				// Create orphaned files (no matching .json)
				orphanedTxt := filepath.Join(tmpDir, "0002.txt")
				orphanedTools := filepath.Join(tmpDir, "0002.tools.json")
				if err := os.WriteFile(orphanedTxt, []byte("old content"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(orphanedTools, []byte(`{"old":"data"}`), 0o644); err != nil {
					return nil, err
				}

				// Create new temp files
				txtFile := filepath.Join(tmpDir, "test2.txt.tmp")
				toolsFile := filepath.Join(tmpDir, "test2.tools.json.tmp")
				jsonFile := filepath.Join(tmpDir, "test2.json.tmp")

				if err := os.WriteFile(txtFile, []byte("new content"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(toolsFile, []byte(`{"new":"data"}`), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(jsonFile, []byte(`{"role":"assistant"}`), 0o644); err != nil {
					return nil, err
				}

				return []filePair{
					{Tmp: txtFile, Final: orphanedTxt},
					{Tmp: toolsFile, Final: orphanedTools},
					{Tmp: jsonFile, Final: filepath.Join(tmpDir, "0002.json")},
				}, nil
			},
			wantErr: false,
			verify: func(t *testing.T) {
				// Check content was overwritten
				content, _ := os.ReadFile(filepath.Join(tmpDir, "0002.txt"))
				if string(content) != "new content" {
					t.Errorf("Expected 'new content', got %s", content)
				}

				toolsContent, _ := os.ReadFile(filepath.Join(tmpDir, "0002.tools.json"))
				if string(toolsContent) != `{"new":"data"}` {
					t.Errorf("Expected new tools data, got %s", toolsContent)
				}
			},
		},
		{
			name: "existing json file blocks commit",
			setup: func() ([]filePair, error) {
				// Create existing committed message (has .json)
				existingJson := filepath.Join(tmpDir, "0003.json")
				if err := os.WriteFile(existingJson, []byte(`{"role":"user"}`), 0o644); err != nil {
					return nil, err
				}

				// Try to overwrite
				jsonFile := filepath.Join(tmpDir, "test3.json.tmp")
				if err := os.WriteFile(jsonFile, []byte(`{"role":"assistant"}`), 0o644); err != nil {
					return nil, err
				}

				return []filePair{
					{Tmp: jsonFile, Final: existingJson},
				}, nil
			},
			wantErr:     true,
			errContains: "destination already exists: 0003.json",
			verify: func(t *testing.T) {
				// Original file should remain unchanged
				content, _ := os.ReadFile(filepath.Join(tmpDir, "0003.json"))
				if string(content) != `{"role":"user"}` {
					t.Error("Original json file should be unchanged")
				}
			},
		},
		{
			name: "rollback on failure preserves atomicity",
			setup: func() ([]filePair, error) {
				// Create temp files
				txtFile := filepath.Join(tmpDir, "test4.txt.tmp")
				jsonFile := filepath.Join(tmpDir, "test4.json.tmp")

				if err := os.WriteFile(txtFile, []byte("content"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(jsonFile, []byte(`{"role":"user"}`), 0o644); err != nil {
					return nil, err
				}

				// Create a conflict for the json file
				conflictJson := filepath.Join(tmpDir, "0004.json")
				if err := os.WriteFile(conflictJson, []byte(`{"existing":"data"}`), 0o644); err != nil {
					return nil, err
				}

				return []filePair{
					{Tmp: txtFile, Final: filepath.Join(tmpDir, "0004.txt")},
					{Tmp: jsonFile, Final: conflictJson}, // This will fail
				}, nil
			},
			wantErr:     true,
			errContains: "destination already exists",
			verify: func(t *testing.T) {
				// The txt file should have been rolled back
				if fileExists(filepath.Join(tmpDir, "0004.txt")) {
					t.Error("0004.txt should have been rolled back")
				}
				// Temp file should still exist
				if !fileExists(filepath.Join(tmpDir, "test4.txt.tmp")) {
					t.Error("Temp file should still exist after rollback")
				}
			},
		},
		{
			name: "empty txt file is skipped",
			setup: func() ([]filePair, error) {
				// Create empty txt file (should be skipped)
				txtFile := filepath.Join(tmpDir, "test5.txt.tmp")
				jsonFile := filepath.Join(tmpDir, "test5.json.tmp")

				if err := os.WriteFile(txtFile, []byte(""), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(jsonFile, []byte(`{"role":"user"}`), 0o644); err != nil {
					return nil, err
				}

				// Ensure no pre-existing files
				os.Remove(filepath.Join(tmpDir, "0005.txt"))
				os.Remove(filepath.Join(tmpDir, "0005.json"))

				return []filePair{
					{Tmp: txtFile, Final: filepath.Join(tmpDir, "0005.txt")},
					{Tmp: jsonFile, Final: filepath.Join(tmpDir, "0005.json")},
				}, nil
			},
			wantErr: false,
			verify: func(t *testing.T) {
				// Only json should exist, txt should be skipped
				txtPath := filepath.Join(tmpDir, "0005.txt")
				jsonPath := filepath.Join(tmpDir, "0005.json")
				txtTmpPath := filepath.Join(tmpDir, "test5.txt.tmp")

				// Debug: check what files exist
				files, _ := filepath.Glob(filepath.Join(tmpDir, "*"))
				t.Logf("All files in dir: %v", files)

				// Check if temp file still exists (should not be renamed)
				if !fileExists(txtTmpPath) {
					t.Error("Empty txt temp file should still exist (not renamed)")
				}

				if fileExists(txtPath) {
					// Check file size
					info, _ := os.Stat(txtPath)
					t.Errorf("Empty txt file should not be created, but found with size %d", info.Size())
				}
				if !fileExists(jsonPath) {
					t.Error("0005.json should exist")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := tt.setup()
			if err != nil {
				t.Fatalf("Setup failed: %v", err)
			}

			_, err = commitFiles(context.Background(), files)
			if (err != nil) != tt.wantErr {
				t.Errorf("commitFiles() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err != nil && tt.errContains != "" && !containsString(err.Error(), tt.errContains) {
				t.Errorf("Error should contain %q, got %q", tt.errContains, err.Error())
			}

			if tt.verify != nil {
				tt.verify(t)
			}
		})
	}
}

// TestListMessagesOnlyJsonAuthoritative tests that ListMessages only considers .json files
func TestListMessagesOnlyJsonAuthoritative(t *testing.T) {
	tmpDir := t.TempDir()

	// Create various files
	files := []struct {
		name    string
		content string
	}{
		{"0001.json", `{"role":"user"}`},
		{"0001.txt", "content 1"},
		{"0002.txt", "orphaned content"}, // No matching .json
		{"0003.json", `{"role":"assistant"}`},
		{"0003.tools.json", `{"calls":[]}`},
		{"0004.tools.json", `{"orphaned":"tools"}`}, // No matching .json
		{"0005.json", `{"role":"user"}`},
		// 0005 has no .txt file (tool-only message)
	}

	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, f.name), []byte(f.content), 0o644); err != nil {
			t.Fatalf("Failed to create %s: %v", f.name, err)
		}
	}

	// List messages
	messages, err := listMessages(tmpDir)
	if err != nil {
		t.Fatalf("ListMessages failed: %v", err)
	}

	// Should only return IDs where .json exists
	expected := []string{"0001", "0003", "0005"}
	if len(messages) != len(expected) {
		t.Errorf("Expected %d messages, got %d", len(expected), len(messages))
	}

	for i, id := range expected {
		if i >= len(messages) || messages[i] != id {
			t.Errorf("Expected message %d to be %s, got %v", i, id, messages[i])
		}
	}
}

// TestReadMessageWithoutTxt tests reading messages that have no .txt file
func TestReadMessageWithoutTxt(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a message with only .json (no .txt)
	metadata := MessageMetadata{
		Role:      "assistant",
		Timestamp: time.Now(),
	}
	metaBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "0001.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("Failed to create json file: %v", err)
	}

	// Read the message
	msg, err := readMessage(tmpDir, "0001")
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	// Should succeed with empty content
	if msg.Content != "" {
		t.Errorf("Expected empty content, got %q", msg.Content)
	}
	if msg.Role != "assistant" {
		t.Errorf("Expected role 'assistant', got %q", msg.Role)
	}
}

// TestStageAndCommitMessage tests the full staging and commit flow
func TestStageAndCommitMessage(t *testing.T) {
	tmpDir := t.TempDir()

	msg := Message{
		ID:        "0001",
		Role:      "user",
		Content:   "Test message",
		Timestamp: time.Now(),
	}

	// Stage files
	staging, err := stageMessageFiles(tmpDir, msg, nil)
	if err != nil {
		t.Fatalf("stageMessageFiles failed: %v", err)
	}
	defer staging.Close()

	// Prepare commit
	files := []filePair{
		{Tmp: staging.TxtPath, Final: filepath.Join(tmpDir, "0001.txt")},
		{Tmp: staging.JsonPath, Final: filepath.Join(tmpDir, "0001.json")},
	}

	// Commit
	if _, err := commitFiles(context.Background(), files); err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// Verify message can be read back
	readMsg, err := readMessage(tmpDir, "0001")
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if readMsg.Content != msg.Content {
		t.Errorf("Expected content %q, got %q", msg.Content, readMsg.Content)
	}
	if readMsg.Role != msg.Role {
		t.Errorf("Expected role %q, got %q", msg.Role, readMsg.Role)
	}
}

// TestMessageStagingCleanup tests that staging cleanup works correctly
func TestMessageStagingCleanup(t *testing.T) {
	tmpDir := t.TempDir()

	msg := Message{
		ID:      "0001",
		Role:    "user",
		Content: "Test",
	}

	staging, err := stageMessageFiles(tmpDir, msg, nil)
	if err != nil {
		t.Fatalf("stageMessageFiles failed: %v", err)
	}

	// Verify files exist
	if !fileExists(staging.TxtPath) {
		t.Error("Staged txt file should exist")
	}
	if !fileExists(staging.JsonPath) {
		t.Error("Staged json file should exist")
	}

	// Clean up
	staging.Close()

	// Verify files are removed
	if fileExists(staging.TxtPath) {
		t.Error("Staged txt file should be removed")
	}
	if fileExists(staging.JsonPath) {
		t.Error("Staged json file should be removed")
	}
}

// containsString is a helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && strings.Contains(s, substr))
}
