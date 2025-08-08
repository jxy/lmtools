package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestForceMessageIDConflict tries to force message ID conflicts
func TestForceMessageIDConflict(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Pre-create a message file to force conflict
		msgID := "0000"
		txtPath := filepath.Join(session.Path, msgID+".txt")
		jsonPath := filepath.Join(session.Path, msgID+".json")

		// Create the files
		if err := os.WriteFile(txtPath, []byte("Pre-existing message"), 0o644); err != nil {
			t.Fatalf("Failed to create txt file: %v", err)
		}
		if err := os.WriteFile(jsonPath, []byte(`{"role":"user","timestamp":"2024-01-01T00:00:00Z"}`), 0o644); err != nil {
			t.Fatalf("Failed to create json file: %v", err)
		}

		// Now try to append messages concurrently
		const numGoroutines = 5
		var wg sync.WaitGroup
		results := make(chan struct {
			id    int
			path  string
			msgID string
			err   error
		}, numGoroutines)

		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				defer wg.Done()

				msg := Message{
					Role:      "assistant",
					Content:   fmt.Sprintf("Message from goroutine %d", id),
					Timestamp: time.Now(),
					Model:     "test",
				}

				path, msgID, err := AppendMessage(session, msg)
				results <- struct {
					id    int
					path  string
					msgID string
					err   error
				}{id, path, msgID, err}
			}(i)
		}

		wg.Wait()
		close(results)

		// Analyze results
		mainPathCount := 0
		siblingPaths := make(map[string]int)
		allMsgIDs := make(map[string]int)

		t.Logf("\n=== Forced Conflict Results ===")
		for result := range results {
			if result.err != nil {
				t.Errorf("Goroutine %d error: %v", result.id, result.err)
				continue
			}

			if result.path == session.Path {
				mainPathCount++
				t.Logf("Goroutine %d: Main path, msgID=%s", result.id, result.msgID)
			} else {
				siblingPaths[result.path]++
				t.Logf("Goroutine %d: Sibling %s, msgID=%s", result.id, GetSessionID(result.path), result.msgID)
			}

			if prev, exists := allMsgIDs[result.msgID]; exists {
				t.Errorf("CONFLICT: msgID %s used by both goroutine %d and %d", result.msgID, prev, result.id)
			}
			allMsgIDs[result.msgID] = result.id
		}

		t.Logf("\nSummary:")
		t.Logf("  Main path messages: %d", mainPathCount)
		t.Logf("  Sibling paths created: %d", len(siblingPaths))
		t.Logf("  Unique message IDs: %d", len(allMsgIDs))

		// Since we pre-created 0000, we expect siblings to be created
		if len(siblingPaths) == 0 && mainPathCount > 1 {
			t.Logf("\n✓ Conflict handling worked - all messages got sequential IDs")
		} else if len(siblingPaths) > 0 {
			t.Logf("\n✓ Conflict handling worked - created %d siblings", len(siblingPaths))
		}
	})
}

// TestLockFileVisibility checks if lock files are visible during operations
func TestLockFileVisibility(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		lockFilesSeen := make(chan string, 10)
		done := make(chan struct{})

		// Start a goroutine that watches for lock files
		go func() {
			ticker := time.NewTicker(1 * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					entries, err := os.ReadDir(session.Path)
					if err != nil {
						continue
					}

					for _, entry := range entries {
						name := entry.Name()
						if filepath.Ext(name) == ".lock" {
							select {
							case lockFilesSeen <- name:
							case <-done:
								return
							}
						}
					}
				}
			}
		}()

		// Perform operations that should create locks
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				msg := Message{
					Role:      "user",
					Content:   fmt.Sprintf("Message %d", id),
					Timestamp: time.Now(),
				}

				// Add a small delay to increase lock visibility window
				time.Sleep(5 * time.Millisecond)
				_, _, _ = AppendMessage(session, msg)
			}(i)
		}

		wg.Wait()
		close(done)
		close(lockFilesSeen)

		// Collect unique lock files seen
		seenLocks := make(map[string]bool)
		for lockFile := range lockFilesSeen {
			seenLocks[lockFile] = true
		}

		t.Logf("Lock files observed during operations: %d", len(seenLocks))
		for lock := range seenLocks {
			t.Logf("  - %s", lock)
		}

		// Check final state - no locks should remain
		entries, _ := os.ReadDir(session.Path)
		remainingLocks := 0
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".lock" {
				remainingLocks++
				t.Errorf("Lock file still present after operations: %s", entry.Name())
			}
		}

		if remainingLocks == 0 {
			t.Logf("✓ All lock files cleaned up properly")
		}
	})
}

