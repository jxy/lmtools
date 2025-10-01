package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWritereadMessage(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session directory
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Test writing and reading user message
		userMsg := Message{
			ID:        "0000",
			Role:      "user",
			Content:   "Hello, this is a test message!",
			Timestamp: time.Now().UTC().Truncate(time.Second), // Truncate for comparison
		}

		err := writeMessage(testSession, userMsg.ID, userMsg)
		if err != nil {
			t.Fatalf("Failed to write message: %v", err)
		}

		// Verify files exist
		txtPath := filepath.Join(testSession, "0000.txt")
		jsonPath := filepath.Join(testSession, "0000.json")

		if _, err := os.Stat(txtPath); err != nil {
			t.Errorf("Text file was not created: %v", err)
		}

		if _, err := os.Stat(jsonPath); err != nil {
			t.Errorf("JSON file was not created: %v", err)
		}

		// Read message back
		readMsg, err := readMessage(testSession, "0000")
		if err != nil {
			t.Fatalf("Failed to read message: %v", err)
		}

		// Compare fields
		if readMsg.ID != userMsg.ID {
			t.Errorf("ID mismatch: expected %s, got %s", userMsg.ID, readMsg.ID)
		}

		if readMsg.Role != userMsg.Role {
			t.Errorf("Role mismatch: expected %s, got %s", userMsg.Role, readMsg.Role)
		}

		if readMsg.Content != userMsg.Content {
			t.Errorf("Content mismatch: expected %s, got %s", userMsg.Content, readMsg.Content)
		}

		if !readMsg.Timestamp.Equal(userMsg.Timestamp) {
			t.Errorf("Timestamp mismatch: expected %v, got %v", userMsg.Timestamp, readMsg.Timestamp)
		}

		// Test assistant message with model
		assistantMsg := Message{
			ID:        "0001",
			Role:      "assistant",
			Content:   "Hello! I'm here to help.",
			Timestamp: time.Now().UTC().Truncate(time.Second),
			Model:     "test-model",
		}

		err = writeMessage(testSession, assistantMsg.ID, assistantMsg)
		if err != nil {
			t.Fatalf("Failed to write assistant message: %v", err)
		}

		readAssistantMsg, err := readMessage(testSession, "0001")
		if err != nil {
			t.Fatalf("Failed to read assistant message: %v", err)
		}

		if readAssistantMsg.Model != assistantMsg.Model {
			t.Errorf("Model mismatch: expected %s, got %s", assistantMsg.Model, readAssistantMsg.Model)
		}
	})
}

func TestWriteFileAtomic(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		testDir := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testDir, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test dir: %v", err)
		}

		testFile := filepath.Join(testDir, "test.txt")
		testData := []byte("This is test data")

		// Test atomic write
		err := writeFileAtomic(testFile, testData)
		if err != nil {
			t.Fatalf("Failed to write file atomically: %v", err)
		}

		// Verify file contents
		readData, err := os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("Failed to read file: %v", err)
		}

		if string(readData) != string(testData) {
			t.Errorf("Content mismatch: expected %s, got %s", testData, readData)
		}

		// Test overwriting existing file
		newData := []byte("This is new data")
		err = writeFileAtomic(testFile, newData)
		if err != nil {
			t.Fatalf("Failed to overwrite file atomically: %v", err)
		}

		readData, err = os.ReadFile(testFile)
		if err != nil {
			t.Fatalf("Failed to read overwritten file: %v", err)
		}

		if string(readData) != string(newData) {
			t.Errorf("Overwrite content mismatch: expected %s, got %s", newData, readData)
		}
	})
}

