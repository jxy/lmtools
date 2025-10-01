package session

import (
	"context"
	"lmtools/internal/core"
	"testing"
	"time"
)

// mockConfigForResume implements the minimal RequestConfig interface for testing
type mockConfigForResume struct {
	resume              string
	system              string
	systemExplicitlySet bool
	enableTool          bool
}

func (m *mockConfigForResume) GetUser() string               { return "testuser" }
func (m *mockConfigForResume) GetModel() string              { return "test-model" }
func (m *mockConfigForResume) GetSystem() string             { return m.system }
func (m *mockConfigForResume) IsSystemExplicitlySet() bool   { return m.systemExplicitlySet }
func (m *mockConfigForResume) GetEffectiveSystem() string    { return m.system }
func (m *mockConfigForResume) GetEnv() string                { return "test" }
func (m *mockConfigForResume) IsEmbed() bool                 { return false }
func (m *mockConfigForResume) IsStreamChat() bool            { return false }
func (m *mockConfigForResume) GetProvider() string           { return "test" }
func (m *mockConfigForResume) GetProviderURL() string        { return "http://test" }
func (m *mockConfigForResume) GetAPIKeyFile() string         { return "" }
func (m *mockConfigForResume) IsToolEnabled() bool           { return m.enableTool }
func (m *mockConfigForResume) GetToolTimeout() time.Duration { return 30 * time.Second }
func (m *mockConfigForResume) GetToolWhitelist() string      { return "" }
func (m *mockConfigForResume) GetToolBlacklist() string      { return "" }
func (m *mockConfigForResume) GetToolAutoApprove() bool      { return false }
func (m *mockConfigForResume) GetToolNonInteractive() bool   { return false }
func (m *mockConfigForResume) GetMaxToolRounds() int         { return 10 }
func (m *mockConfigForResume) GetMaxToolParallel() int       { return 4 }
func (m *mockConfigForResume) GetToolMaxOutputBytes() int    { return 1048576 }
func (m *mockConfigForResume) GetResume() string             { return m.resume }
func (m *mockConfigForResume) GetBranch() string             { return "" }

// mockResumeNotifier implements the Notifier interface for testing
type mockResumeNotifier struct {
	messages []string
}

func (m *mockResumeNotifier) Infof(format string, args ...interface{}) {
	// Store messages for verification
	m.messages = append(m.messages, format)
}

func (m *mockResumeNotifier) Warnf(format string, args ...interface{})  {}
func (m *mockResumeNotifier) Errorf(format string, args ...interface{}) {}
func (m *mockResumeNotifier) Promptf(format string, args ...interface{}) {
	// Do nothing for prompts in tests
}

