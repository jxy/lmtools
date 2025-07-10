package argo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMessage creates a test message with default values
func TestMessage(role, content string) Message {
	return Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}
}

// createTestSession creates a test session with the given messages
func createTestSession(t *testing.T, messages []Message) *Session {
	t.Helper()

	session, err := CreateSession()
	if err != nil {
		t.Fatalf("Failed to create test session: %v", err)
	}

	for _, msg := range messages {
		if _, err := AppendMessage(session, msg); err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}
	}

	return session
}

// setupTestSessionDir creates a temporary directory for testing
func setupTestSessionDir(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")

	if err := os.MkdirAll(sessionsDir, 0o750); err != nil {
		t.Fatalf("Failed to create test sessions directory: %v", err)
	}

	return sessionsDir
}

// withTestSessionDir runs a test with a temporary session directory
func withTestSessionDir(t *testing.T, fn func(sessionsDir string)) {
	t.Helper()

	tmpDir := t.TempDir()
	testSessionsDir := filepath.Join(tmpDir, "sessions")

	// Override the sessions directory for testing
	oldDir := sessionsBaseDir
	sessionsBaseDir = testSessionsDir
	t.Cleanup(func() {
		sessionsBaseDir = oldDir
	})

	if err := os.MkdirAll(testSessionsDir, 0o750); err != nil {
		t.Fatalf("Failed to create test sessions directory: %v", err)
	}

	fn(testSessionsDir)
}

// assertFileStructure verifies that the expected files exist in a session
func assertFileStructure(t *testing.T, sessionPath string, expected []string) {
	t.Helper()

	for _, file := range expected {
		path := filepath.Join(sessionPath, file)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Expected file %s does not exist: %v", file, err)
		}
	}
}

// assertMessageContent verifies message content and metadata
func assertMessageContent(t *testing.T, sessionPath, msgID string, expectedRole, expectedContent string) {
	t.Helper()

	msg, err := ReadMessage(sessionPath, msgID)
	if err != nil {
		t.Fatalf("Failed to read message %s: %v", msgID, err)
	}

	if msg.Role != expectedRole {
		t.Errorf("Expected role %s, got %s", expectedRole, msg.Role)
	}

	if msg.Content != expectedContent {
		t.Errorf("Expected content %q, got %q", expectedContent, msg.Content)
	}
}

// assertLineageEqual compares expected and actual lineage
func assertLineageEqual(t *testing.T, expected, actual []Message) {
	t.Helper()

	if len(expected) != len(actual) {
		t.Fatalf("Lineage length mismatch: expected %d, got %d", len(expected), len(actual))
	}

	for i := range expected {
		if expected[i].Role != actual[i].Role {
			t.Errorf("Message %d role mismatch: expected %s, got %s", i, expected[i].Role, actual[i].Role)
		}
		if expected[i].Content != actual[i].Content {
			t.Errorf("Message %d content mismatch: expected %q, got %q", i, expected[i].Content, actual[i].Content)
		}
	}
}

// cleanupTestSessions removes test session directories
func cleanupTestSessions(t *testing.T, path string) {
	t.Helper()

	if err := os.RemoveAll(path); err != nil {
		t.Logf("Warning: failed to cleanup test sessions: %v", err)
	}
}
