package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWriteReadMessage(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session directory
		testSession := filepath.Join(sessionsDir, "test")
		if err := ensureSessionDir(testSession); err != nil {
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
		readMsg, err := ReadMessage(testSession, "0000")
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

		readAssistantMsg, err := ReadMessage(testSession, "0001")
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
		if err := ensureSessionDir(testDir); err != nil {
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
		if err := ensureSessionDir(testSession); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Test empty directory
		messages, err := ListMessages(testSession)
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
		messages, err = ListMessages(testSession)
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
		if err := os.WriteFile(orphanTxt, []byte("orphan"), 0o644); err != nil {
			t.Fatalf("Failed to create orphan txt file: %v", err)
		}

		messages, err = ListMessages(testSession)
		if err != nil {
			t.Fatalf("Failed to list messages with orphan: %v", err)
		}

		// Should still have same number of messages (orphan ignored)
		if len(messages) != len(testMessages) {
			t.Errorf("Expected %d messages (orphan ignored), got %d", len(testMessages), len(messages))
		}

		// Test with non-message files
		randomFile := filepath.Join(testSession, "random.dat")
		if err := os.WriteFile(randomFile, []byte("random"), 0o644); err != nil {
			t.Fatalf("Failed to create random file: %v", err)
		}

		messages, err = ListMessages(testSession)
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
		if err := ensureSessionDir(testSession); err != nil {
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

		// Test with corrupted message (missing txt file)
		corruptID := "0003"
		metadata := MessageMetadata{
			Role:      "user",
			Timestamp: time.Now(),
		}
		metaData, _ := json.MarshalIndent(metadata, "", "  ")
		metaPath := filepath.Join(testSession, corruptID+".json")
		if err := os.WriteFile(metaPath, metaData, 0o644); err != nil {
			t.Fatalf("Failed to create corrupt message: %v", err)
		}

		// Should skip corrupted message with warning
		loadedMessages, err = loadMessagesInDir(testSession)
		if err != nil {
			t.Fatalf("Failed to load messages with corrupt: %v", err)
		}

		// Should still have the valid messages
		if len(loadedMessages) != len(expectedMessages) {
			t.Errorf("Expected %d valid messages, got %d", len(expectedMessages), len(loadedMessages))
		}
	})
}

func TestFindSiblings(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		testSession := filepath.Join(sessionsDir, "test")
		if err := ensureSessionDir(testSession); err != nil {
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
			if err := ensureSessionDir(sibDir); err != nil {
				t.Fatalf("Failed to create sibling dir %s: %v", sib, err)
			}
		}

		// Also create siblings for different message
		otherSibling := filepath.Join(testSession, "0003.s.0000")
		if err := ensureSessionDir(otherSibling); err != nil {
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
		err := ensureSessionDir(newDir)
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
		err = ensureSessionDir(newDir)
		if err != nil {
			t.Errorf("Failed to ensure existing dir: %v", err)
		}
	})
}

func TestListMessagesNumericOrdering(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		sessionPath := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(sessionPath, 0o750); err != nil {
			t.Fatal(err)
		}

		// Create messages with IDs that would sort incorrectly lexicographically
		testIDs := []string{"0001", "0002", "000a", "000f", "0010", "00ff", "0100", "ffff", "10000"}

		for _, id := range testIDs {
			// Create dummy files
			contentPath := filepath.Join(sessionPath, id+".txt")
			metaPath := filepath.Join(sessionPath, id+".json")

			if err := os.WriteFile(contentPath, []byte("test"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metaPath, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		// List messages
		messages, err := ListMessages(sessionPath)
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
		session, err := CreateSession()
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
			_, msgID, err := AppendMessage(session, msg)
			if err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
			messageIDs = append(messageIDs, msgID)
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
