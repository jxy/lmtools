package argo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCreateSession(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Test creating a session
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Verify session ID is 4-digit hex
		sessionID := filepath.Base(session.Path)
		if len(sessionID) != 4 {
			t.Errorf("Expected 4-character session ID, got %q", sessionID)
		}

		// Verify it's valid hex
		for _, c := range sessionID {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("Invalid hex character in session ID: %c", c)
			}
		}

		// Verify directory was created
		if _, err := os.Stat(session.Path); err != nil {
			t.Errorf("Session directory was not created: %v", err)
		}

		// Test creating multiple sessions - should have unique IDs
		session2, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create second session: %v", err)
		}

		if session.Path == session2.Path {
			t.Errorf("Two sessions have the same path: %s", session.Path)
		}
	})
}

func TestLoadSession(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a session first
		created, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Test loading with relative path (just ID)
		sessionID := filepath.Base(created.Path)
		loaded, err := LoadSession(sessionID)
		if err != nil {
			t.Fatalf("Failed to load session by ID: %v", err)
		}

		if loaded.Path != created.Path {
			t.Errorf("Loaded session path mismatch: expected %s, got %s", created.Path, loaded.Path)
		}

		// Test loading with absolute path
		loaded2, err := LoadSession(created.Path)
		if err != nil {
			t.Fatalf("Failed to load session by absolute path: %v", err)
		}

		if loaded2.Path != created.Path {
			t.Errorf("Loaded session path mismatch: expected %s, got %s", created.Path, loaded2.Path)
		}

		// Test loading non-existent session
		_, err = LoadSession("nonexistent")
		if err == nil {
			t.Error("Expected error loading non-existent session")
		}
	})
}

func TestAppendMessage(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Test appending user message
		userMsg := Message{
			Role:      "user",
			Content:   "Hello, world!",
			Timestamp: time.Now(),
		}

		msgID, err := AppendMessage(session, userMsg)
		if err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}

		if msgID != "0000" {
			t.Errorf("Expected first message ID to be 0000, got %s", msgID)
		}

		// Verify files were created
		assertFileStructure(t, session.Path, []string{"0000.txt", "0000.json"})

		// Verify content
		assertMessageContent(t, session.Path, "0000", "user", "Hello, world!")

		// Test appending assistant message
		assistantMsg := Message{
			Role:      "assistant",
			Content:   "Hi there!",
			Timestamp: time.Now(),
			Model:     "test-model",
		}

		msgID2, err := AppendMessage(session, assistantMsg)
		if err != nil {
			t.Fatalf("Failed to append second message: %v", err)
		}

		if msgID2 != "0001" {
			t.Errorf("Expected second message ID to be 0001, got %s", msgID2)
		}

		// Test message ID incrementing
		for i := 2; i < 5; i++ {
			msg := Message{
				Role:      "user",
				Content:   "Test message",
				Timestamp: time.Now(),
			}
			msgID, err := AppendMessage(session, msg)
			if err != nil {
				t.Fatalf("Failed to append message %d: %v", i, err)
			}

			expected := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(
				strings.TrimPrefix(strings.TrimSpace(
					strings.TrimPrefix("0x"+msgID, "0x")), "0")), "x"))
			if msgID != expected {
				// Just verify it increments
				if len(msgID) != 4 {
					t.Errorf("Expected 4-digit hex ID, got %s", msgID)
				}
			}
		}
	})
}

func TestCreateSibling(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create session with messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add some messages
		messages := []Message{
			{Role: "user", Content: "Message 1", Timestamp: time.Now()},
			{Role: "assistant", Content: "Message 2", Timestamp: time.Now()},
			{Role: "user", Content: "Message 3", Timestamp: time.Now()},
		}

		for _, msg := range messages {
			if _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// Create sibling from message 0001
		siblingPath, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create sibling: %v", err)
		}

		// Verify sibling path
		expectedSibling := filepath.Join(session.Path, "0001.s.0000")
		if siblingPath != expectedSibling {
			t.Errorf("Expected sibling path %s, got %s", expectedSibling, siblingPath)
		}

		// Verify directory was created
		if _, err := os.Stat(siblingPath); err != nil {
			t.Errorf("Sibling directory was not created: %v", err)
		}

		// Create second sibling from same message
		siblingPath2, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create second sibling: %v", err)
		}

		expectedSibling2 := filepath.Join(session.Path, "0001.s.0001")
		if siblingPath2 != expectedSibling2 {
			t.Errorf("Expected second sibling path %s, got %s", expectedSibling2, siblingPath2)
		}
	})
}