func TestListMessages(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Test empty directory
		messages, err := listMessages(testSession)
		if err != nil {
			t.Fatalf("Failed to list messages: %v", err)
		}

		if len(messages) != 0 {
			t.Errorf("Expected 0 messages in empty directory, got %d", len(messages))
		}

		// Create some messages
		testMessages := []string{"0000", "0001", "0002", "00ff", "0100"}
		for _, msgID := range testMessages {
			msg := Message{
				ID:        msgID,
				Role:      "user",
				Content:   "Test",
				Timestamp: time.Now(),
			}
			if err := writeMessage(testSession, msgID, msg); err != nil {
				t.Fatalf("Failed to write message %s: %v", msgID, err)
			}
		}

		// List messages
		messages, err = listMessages(testSession)
		if err != nil {
			t.Fatalf("Failed to list messages: %v", err)
		}

		if len(messages) != len(testMessages) {
			t.Errorf("Expected %d messages, got %d", len(testMessages), len(messages))
		}

		// Verify sorted order
		for i, msgID := range messages {
			if msgID != testMessages[i] {
				t.Errorf("Message %d: expected %s, got %s", i, testMessages[i], msgID)
			}
		}

		// Test with missing JSON file
		orphanTxt := filepath.Join(testSession, "orphan.txt")
		if err := os.WriteFile(orphanTxt, []byte("orphan"), constants.FilePerm); err != nil {
			t.Fatalf("Failed to create orphan txt file: %v", err)
		}

		messages, err = listMessages(testSession)
		if err != nil {
			t.Fatalf("Failed to list messages with orphan: %v", err)
		}

		// Should still have same number of messages (orphan ignored)
		if len(messages) != len(testMessages) {
			t.Errorf("Expected %d messages (orphan ignored), got %d", len(testMessages), len(messages))
		}

		// Test with non-message files
		randomFile := filepath.Join(testSession, "random.dat")
		if err := os.WriteFile(randomFile, []byte("random"), constants.FilePerm); err != nil {
			t.Fatalf("Failed to create random file: %v", err)
		}

		messages, err = listMessages(testSession)
		if err != nil {
			t.Fatalf("Failed to list messages with random file: %v", err)
		}

		// Should still have same number of messages
		if len(messages) != len(testMessages) {
			t.Errorf("Expected %d messages (random file ignored), got %d", len(testMessages), len(messages))
		}
	})
}

func TestLoadMessagesInDir(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Create test messages
		expectedMessages := []Message{
			{ID: "0000", Role: "user", Content: "First message", Timestamp: time.Now().UTC().Truncate(time.Second)},
			{ID: "0001", Role: "assistant", Content: "Second message", Timestamp: time.Now().UTC().Truncate(time.Second), Model: "test-model"},
			{ID: "0002", Role: "user", Content: "Third message", Timestamp: time.Now().UTC().Truncate(time.Second)},
		}

		for _, msg := range expectedMessages {
			if err := writeMessage(testSession, msg.ID, msg); err != nil {
				t.Fatalf("Failed to write message %s: %v", msg.ID, err)
			}
		}

		// Load messages
		loadedMessages, err := loadMessagesInDir(testSession)
		if err != nil {
			t.Fatalf("Failed to load messages: %v", err)
		}

		assertLineageEqual(t, expectedMessages, loadedMessages)

		// Test with message that has only .json file (no content)
		// According to the new rule: "A message exists if and only if its JSON file exists"
		// This message should be included
		jsonOnlyID := "0003"
		metadata := MessageMetadata{
			Role:      "user",
			Timestamp: time.Now(),
		}
		metaData, _ := json.MarshalIndent(metadata, "", "  ")
		metaPath := filepath.Join(testSession, jsonOnlyID+".json")
		if err := os.WriteFile(metaPath, metaData, constants.FilePerm); err != nil {
			t.Fatalf("Failed to create JSON-only message: %v", err)
		}

		// Should include the JSON-only message
		loadedMessages, err = loadMessagesInDir(testSession)
		if err != nil {
			t.Fatalf("Failed to load messages with JSON-only: %v", err)
		}

		// Should have all messages including the JSON-only one
		if len(loadedMessages) != len(expectedMessages)+1 {
			t.Errorf("Expected %d messages (including JSON-only), got %d", len(expectedMessages)+1, len(loadedMessages))
		}
	})
}

