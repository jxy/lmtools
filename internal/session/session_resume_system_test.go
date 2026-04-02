package session

import (
	"lmtools/internal/core"
	"testing"
	"time"
)

// TestResumeSessionWithCustomSystemNoFork tests that resuming a session with a custom
// system message doesn't fork when no -s flag is provided
func TestResumeSessionWithCustomSystemNoFork(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
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
		cfg := newTestCoordinatorConfig()
		cfg.Resume = sessionID
		cfg.System = ""
		notifier := core.NewTestNotifier()

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
		if infoMessagesContain(notifier, "Forked session") {
			t.Errorf("Unexpected fork message logged: %v", notifier.InfoMessages)
		}
	})

	// Test 2: Resume with same system prompt via -s flag (should NOT fork)
	t.Run("ResumeWithSameSystemFlag", func(t *testing.T) {
		cfg := newTestCoordinatorConfig()
		cfg.Resume = sessionID
		cfg.System = customSystem
		cfg.SystemExplicitlySet = true
		notifier := core.NewTestNotifier()

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
		if infoMessagesContain(notifier, "Forked session") {
			t.Errorf("Unexpected fork message logged: %v", notifier.InfoMessages)
		}
	})

	// Test 3: Resume with different system prompt via -s flag (SHOULD fork)
	t.Run("ResumeWithDifferentSystemFlag", func(t *testing.T) {
		differentSystem := "You are a completely different assistant."
		cfg := newTestCoordinatorConfig()
		cfg.Resume = sessionID
		cfg.System = differentSystem
		cfg.SystemExplicitlySet = true
		notifier := core.NewTestNotifier()

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
		if !infoMessagesContain(notifier, "Forked session") {
			t.Errorf("Expected fork message not logged")
		}
	})
}

// TestResumeSessionWithDefaultSystem tests resuming a session that has the default system prompt
func TestResumeSessionWithDefaultSystem(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
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
		cfg := newTestCoordinatorConfig()
		cfg.Resume = sessionID
		cfg.System = ""
		notifier := core.NewTestNotifier()

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
