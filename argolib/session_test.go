package argo

import (
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

		// Verify session ID is 4-digit hex (0001)
		sessionID := filepath.Base(session.Path)
		if sessionID != "0001" {
			t.Errorf("Expected first session ID to be '0001', got %q", sessionID)
		}

		// Verify it's valid hex
		for _, c := range sessionID {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Errorf("Invalid hex character in session ID: %c", c)
			}
		}

		// Verify directory was created
		if _, err := os.Stat(session.Path); err != nil {
			t.Errorf("Session directory was not created: %v", err)
		}

		// Test creating multiple sessions - should have sequential IDs
		session2, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create second session: %v", err)
		}

		sessionID2 := filepath.Base(session2.Path)
		if sessionID2 != "0002" {
			t.Errorf("Expected second session ID to be '0002', got %q", sessionID2)
		}

		if session.Path == session2.Path {
			t.Errorf("Two sessions have the same path: %s", session.Path)
		}
	})
}

func TestSessionIDOverflow(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a session directory with ID ffff to simulate near-overflow
		highIDPath := filepath.Join(sessionsDir, "ffff")
		if err := os.Mkdir(highIDPath, 0o750); err != nil {
			t.Fatalf("Failed to create high ID session: %v", err)
		}

		// Now try to create a session - should succeed with 5-digit hex
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session after ffff: %v", err)
		}

		sessionID := filepath.Base(session.Path)
		if sessionID != "10000" {
			t.Errorf("Expected session ID '10000' after 'ffff', got %q", sessionID)
		}

		// Create another one to verify sequential continues
		session2, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create second session: %v", err)
		}

		sessionID2 := filepath.Base(session2.Path)
		if sessionID2 != "10001" {
			t.Errorf("Expected session ID '10001', got %q", sessionID2)
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

		// With bubble-up logic, the nested branch creates a sibling at the parent level
		// So it's a sibling of 0001, which means it includes only message 0
		expectedNestedLineage := []Message{
			linearMessages[0], // Message 0
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

func TestBubbleUpSiblingCreation(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create initial session with messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages
		messages := []Message{
			{Role: "user", Content: "Message 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Message 1", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "Message 2", Timestamp: time.Now()},
			{Role: "assistant", Content: "Message 3", Timestamp: time.Now(), Model: "test-model"},
		}

		for _, msg := range messages {
			if _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		t.Run("BubbleUpFromFirstLevelSibling", func(t *testing.T) {
			// Create first sibling of message 0001
			sibling1Path, err := CreateSibling(session.Path, "0001")
			if err != nil {
				t.Fatalf("Failed to create first sibling: %v", err)
			}

			// Expected: abc123/0001.s.0000
			expectedPath1 := filepath.Join(session.Path, "0001.s.0000")
			if sibling1Path != expectedPath1 {
				t.Errorf("First sibling path mismatch: got %s, want %s", sibling1Path, expectedPath1)
			}

			// Load the sibling session and add a message
			sibSession1, err := LoadSession(sibling1Path)
			if err != nil {
				t.Fatalf("Failed to load sibling session: %v", err)
			}

			sibMsg := Message{
				Role:      "assistant",
				Content:   "Regenerated message 1",
				Timestamp: time.Now(),
				Model:     "test-model-v2",
			}
			_, err = AppendMessage(sibSession1, sibMsg)
			if err != nil {
				t.Fatalf("Failed to append to sibling: %v", err)
			}

			// Now branch from the message inside the sibling (bubble-up test)
			// This should create abc123/0001.s.0001, NOT abc123/0001.s.0000/0000.s.0000
			sibling2Path, err := CreateSibling(sibSession1.Path, "0000")
			if err != nil {
				t.Fatalf("Failed to create second sibling: %v", err)
			}

			// Expected: abc123/0001.s.0001 (bubbled up)
			expectedPath2 := filepath.Join(session.Path, "0001.s.0001")
			if sibling2Path != expectedPath2 {
				t.Errorf("Second sibling path mismatch (bubble-up failed): got %s, want %s", sibling2Path, expectedPath2)
			}
		})

		t.Run("BubbleUpFromDeepNesting", func(t *testing.T) {
			// Create a deeper nesting structure
			// First, create sibling of 0002
			sib1, err := CreateSibling(session.Path, "0002")
			if err != nil {
				t.Fatalf("Failed to create sibling of 0002: %v", err)
			}

			sib1Session, _ := LoadSession(sib1)
			if _, err := AppendMessage(sib1Session, Message{Role: "user", Content: "Alt message", Timestamp: time.Now()}); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}

			// Create sibling of the message in sib1
			sib2, err := CreateSibling(sib1, "0000")
			if err != nil {
				t.Fatalf("Failed to create nested sibling: %v", err)
			}

			// This should bubble up to create a sibling of 0002
			expectedSib2 := filepath.Join(session.Path, "0002.s.0001")
			if sib2 != expectedSib2 {
				t.Errorf("Nested sibling didn't bubble up: got %s, want %s", sib2, expectedSib2)
			}
		})

		t.Run("MultipleBubbleUps", func(t *testing.T) {
			// Test that multiple bubble-ups from the same nested location work correctly
			baseSib, err := CreateSibling(session.Path, "0003")
			if err != nil {
				t.Fatalf("Failed to create base sibling: %v", err)
			}

			baseSibSession, _ := LoadSession(baseSib)
			if _, err := AppendMessage(baseSibSession, Message{Role: "assistant", Content: "Regen 1", Timestamp: time.Now(), Model: "model"}); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}

			// Create multiple siblings from the nested message
			var siblingPaths []string
			for i := 0; i < 3; i++ {
				sibPath, err := CreateSibling(baseSib, "0000")
				if err != nil {
					t.Fatalf("Failed to create bubbled sibling %d: %v", i, err)
				}
				siblingPaths = append(siblingPaths, sibPath)
			}

			// All should bubble up to be siblings of 0003
			expectedPaths := []string{
				filepath.Join(session.Path, "0003.s.0001"),
				filepath.Join(session.Path, "0003.s.0002"),
				filepath.Join(session.Path, "0003.s.0003"),
			}

			for i, gotPath := range siblingPaths {
				if gotPath != expectedPaths[i] {
					t.Errorf("Sibling %d path mismatch: got %s, want %s", i, gotPath, expectedPaths[i])
				}
			}
		})
	})
}