// TestConcurrentResumeRealWorld simulates the real-world usage pattern
func TestConcurrentResumeRealWorld(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with some history
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		sessionID := filepath.Base(session.Path)

		// Add conversation history
		history := []Message{
			{Role: "user", Content: "Explain quantum computing", Timestamp: time.Now()},
			{Role: "assistant", Content: "Quantum computing uses quantum mechanics...", Timestamp: time.Now(), Model: "gpt4o"},
			{Role: "user", Content: "What are qubits?", Timestamp: time.Now()},
			{Role: "assistant", Content: "Qubits are quantum bits...", Timestamp: time.Now(), Model: "gpt4o"},
			{Role: "user", Content: "How do they work?", Timestamp: time.Now()},
		}

		for _, msg := range history {
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to add history: %v", err)
			}
		}

		// Simulate multiple users resuming and adding responses
		type user struct {
			name  string
			delay time.Duration
		}

		users := []user{
			{"Alice", 0},
			{"Bob", 1 * time.Millisecond},
			{"Charlie", 2 * time.Millisecond},
			{"Dave", 0},
			{"Eve", 1 * time.Millisecond},
		}

		var wg sync.WaitGroup
		results := make(chan struct {
			user  string
			path  string
			msgID string
			err   error
		}, len(users))

		for _, u := range users {
			wg.Add(1)
			go func(user user) {
				defer wg.Done()

				// Simulate slight delay in starting
				time.Sleep(user.delay)

				// Load session (resume)
				session, err := LoadSession(sessionID)
				if err != nil {
					results <- struct {
						user  string
						path  string
						msgID string
						err   error
					}{user.name, "", "", err}
					return
				}

				// Each user adds their response
				msg := Message{
					Role:      "assistant",
					Content:   fmt.Sprintf("%s: Qubits work through superposition and entanglement...", user.name),
					Timestamp: time.Now(),
					Model:     "gpt4o",
				}

				path, msgID, err := AppendMessage(session, msg)
				results <- struct {
					user  string
					path  string
					msgID string
					err   error
				}{user.name, path, msgID, err}
			}(u)
		}

		wg.Wait()
		close(results)

		// Analyze outcomes
		t.Logf("\n=== Real-World Concurrent Resume ===")

		pathUsers := make(map[string][]string)
		for result := range results {
			if result.err != nil {
				t.Errorf("%s: Error: %v", result.user, result.err)
			} else {
				pathUsers[result.path] = append(pathUsers[result.path], result.user)
				t.Logf("%s: Added message %s to %s", result.user, result.msgID, GetSessionID(result.path))
			}
		}

		t.Logf("\nPath distribution:")
		for path, users := range pathUsers {
			t.Logf("  %s: %v", GetSessionID(path), users)
		}

		// Verify each path has valid lineage
		for path := range pathUsers {
			lineage, err := GetLineage(path)
			if err != nil {
				t.Errorf("Failed to get lineage for %s: %v", path, err)
				continue
			}

			// Should have original 5 messages + new responses
			if len(lineage) < 6 {
				t.Errorf("Path %s has incomplete lineage: %d messages", path, len(lineage))
			}

			// Last message should be from one of our users
			if len(lineage) > 0 {
				lastMsg := lineage[len(lineage)-1]
				t.Logf("  %s last message: %.50s...", GetSessionID(path), lastMsg.Content)
			}
		}
	})
}
