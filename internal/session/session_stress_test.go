package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStressConcurrentResumeWithConflicts simulates the exact user scenario
// with multiple processes trying to resume and append messages simultaneously
func TestStressConcurrentResumeWithConflicts(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create initial session
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		sessionID := filepath.Base(session.Path)

		// Add initial conversation
		initialMessages := []Message{
			{Role: "user", Content: "What is Go?", Timestamp: time.Now()},
			{Role: "assistant", Content: "Go is a programming language.", Timestamp: time.Now(), Model: "test"},
			{Role: "user", Content: "Tell me more", Timestamp: time.Now()},
		}

		for _, msg := range initialMessages {
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append initial message: %v", err)
			}
		}

		// Simulate multiple concurrent resume operations with zero delay
		const numConcurrent = 10
		var wg sync.WaitGroup
		results := make(chan struct {
			goroutineID int
			path        string
			msgID       string
			err         error
		}, numConcurrent)

		wg.Add(numConcurrent)

		// Use a barrier to ensure all goroutines start at exactly the same time
		barrier := make(chan struct{})

		for i := 0; i < numConcurrent; i++ {
			go func(id int) {
				defer wg.Done()

				// Wait at the barrier
				<-barrier

				// Simulate resume: load session
				session, err := LoadSession(sessionID)
				if err != nil {
					results <- struct {
						goroutineID int
						path        string
						msgID       string
						err         error
					}{id, "", "", err}
					return
				}

				// Immediately try to append (no delay)
				msg := Message{
					Role:      "assistant",
					Content:   fmt.Sprintf("Detailed explanation from assistant %d about Go...", id),
					Timestamp: time.Now(),
					Model:     "test-model",
				}

				path, msgID, err := AppendMessage(session, msg)
				results <- struct {
					goroutineID int
					path        string
					msgID       string
					err         error
				}{id, path, msgID, err}
			}(i)
		}

		// Release all goroutines at once
		startTime := time.Now()
		close(barrier)

		// Wait for completion
		wg.Wait()
		close(results)
		duration := time.Since(startTime)

		// Analyze results
		var successCount int
		var errorCount int
		pathDistribution := make(map[string][]int) // path -> goroutine IDs
		messageIDs := make(map[string]int)         // msgID -> goroutineID

		for result := range results {
			if result.err != nil {
				errorCount++
				t.Errorf("Goroutine %d error: %v", result.goroutineID, result.err)
			} else {
				successCount++
				pathDistribution[result.path] = append(pathDistribution[result.path], result.goroutineID)

				if prevID, exists := messageIDs[result.msgID]; exists {
					t.Errorf("CONFLICT: Message ID %s used by both goroutine %d and %d",
						result.msgID, prevID, result.goroutineID)
				}
				messageIDs[result.msgID] = result.goroutineID
			}
		}

		t.Logf("\n=== Stress Test Results ===")
		t.Logf("Duration: %v", duration)
		t.Logf("Success: %d/%d", successCount, numConcurrent)
		t.Logf("Errors: %d", errorCount)
		t.Logf("Unique paths used: %d", len(pathDistribution))

		// Show distribution
		mainPath := session.Path
		mainCount := len(pathDistribution[mainPath])
		t.Logf("\nPath distribution:")
		t.Logf("  Main path (%s): %d goroutines", sessionID, mainCount)

		for path, goroutines := range pathDistribution {
			if path != mainPath {
				t.Logf("  Sibling %s: %d goroutines (IDs: %v)",
					GetSessionID(path), len(goroutines), goroutines)
			}
		}

		// If we got siblings, that's actually good - it means conflict detection worked
		siblingCount := len(pathDistribution) - 1
		if siblingCount > 0 {
			t.Logf("\n✓ Conflict detection worked! Created %d sibling paths", siblingCount)
		}

		// Verify final lineage for each path
		t.Logf("\nVerifying lineages:")
		for path := range pathDistribution {
			lineage, err := GetLineage(path)
			if err != nil {
				t.Errorf("Failed to get lineage for %s: %v", path, err)
				continue
			}

			// Should have initial 3 messages + new assistant responses
			if len(lineage) < 4 {
				t.Errorf("Path %s has too few messages: %d", GetSessionID(path), len(lineage))
			}

			// Count assistant messages
			assistantCount := 0
			for _, msg := range lineage {
				if msg.Role == "assistant" {
					assistantCount++
				}
			}

			t.Logf("  %s: %d total messages (%d assistant)",
				GetSessionID(path), len(lineage), assistantCount)
		}
	})
}