// TestResumeSessionWithCustomSystemNoFork tests that resuming a session with a custom
// system message doesn't fork when no -s flag is provided
func TestResumeSessionWithCustomSystemNoFork(t *testing.T) {
	// Setup test environment
	tempDir := t.TempDir()
	SetSessionsDir(tempDir)
	SetSkipFlockCheck(true)
	defer SetSessionsDir("")
	defer SetSkipFlockCheck(false)

	ctx := context.Background()
	// Use a nil logger for testing
	var log core.Logger

	// Create a session with a custom system prompt
	customSystem := "You are a helpful test assistant with special instructions."
	sess1, err := CreateSession(customSystem, log)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a user message to the session
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "Hello",
		Timestamp: time.Now(),
	}
	result, err := AppendMessageWithToolInteraction(ctx, sess1, userMsg, nil, nil)
	if err != nil {
		t.Fatalf("Failed to append user message: %v", err)
	}

	// Add an assistant message
	assistantMsg := Message{
		Role:      core.RoleAssistant,
		Content:   "Hi there!",
		Timestamp: time.Now(),
		Model:     "test-model",
	}
	result, err = AppendMessageWithToolInteraction(ctx, sess1, assistantMsg, nil, nil)
	if err != nil {
		t.Fatalf("Failed to append assistant message: %v", err)
	}

	sessionID := GetSessionID(result.Path)

	// Test 1: Resume without -s flag (should NOT fork)
	t.Run("ResumeWithoutSystemFlag", func(t *testing.T) {
		cfg := &mockConfigForResume{
			resume:              sessionID,
			system:              "", // No system prompt provided
			systemExplicitlySet: false,
		}
		notifier := &mockResumeNotifier{}

		coordinator := NewCoordinator(cfg, notifier)
		prepResult, err := coordinator.PrepareSession(ctx, "Continue", false, nil)
		if err != nil {
			t.Fatalf("Failed to prepare session: %v", err)
		}

		// Verify no fork occurred
		newSessionID := GetSessionID(prepResult.Session.Path)
		if newSessionID != sessionID {
			t.Errorf("Session was forked unexpectedly. Original: %s, New: %s", sessionID, newSessionID)
		}

		// Check that no fork message was logged
		for _, msg := range notifier.messages {
			if containsStr(msg, "Forked session") {
				t.Errorf("Unexpected fork message logged: %s", msg)
			}
		}
	})

	// Test 2: Resume with same system prompt via -s flag (should NOT fork)
	t.Run("ResumeWithSameSystemFlag", func(t *testing.T) {
		cfg := &mockConfigForResume{
			resume:              sessionID,
			system:              customSystem, // Same system prompt
			systemExplicitlySet: true,
		}
		notifier := &mockResumeNotifier{}

		coordinator := NewCoordinator(cfg, notifier)
		prepResult, err := coordinator.PrepareSession(ctx, "Continue", false, nil)
		if err != nil {
			t.Fatalf("Failed to prepare session: %v", err)
		}

		// Verify no fork occurred
		newSessionID := GetSessionID(prepResult.Session.Path)
		if newSessionID != sessionID {
			t.Errorf("Session was forked unexpectedly. Original: %s, New: %s", sessionID, newSessionID)
		}

		// Check that no fork message was logged
		for _, msg := range notifier.messages {
			if containsStr(msg, "Forked session") {
				t.Errorf("Unexpected fork message logged: %s", msg)
			}
		}
	})

	// Test 3: Resume with different system prompt via -s flag (SHOULD fork)
	t.Run("ResumeWithDifferentSystemFlag", func(t *testing.T) {
		differentSystem := "You are a completely different assistant."
		cfg := &mockConfigForResume{
			resume:              sessionID,
			system:              differentSystem, // Different system prompt
			systemExplicitlySet: true,
		}
		notifier := &mockResumeNotifier{}

		coordinator := NewCoordinator(cfg, notifier)
		prepResult, err := coordinator.PrepareSession(ctx, "Continue", false, nil)
		if err != nil {
			t.Fatalf("Failed to prepare session: %v", err)
		}

		// Verify fork occurred
		newSessionID := GetSessionID(prepResult.Session.Path)
		if newSessionID == sessionID {
			t.Errorf("Session was not forked when it should have been. SessionID: %s", sessionID)
		}

		// Check that fork message was logged
		foundForkMessage := false
		for _, msg := range notifier.messages {
			if containsStr(msg, "Forked session") {
				foundForkMessage = true
				break
			}
		}
		if !foundForkMessage {
			t.Errorf("Expected fork message not logged")
		}
	})
}

// TestResumeSessionWithDefaultSystem tests resuming a session that has the default system prompt
func TestResumeSessionWithDefaultSystem(t *testing.T) {
	// Setup test environment
	tempDir := t.TempDir()
	SetSessionsDir(tempDir)
	SetSkipFlockCheck(true)
	defer SetSessionsDir("")
	defer SetSkipFlockCheck(false)

	ctx := context.Background()
	// Use a nil logger for testing
	var log core.Logger

	// Create a session without a system prompt (uses default)
	sess1, err := CreateSession("", log)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add messages
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "Hello",
		Timestamp: time.Now(),
	}
	result, err := AppendMessageWithToolInteraction(ctx, sess1, userMsg, nil, nil)
	if err != nil {
		t.Fatalf("Failed to append user message: %v", err)
	}

	sessionID := GetSessionID(result.Path)

	// Test: Resume without -s flag (should NOT fork)
	t.Run("ResumeDefaultWithoutSystemFlag", func(t *testing.T) {
		cfg := &mockConfigForResume{
			resume:              sessionID,
			system:              "", // No system prompt provided
			systemExplicitlySet: false,
		}
		notifier := &mockResumeNotifier{}

		coordinator := NewCoordinator(cfg, notifier)
		prepResult, err := coordinator.PrepareSession(ctx, "Continue", false, nil)
		if err != nil {
			t.Fatalf("Failed to prepare session: %v", err)
		}

		// Verify no fork occurred
		newSessionID := GetSessionID(prepResult.Session.Path)
		if newSessionID != sessionID {
			t.Errorf("Session was forked unexpectedly. Original: %s, New: %s", sessionID, newSessionID)
		}
	})
}

// Helper function
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
