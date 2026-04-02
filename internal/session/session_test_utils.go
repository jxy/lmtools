package session

import (
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMessage creates a test message with default values
func TestMessage(role, content string) Message {
	return Message{
		Role:      core.Role(role),
		Content:   content,
		Timestamp: time.Now(),
	}
}

// WithTestSessionDir runs a test with a temporary session directory
func WithTestSessionDir(t *testing.T, fn func(sessionsDir string)) {
	t.Helper()

	tmpDir := t.TempDir()
	testSessionsDir := filepath.Join(tmpDir, "sessions")

	// Override the sessions directory for testing
	oldDir := GetSessionsDir()
	SetSessionsDir(testSessionsDir)
	t.Cleanup(func() {
		SetSessionsDir(oldDir)
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

	msg, err := readMessage(sessionPath, msgID)
	if err != nil {
		t.Fatalf("Failed to read message %s: %v", msgID, err)
	}

	if string(msg.Role) != expectedRole {
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
