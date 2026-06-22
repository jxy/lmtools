package session

import (
	"os"
	"path/filepath"
	"testing"
)

// NewTestManager returns a session manager rooted in a test-owned temp
// directory. New tests should prefer this over mutating the package default
// manager.
func NewTestManager(t *testing.T) (*Manager, string) {
	t.Helper()

	testSessionsDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(testSessionsDir, 0o750); err != nil {
		t.Fatalf("Failed to create test sessions directory: %v", err)
	}

	manager := NewManager(testSessionsDir)
	manager.SetSkipFlockCheck(true)
	return manager, testSessionsDir
}

func WithTestManager(t *testing.T, fn func(manager *Manager, sessionsDir string)) {
	t.Helper()

	manager, sessionsDir := NewTestManager(t)
	fn(manager, sessionsDir)
}

func UseTestSessionDir(t *testing.T) string {
	t.Helper()

	_, testSessionsDir := NewTestManager(t)

	// Override the sessions directory for testing
	oldDir := GetSessionsDir()
	SetSessionsDir(testSessionsDir)
	SetSkipFlockCheck(true)
	t.Cleanup(func() {
		SetSessionsDir(oldDir)
		SetSkipFlockCheck(false)
	})

	return testSessionsDir
}

// WithTestSessionDir runs a test with a temporary session directory
func WithTestSessionDir(t *testing.T, fn func(sessionsDir string)) {
	t.Helper()
	fn(UseTestSessionDir(t))
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