// TestStressRapidFileCreation tests extremely rapid message creation
func TestStressRapidFileCreation(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		const numMessages = 100
		const numGoroutines = 20

		var wg sync.WaitGroup
		errors := make(chan error, numMessages)
		conflicts := make(chan string, numMessages)

		wg.Add(numGoroutines)

		startTime := time.Now()

		for g := 0; g < numGoroutines; g++ {
			go func(goroutineID int) {
				defer wg.Done()

				for i := 0; i < numMessages/numGoroutines; i++ {
					msg := Message{
						Role:      "user",
						Content:   fmt.Sprintf("G%d-M%d", goroutineID, i),
						Timestamp: time.Now(),
					}

					path, _, err := AppendMessage(session, msg)
					if err != nil {
						errors <- fmt.Errorf("G%d-M%d: %w", goroutineID, i, err)
					} else if path != session.Path {
						conflicts <- fmt.Sprintf("G%d-M%d -> %s", goroutineID, i, GetSessionID(path))
					}
				}
			}(g)
		}

		wg.Wait()
		close(errors)
		close(conflicts)

		duration := time.Since(startTime)

		// Count errors and conflicts
		errorCount := 0
		for err := range errors {
			errorCount++
			t.Errorf("Error: %v", err)
		}

		conflictList := []string{}
		for conflict := range conflicts {
			conflictList = append(conflictList, conflict)
		}

		t.Logf("\n=== Rapid Creation Results ===")
		t.Logf("Duration: %v", duration)
		t.Logf("Messages per second: %.2f", float64(numMessages)/duration.Seconds())
		t.Logf("Errors: %d/%d", errorCount, numMessages)
		t.Logf("Conflicts requiring siblings: %d", len(conflictList))

		if len(conflictList) > 0 {
			t.Logf("\nFirst 10 conflicts:")
			for i := 0; i < 10 && i < len(conflictList); i++ {
				t.Logf("  %s", conflictList[i])
			}
		}
	})
}

// TestExtremeScenario tests the system under extreme load
func TestExtremeScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping extreme stress test in short mode")
	}

	WithTestSessionDir(t, func(sessionsDir string) {
		// Create multiple sessions
		const numSessions = 5
		const numGoroutinesPerSession = 10
		const numMessagesPerGoroutine = 20

		sessions := make([]*Session, numSessions)
		for i := 0; i < numSessions; i++ {
			session, err := CreateSession()
			if err != nil {
				t.Fatalf("Failed to create session %d: %v", i, err)
			}
			sessions[i] = session
		}

		var wg sync.WaitGroup
		totalOps := numSessions * numGoroutinesPerSession * numMessagesPerGoroutine
		results := make(chan struct {
			sessionID   int
			goroutineID int
			success     int
			conflicts   int
			errors      int
		}, numSessions*numGoroutinesPerSession)

		wg.Add(numSessions * numGoroutinesPerSession)

		startTime := time.Now()

		// Launch goroutines for each session
		for s := 0; s < numSessions; s++ {
			for g := 0; g < numGoroutinesPerSession; g++ {
				go func(sessionID, goroutineID int) {
					defer wg.Done()

					session := sessions[sessionID]
					success := 0
					conflicts := 0
					errors := 0

					for m := 0; m < numMessagesPerGoroutine; m++ {
						msg := Message{
							Role:      "user",
							Content:   fmt.Sprintf("S%d-G%d-M%d", sessionID, goroutineID, m),
							Timestamp: time.Now(),
						}

						path, _, err := AppendMessage(session, msg)
						if err != nil {
							errors++
						} else if path != session.Path {
							conflicts++
						} else {
							success++
						}
					}

					results <- struct {
						sessionID   int
						goroutineID int
						success     int
						conflicts   int
						errors      int
					}{sessionID, goroutineID, success, conflicts, errors}
				}(s, g)
			}
		}

		wg.Wait()
		close(results)

		duration := time.Since(startTime)

		// Aggregate results
		totalSuccess := 0
		totalConflicts := 0
		totalErrors := 0

		for result := range results {
			totalSuccess += result.success
			totalConflicts += result.conflicts
			totalErrors += result.errors
		}

		t.Logf("\n=== Extreme Scenario Results ===")
		t.Logf("Duration: %v", duration)
		t.Logf("Total operations: %d", totalOps)
		t.Logf("Operations per second: %.2f", float64(totalOps)/duration.Seconds())
		t.Logf("Success (main path): %d (%.1f%%)", totalSuccess, float64(totalSuccess)/float64(totalOps)*100)
		t.Logf("Conflicts (siblings): %d (%.1f%%)", totalConflicts, float64(totalConflicts)/float64(totalOps)*100)
		t.Logf("Errors: %d (%.1f%%)", totalErrors, float64(totalErrors)/float64(totalOps)*100)

		if totalErrors > 0 {
			t.Errorf("System had %d errors under extreme load", totalErrors)
		}
	})
}