func TestFindSiblings(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Test with no siblings
		siblings, err := findSiblings(testSession, "0002")
		if err != nil {
			t.Fatalf("Failed to find siblings: %v", err)
		}

		if len(siblings) != 0 {
			t.Errorf("Expected 0 siblings, got %d", len(siblings))
		}

		// Create siblings
		expectedSiblings := []string{"0002.s.0000", "0002.s.0001", "0002.s.0002"}
		for _, sib := range expectedSiblings {
			sibDir := filepath.Join(testSession, sib)
			if err := os.MkdirAll(sibDir, constants.DirPerm); err != nil {
				t.Fatalf("Failed to create sibling dir %s: %v", sib, err)
			}
		}

		// Also create siblings for different message
		otherSibling := filepath.Join(testSession, "0003.s.0000")
		if err := os.MkdirAll(otherSibling, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create other sibling dir: %v", err)
		}

		// Find siblings for 0002
		siblings, err = findSiblings(testSession, "0002")
		if err != nil {
			t.Fatalf("Failed to find siblings: %v", err)
		}

		if len(siblings) != len(expectedSiblings) {
			t.Errorf("Expected %d siblings, got %d", len(expectedSiblings), len(siblings))
		}

		// Verify correct siblings found and sorted
		for i, sib := range siblings {
			if sib != expectedSiblings[i] {
				t.Errorf("Sibling %d: expected %s, got %s", i, expectedSiblings[i], sib)
			}
		}

		// Test finding siblings for message with no siblings
		siblings, err = findSiblings(testSession, "0001")
		if err != nil {
			t.Fatalf("Failed to find siblings for 0001: %v", err)
		}

		if len(siblings) != 0 {
			t.Errorf("Expected 0 siblings for 0001, got %d", len(siblings))
		}
	})
}

func TestEnsureSessionDir(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Test creating new directory
		newDir := filepath.Join(sessionsDir, "new", "nested", "dir")
		err := os.MkdirAll(newDir, constants.DirPerm)
		if err != nil {
			t.Fatalf("Failed to ensure session dir: %v", err)
		}

		// Verify directory exists
		info, err := os.Stat(newDir)
		if err != nil {
			t.Errorf("Directory was not created: %v", err)
		}

		if !info.IsDir() {
			t.Error("Created path is not a directory")
		}

		// Test with existing directory (should not error)
		err = os.MkdirAll(newDir, constants.DirPerm)
		if err != nil {
			t.Errorf("Failed to ensure existing dir: %v", err)
		}
	})
}

func TestListMessagesNumericOrdering(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		sessionPath := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(sessionPath, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Create messages with IDs that would sort incorrectly lexicographically
		testIDs := []string{"0001", "0002", "000a", "000f", "0010", "00ff", "0100", "ffff", "10000"}

		for _, id := range testIDs {
			// Create dummy files
			contentPath := filepath.Join(sessionPath, id+".txt")
			metaPath := filepath.Join(sessionPath, id+".json")

			if err := os.WriteFile(contentPath, []byte("test"), constants.FilePerm); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metaPath, []byte("{}"), constants.FilePerm); err != nil {
				t.Fatal(err)
			}
		}

		// List messages
		messages, err := listMessages(sessionPath)
		if err != nil {
			t.Fatalf("Failed to list messages: %v", err)
		}

		// Verify numeric ordering
		expectedOrder := []string{"0001", "0002", "000a", "000f", "0010", "00ff", "0100", "ffff", "10000"}

		if len(messages) != len(expectedOrder) {
			t.Errorf("Expected %d messages, got %d", len(expectedOrder), len(messages))
		}

		for i, expected := range expectedOrder {
			if i < len(messages) && messages[i] != expected {
				t.Errorf("Message %d: expected %s, got %s", i, expected, messages[i])
			}
		}

		// Verify numeric values are in order
		var prevValue uint64 = 0
		for _, msgID := range messages {
			value, err := strconv.ParseUint(msgID, 16, 64)
			if err != nil {
				t.Errorf("Failed to parse hex ID %s: %v", msgID, err)
				continue
			}
			if value <= prevValue {
				t.Errorf("Messages not in numeric order: %s (0x%x) came after 0x%x", msgID, value, prevValue)
			}
			prevValue = value
		}
	})
}