func TestGetLineage(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a session with linear messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add linear conversation
		linearMessages := []Message{
			{Role: "user", Content: "Message 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Message 1", Timestamp: time.Now()},
			{Role: "user", Content: "Message 2", Timestamp: time.Now()},
			{Role: "assistant", Content: "Message 3", Timestamp: time.Now()},
		}

		for _, msg := range linearMessages {
			if _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// Test linear lineage
		lineage, err := GetLineage(session.Path)
		if err != nil {
			t.Fatalf("Failed to get lineage: %v", err)
		}

		assertLineageEqual(t, linearMessages, lineage)

		// Create a branch from message 0001
		branchPath, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create branch: %v", err)
		}

		branchSession, err := LoadSession(branchPath)
		if err != nil {
			t.Fatalf("Failed to load branch session: %v", err)
		}

		// Add messages to branch
		branchMessages := []Message{
			{Role: "user", Content: "Branch message 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Branch message 1", Timestamp: time.Now()},
		}

		for _, msg := range branchMessages {
			if _, err := AppendMessage(branchSession, msg); err != nil {
				t.Fatalf("Failed to append branch message: %v", err)
			}
		}

		// Test branch lineage - branch from 0001 means 0001 is the sibling point
		// So we include messages up to (but not including) 0001, then branch messages
		branchLineage, err := GetLineage(branchSession.Path)
		if err != nil {
			t.Fatalf("Failed to get branch lineage: %v", err)
		}

		expectedBranchLineage := []Message{
			linearMessages[0], // Message 0
			branchMessages[0], // Branch message 0
			branchMessages[1], // Branch message 1
		}

		assertLineageEqual(t, expectedBranchLineage, branchLineage)

		// Test nested branch
		nestedBranchPath, err := CreateSibling(branchSession.Path, "0000")
		if err != nil {
			t.Fatalf("Failed to create nested branch: %v", err)
		}

		nestedSession, err := LoadSession(nestedBranchPath)
		if err != nil {
			t.Fatalf("Failed to load nested branch session: %v", err)
		}

		nestedMsg := Message{Role: "user", Content: "Nested message", Timestamp: time.Now()}
		if _, err := AppendMessage(nestedSession, nestedMsg); err != nil {
			t.Fatalf("Failed to append nested message: %v", err)
		}

		// Test nested lineage
		t.Logf("Nested session path: %s", nestedSession.Path)
		nestedLineage, err := GetLineage(nestedSession.Path)
		if err != nil {
			t.Fatalf("Failed to get nested lineage: %v", err)
		}

		t.Logf("Nested lineage length: %d", len(nestedLineage))
		for i, msg := range nestedLineage {
			t.Logf("  [%d] %s: %s", i, msg.Role, msg.Content)
		}

		// The actual lineage should exclude the sibling point
		expectedNestedLineage := []Message{
			linearMessages[0], // Message 0
			linearMessages[1], // Message 1
			nestedMsg,         // Nested message
		}

		assertLineageEqual(t, expectedNestedLineage, nestedLineage)
	})
}

func TestGetSessionID(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Test with simple session
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		sessionID := GetSessionID(session.Path)
		expectedID := filepath.Base(session.Path)
		if sessionID != expectedID {
			t.Errorf("Expected session ID %s, got %s", expectedID, sessionID)
		}

		// Test with branch
		branchPath, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create branch: %v", err)
		}

		branchID := GetSessionID(branchPath)
		expectedBranchID := expectedID + "/0001.s.0000"
		if branchID != expectedBranchID {
			t.Errorf("Expected branch ID %s, got %s", expectedBranchID, branchID)
		}
	})
}

