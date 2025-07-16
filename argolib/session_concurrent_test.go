package argo

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConcurrentResume tests the exact scenario from the user's bug report
func TestConcurrentResume(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create initial session with a message
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add initial message
		msg1 := Message{
			Role:      "user",
			Content:   "Initial message",
			Timestamp: time.Now(),
		}
		_, _, err = AppendMessage(session, msg1)
		if err != nil {
			t.Fatalf("Failed to append initial message: %v", err)
		}

		sessionID := filepath.Base(session.Path)

		// Simulate multiple concurrent resume operations
		const numConcurrent = 5
		var wg sync.WaitGroup
		results := make(chan struct {
			path  string
			msgID string
			err   error
		}, numConcurrent)

		wg.Add(numConcurrent)
		startTime := time.Now()

		// Launch concurrent resume operations
		for i := 0; i < numConcurrent; i++ {
			go func(goroutineID int) {
				defer wg.Done()

				// Simulate loading the session (resume)
				resumedSession, err := LoadSession(sessionID)
				if err != nil {
					results <- struct {
						path  string
						msgID string
						err   error
					}{"", "", fmt.Errorf("goroutine %d: failed to load session: %w", goroutineID, err)}
					return
				}

				// Each goroutine tries to append a message
				msg := Message{
					Role:      "assistant",
					Content:   fmt.Sprintf("Response from goroutine %d", goroutineID),
					Timestamp: time.Now(),
					Model:     "test-model",
				}

				path, msgID, err := AppendMessage(resumedSession, msg)
				results <- struct {
					path  string
					msgID string
					err   error
				}{path, msgID, err}
			}(i)
		}

		// Wait for all goroutines to complete
		go func() {
			wg.Wait()
			close(results)
		}()

		// Collect results
		var paths []string
		var errors []error
		messageMap := make(map[string]string) // msgID -> path

		for result := range results {
			if result.err != nil {
				errors = append(errors, result.err)
			} else {
				paths = append(paths, result.path)
				messageMap[result.msgID] = result.path
			}
		}

		duration := time.Since(startTime)
		t.Logf("Concurrent resume test completed in %v", duration)

		// Verify no errors
		if len(errors) > 0 {
			for _, err := range errors {
				t.Errorf("Error during concurrent append: %v", err)
			}
		}

		// Verify all operations succeeded
		if len(paths) != numConcurrent {
			t.Errorf("Expected %d successful appends, got %d", numConcurrent, len(paths))
		}

		// Count how many went to main path vs siblings
		mainPathCount := 0
		siblingPaths := make(map[string]int)

		for _, path := range paths {
			if path == session.Path {
				mainPathCount++
			} else {
				siblingPaths[path]++
			}
		}

		t.Logf("Main path messages: %d", mainPathCount)
		t.Logf("Sibling paths: %d", len(siblingPaths))
		for path, count := range siblingPaths {
			t.Logf("  %s: %d messages", GetSessionID(path), count)
		}

		// Verify message IDs are unique across all paths
		if len(messageMap) != numConcurrent {
			t.Errorf("Expected %d unique message IDs, got %d", numConcurrent, len(messageMap))
		}

		// Verify lineage for each path
		allPaths := append([]string{session.Path}, getKeys(siblingPaths)...)
		for _, path := range allPaths {
			lineage, err := GetLineage(path)
			if err != nil {
				t.Errorf("Failed to get lineage for %s: %v", path, err)
				continue
			}

			// Each path should have at least 2 messages (initial + one response)
			if len(lineage) < 2 {
				t.Errorf("Path %s has insufficient lineage: %d messages", path, len(lineage))
			}

			// First message should always be the initial message
			if lineage[0].Content != "Initial message" {
				t.Errorf("Path %s: first message mismatch: %s", path, lineage[0].Content)
			}

			t.Logf("Lineage for %s: %d messages", GetSessionID(path), len(lineage))
		}
	})
}

// TestConcurrentAppendSameSession tests multiple goroutines appending to the same session
func TestConcurrentAppendSameSession(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		const numMessages = 10
		var wg sync.WaitGroup
		errors := make(chan error, numMessages)

		wg.Add(numMessages)

		// Launch concurrent appends
		for i := 0; i < numMessages; i++ {
			go func(msgNum int) {
				defer wg.Done()

				msg := Message{
					Role:      "user",
					Content:   fmt.Sprintf("Message %d", msgNum),
					Timestamp: time.Now(),
				}

				_, _, err := AppendMessage(session, msg)
				if err != nil {
					errors <- fmt.Errorf("message %d: %w", msgNum, err)
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// Check for errors
		for err := range errors {
			t.Errorf("Append error: %v", err)
		}

		// List all messages including those in siblings
		allMessages, siblingDirs := listAllMessagesIncludingSiblings(t, session.Path)

		t.Logf("Total messages created: %d", len(allMessages))
		t.Logf("Sibling directories created: %d", len(siblingDirs))

		// Verify we have all messages
		if len(allMessages) != numMessages {
			t.Errorf("Expected %d total messages, got %d", numMessages, len(allMessages))
		}

		// Verify message IDs are unique
		idMap := make(map[string]bool)
		for _, msgID := range allMessages {
			if idMap[msgID] {
				t.Errorf("Duplicate message ID found: %s", msgID)
			}
			idMap[msgID] = true
		}
	})
}

// TestConcurrentBranchCreation tests concurrent creation of branches from the same message
func TestConcurrentBranchCreation(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages
		messages := []Message{
			{Role: "user", Content: "Question", Timestamp: time.Now()},
			{Role: "assistant", Content: "Answer", Timestamp: time.Now(), Model: "test"},
		}

		for _, msg := range messages {
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		const numBranches = 10
		var wg sync.WaitGroup
		branchPaths := make(chan string, numBranches)
		errors := make(chan error, numBranches)

		wg.Add(numBranches)

		// Create branches concurrently from message 0001
		for i := 0; i < numBranches; i++ {
			go func(branchNum int) {
				defer wg.Done()

				path, err := CreateSibling(session.Path, "0001")
				if err != nil {
					errors <- fmt.Errorf("branch %d: %w", branchNum, err)
				} else {
					branchPaths <- path
				}
			}(i)
		}

		wg.Wait()
		close(branchPaths)
		close(errors)

		// Collect results
		var paths []string
		for path := range branchPaths {
			paths = append(paths, path)
		}

		var errs []error
		for err := range errors {
			errs = append(errs, err)
		}

		t.Logf("Successfully created %d branches", len(paths))
		t.Logf("Errors: %d", len(errs))

		// Verify all paths are unique
		pathMap := make(map[string]bool)
		for _, path := range paths {
			if pathMap[path] {
				t.Errorf("Duplicate branch path: %s", path)
			}
			pathMap[path] = true
		}

		// We expect all operations to succeed
		if len(paths) != numBranches {
			t.Errorf("Expected %d branches, got %d", numBranches, len(paths))
			for _, err := range errs {
				t.Logf("Error: %v", err)
			}
		}
	})
}