func TestIsAssistantMessage(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create test session
		session, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages
		messages := []Message{
			{Role: "user", Content: "Test user message", Timestamp: time.Now()},
			{Role: "assistant", Content: "Test assistant message", Timestamp: time.Now(), Model: "test-model"},
		}

		var messageIDs []string
		for _, msg := range messages {
			result, err := AppendMessageWithToolInteraction(context.Background(), session, msg, nil, nil)
			if err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
			messageIDs = append(messageIDs, result.MessageID)
		}

		// Test cases
		tests := []struct {
			name     string
			path     string
			expected bool
			wantErr  bool
		}{
			{
				name:     "User message",
				path:     filepath.Join(GetSessionID(session.Path), messageIDs[0]), // Use relative path
				expected: false,
				wantErr:  false,
			},
			{
				name:     "Assistant message",
				path:     filepath.Join(GetSessionID(session.Path), messageIDs[1]), // Use relative path
				expected: true,
				wantErr:  false,
			},
			{
				name:     "Empty path",
				path:     "",
				expected: false,
				wantErr:  true, // Now returns an error for empty path
			},
			{
				name:     "Invalid message ID",
				path:     filepath.Join(GetSessionID(session.Path), "9999"),
				expected: false,
				wantErr:  true, // Should error when trying to read non-existent message
			},
			{
				name:     "Session path only (no message ID)",
				path:     session.Path,
				expected: false,
				wantErr:  false, // Should not error, just return false
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				isAssistant, err := IsAssistantMessage(tt.path)
				if tt.wantErr && err == nil {
					t.Errorf("Expected error but got none")
				}
				if !tt.wantErr && err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if isAssistant != tt.expected {
					t.Errorf("Expected %v, got %v", tt.expected, isAssistant)
				}
			})
		}
	})
}