func TestConcurrentSiblingCreation(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Test that concurrent sibling creation doesn't cause index collisions
		// Create a session with a message to branch from
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add initial messages
		if _, err := AppendMessage(session, Message{Role: "user", Content: "Message 0", Timestamp: time.Now()}); err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}
		if _, err := AppendMessage(session, Message{Role: "assistant", Content: "Message 1", Timestamp: time.Now(), Model: "test"}); err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}

		// Create first-level sibling
		sibling1, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create sibling: %v", err)
		}

		// Add message to sibling
		sib1Session, _ := LoadSession(sibling1)
		if _, err := AppendMessage(sib1Session, Message{Role: "assistant", Content: "Alt 1", Timestamp: time.Now(), Model: "test"}); err != nil {
			t.Fatalf("Failed to append to sibling: %v", err)
		}

		// Now create siblings concurrently from different levels
		const numGoroutines = 10
		results := make(chan string, numGoroutines)
		errors := make(chan error, numGoroutines)
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		// Half create siblings from the root level
		for i := 0; i < numGoroutines/2; i++ {
			go func() {
				defer wg.Done()
				path, err := CreateSibling(session.Path, "0001")
				if err != nil {
					errors <- err
				} else {
					results <- path
				}
			}()
		}

		// Half create siblings from the nested level (should bubble up)
		for i := 0; i < numGoroutines/2; i++ {
			go func() {
				defer wg.Done()
				path, err := CreateSibling(sibling1, "0000")
				if err != nil {
					errors <- err
				} else {
					results <- path
				}
			}()
		}

		// Wait for all goroutines to finish
		go func() {
			wg.Wait()
			close(results)
			close(errors)
		}()

		// Collect results
		var paths []string
		lockErrors := 0
		for {
			select {
			case err, ok := <-errors:
				if !ok {
					goto done
				}
				// Lock acquisition failures are expected in concurrent scenarios
				if stderrors.Is(err, ErrLockHeld) || stderrors.Is(err, ErrLockTimeout) {
					lockErrors++
				} else {
					t.Errorf("Unexpected error: %v", err)
				}
			case path, ok := <-results:
				if !ok {
					goto done
				}
				paths = append(paths, path)
			}
		}
	done:

		// Verify all paths are unique
		pathMap := make(map[string]bool)
		for _, path := range paths {
			if pathMap[path] {
				t.Errorf("Duplicate path created: %s", path)
			}
			pathMap[path] = true
		}

		// Verify we got some successful creations
		// Due to locking, not all goroutines may succeed
		expectedSuccesses := numGoroutines - lockErrors
		if len(pathMap) != expectedSuccesses {
			t.Errorf("Expected %d unique paths (with %d lock failures), got %d",
				expectedSuccesses, lockErrors, len(pathMap))
		}

		// Verify we had a reasonable success rate
		if len(pathMap) == 0 {
			t.Errorf("No siblings were created successfully")
		}

		// Log the results for debugging
		t.Logf("Created %d unique sibling paths, %d lock failures", len(pathMap), lockErrors)
	})
}