// TestConcurrentSiblingConflictResolution tests the sibling creation under high conflict
func TestConcurrentSiblingConflictResolution(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add initial message
		_, _, err = AppendMessage(session, Message{
			Role:      "user",
			Content:   "Hello",
			Timestamp: time.Now(),
		})
		if err != nil {
			t.Fatalf("Failed to append initial message: %v", err)
		}

		const numGoroutines = 20
		var wg sync.WaitGroup
		results := make(chan struct {
			path  string
			msgID string
		}, numGoroutines)

		wg.Add(numGoroutines)

		// All goroutines try to append at the same time
		startSignal := make(chan struct{})

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				// Wait for start signal to maximize concurrency
				<-startSignal

				msg := Message{
					Role:      "assistant",
					Content:   fmt.Sprintf("Response %d", id),
					Timestamp: time.Now(),
					Model:     "test",
				}

				path, msgID, err := AppendMessage(session, msg)
				if err != nil {
					t.Errorf("Goroutine %d failed: %v", id, err)
					return
				}

				results <- struct {
					path  string
					msgID string
				}{path, msgID}
			}(i)
		}

		// Start all goroutines at once
		close(startSignal)

		// Wait for completion
		wg.Wait()
		close(results)

		// Analyze results
		pathCounts := make(map[string]int)
		msgIDs := make(map[string]bool)

		for result := range results {
			pathCounts[result.path]++
			if msgIDs[result.msgID] {
				t.Errorf("Duplicate message ID: %s", result.msgID)
			}
			msgIDs[result.msgID] = true
		}

		t.Logf("Paths created:")
		for path, count := range pathCounts {
			t.Logf("  %s: %d messages", GetSessionID(path), count)
		}

		// Verify total messages
		if len(msgIDs) != numGoroutines {
			t.Errorf("Expected %d unique messages, got %d", numGoroutines, len(msgIDs))
		}
	})
}

// TestRapidSequentialAppends tests rapid sequential appends that might overlap
func TestRapidSequentialAppends(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		const numMessages = 50
		paths := make(map[string]int)

		for i := 0; i < numMessages; i++ {
			msg := Message{
				Role:      "user",
				Content:   fmt.Sprintf("Rapid message %d", i),
				Timestamp: time.Now(),
			}

			path, _, err := AppendMessage(session, msg)
			if err != nil {
				t.Fatalf("Failed to append message %d: %v", i, err)
			}

			paths[path]++
		}

		t.Logf("Message distribution:")
		for path, count := range paths {
			t.Logf("  %s: %d messages", GetSessionID(path), count)
		}

		// Verify total messages across all paths
		totalMessages := 0
		for path := range paths {
			msgs, err := ListMessages(path)
			if err != nil {
				t.Errorf("Failed to list messages in %s: %v", path, err)
				continue
			}
			totalMessages += len(msgs)
		}

		if totalMessages != numMessages {
			t.Errorf("Expected %d total messages, found %d", numMessages, totalMessages)
		}
	})
}

// Helper functions

func getKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func listAllMessagesIncludingSiblings(t *testing.T, sessionPath string) ([]string, []string) {
	var allMessages []string
	var siblingDirs []string

	// Get messages in main path
	msgs, err := ListMessages(sessionPath)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}
	allMessages = append(allMessages, msgs...)

	// Check for siblings of each message
	for _, msgID := range msgs {
		siblings, err := findSiblings(sessionPath, msgID)
		if err != nil {
			t.Errorf("Failed to find siblings for %s: %v", msgID, err)
			continue
		}

		for _, sibDir := range siblings {
			siblingDirs = append(siblingDirs, sibDir)
			sibPath := filepath.Join(sessionPath, sibDir)

			// Recursively get messages from sibling
			sibMsgs, sibSibDirs := listAllMessagesIncludingSiblings(t, sibPath)
			allMessages = append(allMessages, sibMsgs...)
			siblingDirs = append(siblingDirs, sibSibDirs...)
		}
	}

	return allMessages, siblingDirs
}