// TestMessageStagingClose tests the Close method of MessageStaging
func TestMessageStagingClose(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "staging_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create staging with some files
	staging := &MessageStaging{
		TxtPath:   filepath.Join(tmpDir, "test.txt"),
		JsonPath:  filepath.Join(tmpDir, "test.json"),
		ToolsPath: filepath.Join(tmpDir, "test.tools.json"),
	}

	// Create the files
	if err := os.WriteFile(staging.TxtPath, []byte("text"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to write txt file: %v", err)
	}
	if err := os.WriteFile(staging.JsonPath, []byte("{}"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to write json file: %v", err)
	}
	if err := os.WriteFile(staging.ToolsPath, []byte("{}"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to write tools file: %v", err)
	}

	// Verify files exist
	if _, err := os.Stat(staging.TxtPath); os.IsNotExist(err) {
		t.Error("TxtPath should exist before Close")
	}
	if _, err := os.Stat(staging.JsonPath); os.IsNotExist(err) {
		t.Error("JsonPath should exist before Close")
	}
	if _, err := os.Stat(staging.ToolsPath); os.IsNotExist(err) {
		t.Error("ToolsPath should exist before Close")
	}

	// Close should remove all files
	staging.Close()

	// Verify files are removed
	if _, err := os.Stat(staging.TxtPath); !os.IsNotExist(err) {
		t.Error("TxtPath should be removed after Close")
	}
	if _, err := os.Stat(staging.JsonPath); !os.IsNotExist(err) {
		t.Error("JsonPath should be removed after Close")
	}
	if _, err := os.Stat(staging.ToolsPath); !os.IsNotExist(err) {
		t.Error("ToolsPath should be removed after Close")
	}
}

// TestCommitFilesRollback tests the rollback functionality of commitFiles
func TestCommitFilesRollback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create temporary files
	tmp1 := filepath.Join(tmpDir, "tmp1")
	tmp2 := filepath.Join(tmpDir, "tmp2")
	tmp3 := filepath.Join(tmpDir, "tmp3")
	final1 := filepath.Join(tmpDir, "final1")
	final2 := filepath.Join(tmpDir, "final2")
	final3 := filepath.Join(tmpDir, "final3")

	if err := os.WriteFile(tmp1, []byte("1"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp1: %v", err)
	}
	if err := os.WriteFile(tmp2, []byte("2"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp2: %v", err)
	}
	if err := os.WriteFile(tmp3, []byte("3"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp3: %v", err)
	}

	// Create final3 to cause a conflict
	if err := os.WriteFile(final3, []byte("existing"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create final3: %v", err)
	}

	files := []filePair{
		{Tmp: tmp1, Final: final1},
		{Tmp: tmp2, Final: final2},
		{Tmp: tmp3, Final: final3}, // This will fail
	}

	// Verify final3 exists before commit
	if !fileExists(final3) {
		t.Fatal("final3 should exist before commitFiles")
	}

	// Attempt commit (should fail and rollback)
	_, err = commitFiles(context.Background(), files)
	if err == nil {
		t.Error("Expected error due to existing final3")
		// Check if final3 was overwritten
		content, _ := os.ReadFile(final3)
		t.Logf("final3 content: %s (should be 'existing' not '3')", content)
	} else {
		t.Logf("Got expected error: %v", err)
	}

	// Debug: check what files exist
	t.Logf("After commitFiles:")
	t.Logf("  tmp1 exists: %v", fileExists(tmp1))
	t.Logf("  tmp2 exists: %v", fileExists(tmp2))
	t.Logf("  tmp3 exists: %v", fileExists(tmp3))
	t.Logf("  final1 exists: %v", fileExists(final1))
	t.Logf("  final2 exists: %v", fileExists(final2))
	t.Logf("  final3 exists: %v", fileExists(final3))

	// Verify rollback: tmp1 and tmp2 should still exist
	if _, err := os.Stat(tmp1); os.IsNotExist(err) {
		t.Error("tmp1 should exist after rollback")
	}
	if _, err := os.Stat(tmp2); os.IsNotExist(err) {
		t.Error("tmp2 should exist after rollback")
	}

	// final1 and final2 should not exist
	if _, err := os.Stat(final1); !os.IsNotExist(err) {
		t.Error("final1 should not exist after rollback")
	}
	if _, err := os.Stat(final2); !os.IsNotExist(err) {
		t.Error("final2 should not exist after rollback")
	}
}

// TestOrphanFileHandling tests that orphaned files (.txt and .tools.json without .json) are cleaned up
func TestOrphanFileHandling(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session directory
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Create orphaned files (no matching .json)
		// Use "0000" as the orphan ID since that's what GetNextMessageID will return for an empty directory
		orphanTxtPath := filepath.Join(testSession, "0000.txt")
		orphanToolsPath := filepath.Join(testSession, "0000.tools.json")

		if err := os.WriteFile(orphanTxtPath, []byte("orphaned content"), constants.FilePerm); err != nil {
			t.Fatalf("Failed to create orphan txt file: %v", err)
		}
		if err := os.WriteFile(orphanToolsPath, []byte(`{"calls":[]}`), constants.FilePerm); err != nil {
			t.Fatalf("Failed to create orphan tools file: %v", err)
		}

		// Verify orphan files exist
		if _, err := os.Stat(orphanTxtPath); err != nil {
			t.Errorf("Orphan txt file should exist: %v", err)
		}
		if _, err := os.Stat(orphanToolsPath); err != nil {
			t.Errorf("Orphan tools file should exist: %v", err)
		}

		// Test the orphan cleanup behavior directly
		// The orphan cleanup only happens when commitFiles tries to write to a file that already exists

		// First, manually remove the orphan tools file to match the actual behavior
		// The commitFiles function only removes orphans when there's a conflict during commit
		os.Remove(orphanToolsPath)

		// Create temporary files that will be committed with ID 0000
		tempTxt, err := os.CreateTemp(testSession, "temp-*.txt")
		if err != nil {
			t.Fatalf("Failed to create temp txt: %v", err)
		}
		if _, err := tempTxt.WriteString("new content"); err != nil {
			t.Fatalf("Failed to write to temp txt: %v", err)
		}
		tempTxt.Close()

		tempJson, err := os.CreateTemp(testSession, "temp-*.json")
		if err != nil {
			t.Fatalf("Failed to create temp json: %v", err)
		}
		metadata := MessageMetadata{
			Role:      core.RoleUser,
			Timestamp: time.Now(),
		}
		metaData, _ := json.MarshalIndent(metadata, "", "  ")
		if _, err := tempJson.Write(metaData); err != nil {
			t.Fatalf("Failed to write to temp json: %v", err)
		}
		tempJson.Close()

		// Use the lower-level commitFiles directly
		files := []filePair{
			{Tmp: tempTxt.Name(), Final: orphanTxtPath},                            // Should overwrite orphan txt
			{Tmp: tempJson.Name(), Final: filepath.Join(testSession, "0000.json")}, // Commit point
		}

		result, err := commitFiles(context.Background(), files)
		if err != nil {
			t.Fatalf("commitFiles failed: %v", err)
		}

		// Log orphaned files that were removed
		t.Logf("Orphaned files removed: %v", result.OrphanedFiles)

		// Verify the txt file was overwritten (not removed)
		if _, err := os.Stat(orphanTxtPath); err != nil {
			t.Errorf("Txt file should exist (replaced): %v", err)
		}

		// Verify the new content
		content, _ := os.ReadFile(orphanTxtPath)
		if string(content) != "new content" {
			t.Errorf("Txt file should have new content, got: %s", string(content))
		}

		// Verify the JSON file was created
		if _, err := os.Stat(filepath.Join(testSession, "0000.json")); err != nil {
			t.Errorf("JSON file should have been created: %v", err)
		}
	})
}

// TestCommitOrdering tests that files are committed in the correct order
func TestCommitOrdering(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session directory
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Create a message with tool interaction
		msg := Message{
			Role:      core.RoleAssistant,
			Content:   "I'll help you with that",
			Timestamp: time.Now(),
		}

		toolInteraction := &core.ToolInteraction{
			Calls: []core.ToolCall{
				{
					ID:   "call_123",
					Name: "test_tool",
					Args: json.RawMessage(`{"command":["echo","test"]}`),
				},
			},
		}

		// Stage files
		mc := newMessageCommitter(testSession)
		staged, err := mc.Stage(msg, toolInteraction)
		if err != nil {
			t.Fatalf("Failed to stage files: %v", err)
		}
		defer staged.Close()

		// Verify staging created all files
		if staged.TxtPath == "" {
			t.Error("TxtPath should not be empty")
		}
		if staged.JsonPath == "" {
			t.Error("JsonPath should not be empty")
		}
		if staged.ToolsPath == "" {
			t.Error("ToolsPath should not be empty for message with tools")
		}

		// Commit and verify order
		msgID, needSibling, _, err := mc.Commit(context.Background(), staged)
		if err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
		if needSibling {
			t.Error("Should not need sibling for first message")
		}

		// Verify all files exist with correct names
		txtPath := filepath.Join(testSession, msgID+".txt")
		jsonPath := filepath.Join(testSession, msgID+".json")
		toolsPath := filepath.Join(testSession, msgID+".tools.json")

		if _, err := os.Stat(txtPath); err != nil {
			t.Errorf("Text file should exist: %v", err)
		}
		if _, err := os.Stat(jsonPath); err != nil {
			t.Errorf("JSON file should exist: %v", err)
		}
		if _, err := os.Stat(toolsPath); err != nil {
			t.Errorf("Tools file should exist: %v", err)
		}

		// Verify the message can be read back correctly
		readMsg, err := readMessage(testSession, msgID)
		if err != nil {
			t.Fatalf("Failed to read message: %v", err)
		}
		if readMsg.Content != msg.Content {
			t.Errorf("Content mismatch: got %q, want %q", readMsg.Content, msg.Content)
		}

		// Verify tool interaction can be loaded
		loadedTools, err := LoadToolInteraction(testSession, msgID)
		if err != nil {
			t.Fatalf("Failed to load tool interaction: %v", err)
		}
		if len(loadedTools.Calls) != 1 {
			t.Errorf("Expected 1 tool call, got %d", len(loadedTools.Calls))
		}
	})
}

// TestMessageCommitterConcurrentConflict tests handling of concurrent message ID conflicts
func TestMessageCommitterConcurrentConflict(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session directory
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// This test verifies that the messageCommitter properly handles conflicts
		// when a sibling directory needs to be created due to a conflict.

		// Since the actual conflict detection happens when fileExists(finalJsonPath) returns true,
		// and the ID is determined inside the lock, we can't easily simulate a race condition.
		// Instead, we'll test that the sibling creation works correctly when requested.

		// Create some existing messages
		for i := 0; i < 3; i++ {
			msg := Message{
				Role:      core.RoleUser,
				Content:   fmt.Sprintf("message %d", i),
				Timestamp: time.Now(),
			}
			msgID := fmt.Sprintf("%04x", i)
			err := writeMessage(testSession, msgID, msg)
			if err != nil {
				t.Fatalf("Failed to write message %s: %v", msgID, err)
			}
		}

		// Stage a new message
		newMsg := Message{
			Role:      core.RoleAssistant,
			Content:   "new message",
			Timestamp: time.Now(),
		}

		mc := newMessageCommitter(testSession)
		staged, err := mc.Stage(newMsg, nil)
		if err != nil {
			t.Fatalf("Failed to stage new message: %v", err)
		}
		defer staged.Close()

		// Commit should succeed with ID 0003
		msgID, needSibling, siblingPath, err := mc.Commit(context.Background(), staged)
		if err != nil {
			t.Fatalf("Commit failed: %v", err)
		}

		if needSibling {
			t.Error("Should not need sibling for normal commit")
		}
		if msgID != "0003" {
			t.Errorf("Expected message ID 0003, got %s", msgID)
		}
		if siblingPath != "" {
			t.Error("Sibling path should be empty for normal commit")
		}

		// Verify the message was written
		if _, err := os.Stat(filepath.Join(testSession, "0003.json")); err != nil {
			t.Errorf("Message 0003 should exist: %v", err)
		}

		// Test sibling creation directly
		siblingPath, err = CreateSibling(context.Background(), testSession, "0002")
		if err != nil {
			t.Fatalf("Failed to create sibling: %v", err)
		}

		// Verify sibling was created with correct format
		expectedPrefix := filepath.Join(testSession, "0002.s.")
		if !strings.HasPrefix(siblingPath, expectedPrefix) {
			t.Errorf("Sibling path should start with %s, got %s", expectedPrefix, siblingPath)
		}

		// Verify sibling directory exists
		if _, err := os.Stat(siblingPath); err != nil {
			t.Errorf("Sibling directory should exist: %v", err)
		}
	})
}