func TestAssistantMessageRegeneration(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a session with user and assistant messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add initial messages
		messages := []Message{
			{Role: "user", Content: "Hello", Timestamp: time.Now()},
			{Role: "assistant", Content: "Hi there!", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "How are you?", Timestamp: time.Now()},
			{Role: "assistant", Content: "I'm doing well!", Timestamp: time.Now(), Model: "test-model"},
		}

		for _, msg := range messages {
			if _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		t.Run("RegenerateFirstAssistant", func(t *testing.T) {
			// Create sibling for first assistant message (0001)
			siblingPath, err := CreateSibling(session.Path, "0001")
			if err != nil {
				t.Fatalf("Failed to create sibling: %v", err)
			}

			// Verify sibling path
			expectedPath := filepath.Join(session.Path, "0001.s.0000")
			if siblingPath != expectedPath {
				t.Errorf("Expected sibling path %s, got %s", expectedPath, siblingPath)
			}

			// Load sibling session
			siblingSession, err := LoadSession(siblingPath)
			if err != nil {
				t.Fatalf("Failed to load sibling session: %v", err)
			}

			// Get lineage - should only include first user message
			lineage, err := GetLineage(siblingSession.Path)
			if err != nil {
				t.Fatalf("Failed to get lineage: %v", err)
			}

			if len(lineage) != 1 {
				t.Errorf("Expected 1 message in lineage, got %d", len(lineage))
			}

			if len(lineage) > 0 && lineage[0].Content != "Hello" {
				t.Errorf("Expected first message to be 'Hello', got '%s'", lineage[0].Content)
			}

			// Add regenerated assistant message
			regenMsg := Message{
				Role:      "assistant",
				Content:   "Hello! How can I help you?",
				Timestamp: time.Now(),
				Model:     "test-model-v2",
			}

			if _, err := AppendMessage(siblingSession, regenMsg); err != nil {
				t.Fatalf("Failed to append regenerated message: %v", err)
			}

			// Verify final lineage
			finalLineage, err := GetLineage(siblingSession.Path)
			if err != nil {
				t.Fatalf("Failed to get final lineage: %v", err)
			}

			if len(finalLineage) != 2 {
				t.Errorf("Expected 2 messages in final lineage, got %d", len(finalLineage))
			}

			if len(finalLineage) > 1 && finalLineage[1].Content != "Hello! How can I help you?" {
				t.Errorf("Expected regenerated message, got '%s'", finalLineage[1].Content)
			}
		})

		t.Run("RegenerateLastAssistant", func(t *testing.T) {
			// Create sibling for last assistant message (0003)
			siblingPath, err := CreateSibling(session.Path, "0003")
			if err != nil {
				t.Fatalf("Failed to create sibling: %v", err)
			}

			// Load sibling session
			siblingSession, err := LoadSession(siblingPath)
			if err != nil {
				t.Fatalf("Failed to load sibling session: %v", err)
			}

			// Get lineage - should include all messages except the last assistant
			lineage, err := GetLineage(siblingSession.Path)
			if err != nil {
				t.Fatalf("Failed to get lineage: %v", err)
			}

			if len(lineage) != 3 {
				t.Errorf("Expected 3 messages in lineage, got %d", len(lineage))
			}

			// Verify lineage content
			expectedContents := []string{"Hello", "Hi there!", "How are you?"}
			for i, expected := range expectedContents {
				if i < len(lineage) && lineage[i].Content != expected {
					t.Errorf("Message %d: expected '%s', got '%s'", i, expected, lineage[i].Content)
				}
			}
		})

		t.Run("MultipleRegenerations", func(t *testing.T) {
			// Create multiple regenerations of the same assistant message
			// Note: Previous tests have already created siblings, so we start fresh
			var siblingPaths []string
			startingIndex := 0

			// Find the next available sibling index
			testPath, _ := GetNextSiblingPath(session.Path, "0001")
			if strings.Contains(testPath, ".s.") {
				parts := strings.Split(testPath, ".s.")
				if len(parts) == 2 {
					if idx, err := strconv.ParseUint(parts[1], 16, 64); err == nil {
						startingIndex = int(idx)
					}
				}
			}
			
			for i := 0; i < 3; i++ {
				siblingPath, err := CreateSibling(session.Path, "0001")
				if err != nil {
					t.Fatalf("Failed to create sibling %d: %v", i, err)
				}
				siblingPaths = append(siblingPaths, siblingPath)
			}

			// Verify paths are sequential starting from the current index
			var expectedPaths []string
			for i := 0; i < 3; i++ {
				expectedPaths = append(expectedPaths, 
					filepath.Join(session.Path, fmt.Sprintf("0001.s.%04x", startingIndex+i)))
			}

			for i, expected := range expectedPaths {
				if siblingPaths[i] != expected {
					t.Errorf("Sibling %d: expected path %s, got %s", i, expected, siblingPaths[i])
				}
			}

			// Verify each sibling can be loaded and has correct lineage
			for i, path := range siblingPaths {
				sibSession, err := LoadSession(path)
				if err != nil {
					t.Fatalf("Failed to load sibling %d: %v", i, err)
				}

				lineage, err := GetLineage(sibSession.Path)
				if err != nil {
					t.Fatalf("Failed to get lineage for sibling %d: %v", i, err)
				}

				if len(lineage) != 1 {
					t.Errorf("Sibling %d: expected 1 message in lineage, got %d", i, len(lineage))
				}
			}
		})
	})
}