// TestUserReportedBugScenario specifically tests the scenario from the user's bug report
func TestUserReportedBugScenario(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create session and add initial messages like in the bug report
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// The bug showed IDs like 000c, 000d, suggesting there were already messages
		for i := 0; i < 12; i++ {
			msg := Message{
				Role:      "user",
				Content:   fmt.Sprintf("Message %d", i),
				Timestamp: time.Now(),
			}
			if _, _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message %d: %v", i, err)
			}
		}

		sessionPath := session.Path

		// Simulate the concurrent resume scenario with subprocess-like behavior
		const numProcesses = 3
		type result struct {
			processID int
			msgID     string
			lockFile  string
			err       error
		}

		results := make([]result, numProcesses)
		var wg sync.WaitGroup
		wg.Add(numProcesses)

		// Each "process" tries to resume and append
		for p := 0; p < numProcesses; p++ {
			go func(processID int) {
				defer wg.Done()

				// Simulate loading session (like -resume)
				session := &Session{Path: sessionPath}

				// Try to append message
				msg := Message{
					Role:      "assistant",
					Content:   fmt.Sprintf("Response from process %d", processID),
					Timestamp: time.Now(),
					Model:     "gpt4o",
				}

				_, msgID, err := AppendMessage(session, msg)

				// Check for lock files (they should exist)
				lockFile := ""
				entries, _ := os.ReadDir(sessionPath)
				for _, entry := range entries {
					if strings.HasSuffix(entry.Name(), ".lock") {
						lockFile = entry.Name()
						break
					}
				}

				results[processID] = result{
					processID: processID,
					msgID:     msgID,
					lockFile:  lockFile,
					err:       err,
				}
			}(p)
		}

		wg.Wait()

		// Analyze results
		t.Logf("\n=== Bug Scenario Recreation ===")
		t.Logf("Session path: %s", sessionPath)

		for _, r := range results {
			if r.err != nil {
				t.Logf("Process %d: ERROR: %v", r.processID, r.err)
			} else {
				t.Logf("Process %d: Created message %s (lock file seen: %s)",
					r.processID, r.msgID, r.lockFile)
			}
		}

		// Check for lock files
		entries, _ := os.ReadDir(sessionPath)
		lockFiles := []string{}
		messageFiles := []string{}

		for _, entry := range entries {
			name := entry.Name()
			if strings.HasSuffix(name, ".lock") {
				lockFiles = append(lockFiles, name)
			} else if strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".json") {
				messageFiles = append(messageFiles, name)
			}
		}

		t.Logf("\nLock files present: %v", lockFiles)
		t.Logf("Message files: %d", len(messageFiles))

		// Verify no message ID conflicts
		msgIDs := make(map[string]bool)
		for _, r := range results {
			if r.msgID != "" {
				if msgIDs[r.msgID] {
					t.Errorf("CRITICAL: Message ID conflict detected! ID %s used multiple times", r.msgID)
				}
				msgIDs[r.msgID] = true
			}
		}

		// Get final lineage
		lineage, err := GetLineage(sessionPath)
		if err != nil {
			t.Errorf("Failed to get lineage: %v", err)
		} else {
			t.Logf("\nFinal lineage: %d messages", len(lineage))
			// Show last few messages
			start := len(lineage) - 5
			if start < 0 {
				start = 0
			}
			for i := start; i < len(lineage); i++ {
				t.Logf("  [%d] %s: %.50s...", i, lineage[i].Role, lineage[i].Content)
			}
		}
	})
}