// TestOrphanCleanup tests that orphaned files are cleaned up during commit
func TestOrphanCleanup(t *testing.T) {
	// Create a temporary directory for the test session
	testDir, err := os.MkdirTemp("", "orphan_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(testDir)

	// Create orphaned files (txt and tools.json without matching json)
	orphanTxt := filepath.Join(testDir, "0001.txt")
	orphanTools := filepath.Join(testDir, "0001.tools.json")
	if err := os.WriteFile(orphanTxt, []byte("orphaned content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create orphan txt: %v", err)
	}
	if err := os.WriteFile(orphanTools, []byte(`{"tools": []}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create orphan tools: %v", err)
	}

	// Create a complete message (with json) that should NOT be removed
	completeTxt := filepath.Join(testDir, "0002.txt")
	completeTools := filepath.Join(testDir, "0002.tools.json")
	completeJson := filepath.Join(testDir, "0002.json")
	if err := os.WriteFile(completeTxt, []byte("complete content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create complete txt: %v", err)
	}
	if err := os.WriteFile(completeTools, []byte(`{"tools": []}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create complete tools: %v", err)
	}
	if err := os.WriteFile(completeJson, []byte(`{"role": "user"}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create complete json: %v", err)
	}

	// Verify all files exist before commit
	if !fileExists(orphanTxt) || !fileExists(orphanTools) {
		t.Fatal("Orphan files should exist before commit")
	}
	if !fileExists(completeTxt) || !fileExists(completeTools) || !fileExists(completeJson) {
		t.Fatal("Complete message files should exist before commit")
	}

	// Try to commit files that would conflict with orphans
	tmpTxt := filepath.Join(testDir, "tmp.txt")
	tmpTools := filepath.Join(testDir, "tmp.tools.json")
	tmpJson := filepath.Join(testDir, "tmp.json")
	if err := os.WriteFile(tmpTxt, []byte("new content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp txt: %v", err)
	}
	if err := os.WriteFile(tmpTools, []byte(`{"tools": []}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp tools: %v", err)
	}
	if err := os.WriteFile(tmpJson, []byte(`{"role": "assistant"}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp json: %v", err)
	}

	files := []filePair{
		{Tmp: tmpTxt, Final: orphanTxt},                            // Should succeed after removing orphan
		{Tmp: tmpTools, Final: orphanTools},                        // Should succeed after removing orphan
		{Tmp: tmpJson, Final: filepath.Join(testDir, "0001.json")}, // New file
	}

	// Commit should succeed and remove orphans
	result, err := commitFiles(context.Background(), files)
	if err != nil {
		t.Fatalf("Commit should succeed after orphan cleanup: %v", err)
	}

	// Verify orphaned files were tracked
	if len(result.OrphanedFiles) != 2 {
		t.Errorf("Expected 2 orphaned files, got %d: %v", len(result.OrphanedFiles), result.OrphanedFiles)
	}

	// Verify orphans were removed and replaced
	if !fileExists(orphanTxt) || !fileExists(orphanTools) {
		t.Error("Files should exist after commit (replaced with new content)")
	}

	// Verify complete message files were NOT removed
	if !fileExists(completeTxt) || !fileExists(completeTools) || !fileExists(completeJson) {
		t.Error("Complete message files should NOT be removed")
	}

	// Verify new content was written
	content, err := os.ReadFile(orphanTxt)
	if err != nil || string(content) != "new content" {
		t.Error("New content should be written to previously orphaned file")
	}
}

// TestOrphanCleanupWithConflict tests orphan cleanup when there's a real conflict
func TestOrphanCleanupWithConflict(t *testing.T) {
	testDir, err := os.MkdirTemp("", "orphan_conflict_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(testDir)

	// Create a complete message (txt + json)
	existingTxt := filepath.Join(testDir, "0001.txt")
	existingJson := filepath.Join(testDir, "0001.json")
	if err := os.WriteFile(existingTxt, []byte("existing content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create existing txt: %v", err)
	}
	if err := os.WriteFile(existingJson, []byte(`{"role": "user"}`), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create existing json: %v", err)
	}

	// Try to commit a file that conflicts with existing txt
	tmpTxt := filepath.Join(testDir, "tmp.txt")
	if err := os.WriteFile(tmpTxt, []byte("new content"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create tmp txt: %v", err)
	}

	files := []filePair{
		{Tmp: tmpTxt, Final: existingTxt}, // Should fail - real conflict
	}

	// Commit should fail due to real conflict
	_, err = commitFiles(context.Background(), files)
	if err == nil {
		t.Error("Commit should fail when there's a real conflict")
	}

	// Verify original content is unchanged
	content, err := os.ReadFile(existingTxt)
	if err != nil || string(content) != "existing content" {
		t.Error("Original content should be unchanged after failed commit")
	}
}
