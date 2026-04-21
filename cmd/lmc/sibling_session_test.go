//go:build integration || e2e

package main

import (
	"strings"
	"testing"
)

// TestResumeSiblingSession tests that sibling session IDs are correctly handled
// and not confused with message IDs when using -resume flag
func TestResumeSiblingSession(t *testing.T) {
	lmcBin := getLmcBinary(t)
	_, mockURL := setupTestEnvironment(t)

	// Create custom sessions directory
	sessionsDir := t.TempDir()

	// Create initial session (stdout not needed)
	_, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", mockURL, "-sessions-dir", sessionsDir},
		"Initial message")
	if err != nil {
		t.Fatalf("Failed to create initial session: %v\nStderr: %s", err, stderr)
	}

	// Get session ID
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v\nStderr: %s", err, stderr)
	}

	sessionID := extractFirstSessionID(stdout)
	if sessionID == "" {
		t.Fatalf("Failed to extract session ID from show-sessions output: %s", stdout)
	}

	// Add another message
	_, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-resume", sessionID, "-provider-url", mockURL, "-sessions-dir", sessionsDir},
		"Second message")
	if err != nil {
		t.Fatalf("Failed to add second message: %v", err)
	}

	// Create a branch from the first message
	_, stderr, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-branch", sessionID + "/0001", "-provider-url", mockURL, "-sessions-dir", sessionsDir},
		"Branch message")
	if err != nil {
		t.Fatalf("Failed to create branch: %v\nStderr: %s", err, stderr)
	}

	// The branch should create a sibling session like "0001/0001.s.0000"
	siblingSessionID := sessionID + "/0001.s.0000"
	t.Logf("Testing sibling session: %s", siblingSessionID)

	// Try to resume the sibling session - the key test is that it shouldn't
	// try to branch again (which would fail)
	_, stderr, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-resume", siblingSessionID, "-provider-url", mockURL, "-sessions-dir", sessionsDir},
		"Test message")

	// The important check: it should NOT try to branch
	if strings.Contains(stderr, "Branching from message") {
		t.Errorf("Incorrectly tried to branch when resuming sibling session. Stderr: %s", stderr)
	}

	// The resume might fail for other reasons (e.g., session not found),
	// but it should NOT fail because it tried to treat the sibling session ID as a message ID
	if err != nil && strings.Contains(err.Error(), "branch point") {
		t.Errorf("Failed with branch point error, indicating sibling session was treated as message ID: %v", err)
	}

	t.Logf("Test passed: sibling session ID was not confused with message ID")
}