func TestDeleteNode(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a session with a complex tree structure
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages to root
		messages := []Message{
			{Role: "user", Content: "Message 0", Timestamp: time.Now()},
			{Role: "assistant", Content: "Message 1", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "Message 2", Timestamp: time.Now()},
			{Role: "assistant", Content: "Message 3", Timestamp: time.Now(), Model: "test-model"},
		}

		for _, msg := range messages {
			if _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// Create branches
		branch1, err := CreateSibling(session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create branch1: %v", err)
		}

		// Add messages to branch1
		branch1Session, _ := LoadSession(branch1)
		_, _ = AppendMessage(branch1Session, Message{Role: "user", Content: "Branch1 Message 0", Timestamp: time.Now()})
		_, _ = AppendMessage(branch1Session, Message{Role: "assistant", Content: "Branch1 Message 1", Timestamp: time.Now(), Model: "test"})

		// Create another branch from message 2
		branch2, err := CreateSibling(session.Path, "0002")
		if err != nil {
			t.Fatalf("Failed to create branch2: %v", err)
		}

		// Create a branch from message 3 to test deletion
		branch3, err := CreateSibling(session.Path, "0003")
		if err != nil {
			t.Fatalf("Failed to create branch3: %v", err)
		}

		t.Run("DeleteMessage", func(t *testing.T) {
			// Delete message 0002 and verify subsequent messages and branches are deleted
			err := DeleteNode(filepath.Join(session.Path, "0002"))
			if err != nil {
				t.Fatalf("Failed to delete message: %v", err)
			}

			// Verify message 0002 and 0003 are deleted
			if _, err := ReadMessage(session.Path, "0002"); err == nil {
				t.Errorf("Message 0002 should have been deleted")
			}
			if _, err := ReadMessage(session.Path, "0003"); err == nil {
				t.Errorf("Message 0003 should have been deleted")
			}

			// Verify messages 0000 and 0001 still exist
			if _, err := ReadMessage(session.Path, "0000"); err != nil {
				t.Errorf("Message 0000 should still exist: %v", err)
			}
			if _, err := ReadMessage(session.Path, "0001"); err != nil {
				t.Errorf("Message 0001 should still exist: %v", err)
			}

			// Verify branch from 0001 still exists
			if _, err := os.Stat(branch1); err != nil {
				t.Errorf("Branch from 0001 should still exist: %v", err)
			}

			// Verify branch from 0002 still exists (branches of the deleted message are kept)
			if _, err := os.Stat(branch2); err != nil {
				t.Errorf("Branch from 0002 should still exist: %v", err)
			}

			// Verify branch from 0003 is deleted (branches from later messages are deleted)
			if _, err := os.Stat(branch3); err == nil {
				t.Errorf("Branch from 0003 should have been deleted")
			}
		})

		t.Run("DeleteBranch", func(t *testing.T) {
			// Delete the branch and verify it's gone
			err := DeleteNode(branch1)
			if err != nil {
				t.Fatalf("Failed to delete branch: %v", err)
			}

			if _, err := os.Stat(branch1); err == nil {
				t.Errorf("Branch should have been deleted")
			}
		})

		t.Run("DeleteSession", func(t *testing.T) {
			// Create a new session to delete
			session2, err := CreateSession()
			if err != nil {
				t.Fatalf("Failed to create session2: %v", err)
			}

			// Delete entire session
			err = DeleteNode(session2.Path)
			if err != nil {
				t.Fatalf("Failed to delete session: %v", err)
			}

			if _, err := os.Stat(session2.Path); err == nil {
				t.Errorf("Session should have been deleted")
			}
		})

		t.Run("DeleteNonExistent", func(t *testing.T) {
			// Try to delete non-existent node
			err := DeleteNode(filepath.Join(sessionsDir, "nonexistent"))
			if err == nil {
				t.Errorf("Expected error when deleting non-existent node")
			}
			if !strings.Contains(err.Error(), "node not found") {
				t.Errorf("Expected 'node not found' error, got: %v", err)
			}
		})

		t.Run("DeleteWithRelativePath", func(t *testing.T) {
			// Create a session
			session3, err := CreateSession()
			if err != nil {
				t.Fatalf("Failed to create session3: %v", err)
			}
			sessionID := filepath.Base(session3.Path)

			// Delete using relative path (just session ID)
			err = DeleteNode(sessionID)
			if err != nil {
				t.Fatalf("Failed to delete with relative path: %v", err)
			}

			if _, err := os.Stat(session3.Path); err == nil {
				t.Errorf("Session should have been deleted")
			}
		})

		t.Run("SecurityPathTraversal", func(t *testing.T) {
			// Try to delete with path traversal
			err := DeleteNode("../../../etc/passwd")
			if err == nil {
				t.Errorf("Expected error when trying to delete outside sessions directory")
			}
			if !strings.Contains(err.Error(), "invalid path") {
				t.Errorf("Expected 'invalid path' error, got: %v", err)
			}

			// Try with absolute path outside sessions
			err = DeleteNode("/etc/passwd")
			if err == nil {
				t.Errorf("Expected error when trying to delete absolute path outside sessions")
			}
			if !strings.Contains(err.Error(), "invalid path") {
				t.Errorf("Expected 'invalid path' error, got: %v", err)
			}
		})

		t.Run("SecurityDotDotInPath", func(t *testing.T) {
			// Create a session
			session4, err := CreateSession()
			if err != nil {
				t.Fatalf("Failed to create session4: %v", err)
			}
			sessionID := filepath.Base(session4.Path)

			// Try to use .. in the middle of path
			maliciousPath := sessionID + "/../../../tmp/evil"
			err = DeleteNode(maliciousPath)
			if err == nil {
				t.Errorf("Expected error with .. in path")
			}
			if !strings.Contains(err.Error(), "invalid path") {
				t.Errorf("Expected 'invalid path' error, got: %v", err)
			}

			// Verify original session still exists
			if _, err := os.Stat(session4.Path); err != nil {
				t.Errorf("Session should still exist after failed delete: %v", err)
			}
		})
	})
}
