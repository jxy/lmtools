package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"testing"
	"time"
)

// mockConfigForForkTest implements RequestConfig for testing fork logic
type mockConfigForForkTest struct {
	resume              string
	systemPrompt        string
	systemExplicitlySet bool
	enableTool          bool
}

func (m *mockConfigForForkTest) GetUser() string             { return "testuser" }
func (m *mockConfigForForkTest) GetModel() string            { return "test-model" }
func (m *mockConfigForForkTest) GetSystem() string           { return m.systemPrompt }
func (m *mockConfigForForkTest) IsSystemExplicitlySet() bool { return m.systemExplicitlySet }
func (m *mockConfigForForkTest) GetEffectiveSystem() string {
	// Mimic the real GetEffectiveSystem logic
	if m.enableTool && !m.systemExplicitlySet {
		return prompts.ToolSystemPrompt
	}
	return m.systemPrompt
}
func (m *mockConfigForForkTest) GetEnv() string                { return "test" }
func (m *mockConfigForForkTest) IsEmbed() bool                 { return false }
func (m *mockConfigForForkTest) IsStreamChat() bool            { return false }
func (m *mockConfigForForkTest) GetProvider() string           { return "test" }
func (m *mockConfigForForkTest) GetProviderURL() string        { return "http://test" }
func (m *mockConfigForForkTest) GetAPIKeyFile() string         { return "" }
func (m *mockConfigForForkTest) IsToolEnabled() bool           { return m.enableTool }
func (m *mockConfigForForkTest) GetToolTimeout() time.Duration { return 30 * time.Second }
func (m *mockConfigForForkTest) GetToolWhitelist() string      { return "" }
func (m *mockConfigForForkTest) GetToolBlacklist() string      { return "" }
func (m *mockConfigForForkTest) GetToolAutoApprove() bool      { return false }
func (m *mockConfigForForkTest) GetToolNonInteractive() bool   { return false }
func (m *mockConfigForForkTest) GetMaxToolRounds() int         { return 10 }
func (m *mockConfigForForkTest) GetMaxToolParallel() int       { return 4 }
func (m *mockConfigForForkTest) GetToolMaxOutputBytes() int    { return 1048576 }
func (m *mockConfigForForkTest) GetResume() string             { return m.resume }
func (m *mockConfigForForkTest) GetBranch() string             { return "" }

// mockNotifierForForkTest tracks fork notifications
type mockNotifierForForkTest struct {
	forked   bool
	messages []string
}

func (m *mockNotifierForForkTest) Infof(format string, args ...interface{}) {
	msg := format
	if len(args) > 0 {
		// Simple handling for our test case
		if containsStr(format, "Forked session") {
			m.forked = true
		}
	}
	m.messages = append(m.messages, msg)
}
func (m *mockNotifierForForkTest) Debugf(format string, args ...interface{})  {}
func (m *mockNotifierForForkTest) Warnf(format string, args ...interface{})   {}
func (m *mockNotifierForForkTest) Errorf(format string, args ...interface{})  {}
func (m *mockNotifierForForkTest) Promptf(format string, args ...interface{}) {}

// TestCoordinatorForkingLogic tests all scenarios of the session forking logic
func TestCoordinatorForkingLogic(t *testing.T) {
	// Setup test environment
	tempDir := t.TempDir()
	SetSessionsDir(tempDir)
	SetSkipFlockCheck(true)
	defer SetSessionsDir("")
	defer SetSkipFlockCheck(false)

	ctx := context.Background()
	var log core.Logger // nil logger for testing

	// Test Case 1: System prompt specified on command line
	t.Run("ExplicitSystemPrompt", func(t *testing.T) {
		// Sub-case 1a: Specified prompt differs from session prompt
		t.Run("DifferentPrompt_ShouldFork", func(t *testing.T) {
			// Create session with custom prompt
			sess, err := CreateSession("Original prompt", log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message to the session
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Resume with different explicit prompt
			cfg := &mockConfigForForkTest{
				resume:              sessionID,
				systemPrompt:        "New different prompt",
				systemExplicitlySet: true,
			}
			notifier := &mockNotifierForForkTest{}

			coordinator := NewCoordinator(cfg, notifier)
			result, err := coordinator.PrepareSession(ctx, "test", false, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Should fork
			newSessionID := GetSessionID(result.Session.Path)
			if newSessionID == sessionID {
				t.Error("Expected fork but session was not forked")
			}
			if !notifier.forked {
				t.Error("Expected fork notification")
			}

			// Verify new session has the specified prompt
			newSystemMsg, err := GetSystemMessage(result.Session.Path)
			if err != nil {
				t.Fatal(err)
			}
			if newSystemMsg == nil || *newSystemMsg != "New different prompt" {
				t.Errorf("New session should have specified prompt, got: %v", newSystemMsg)
			}
		})

		// Sub-case 1b: Specified prompt matches session prompt
		t.Run("SamePrompt_ShouldNotFork", func(t *testing.T) {
			// Create session with custom prompt
			customPrompt := "My custom prompt"
			sess, err := CreateSession(customPrompt, log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Resume with same explicit prompt
			cfg := &mockConfigForForkTest{
				resume:              sessionID,
				systemPrompt:        customPrompt,
				systemExplicitlySet: true,
			}
			notifier := &mockNotifierForForkTest{}

			coordinator := NewCoordinator(cfg, notifier)
			result, err := coordinator.PrepareSession(ctx, "test", false, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Should NOT fork
			newSessionID := GetSessionID(result.Session.Path)
			if newSessionID != sessionID {
				t.Errorf("Should not fork when prompts match. Original: %s, New: %s", sessionID, newSessionID)
			}
			if notifier.forked {
				t.Error("Should not have fork notification")
			}
		})
	})

	// Test Case 2: No system prompt specified on command line
	t.Run("NoExplicitSystemPrompt", func(t *testing.T) {
		// Sub-case 2a: Session has default non-tool prompt, now using tools
		t.Run("DefaultNonToolPrompt_WithTools_ShouldFork", func(t *testing.T) {
			// Create session with default non-tool prompt
			sess, err := CreateSession(prompts.DefaultSystemPrompt, log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Resume with tools enabled (no explicit system prompt)
			cfg := &mockConfigForForkTest{
				resume:              sessionID,
				systemPrompt:        prompts.DefaultSystemPrompt, // This is what the config would return
				systemExplicitlySet: false,
				enableTool:          true,
			}
			notifier := &mockNotifierForForkTest{}

			coordinator := NewCoordinator(cfg, notifier)
			result, err := coordinator.PrepareSession(ctx, "test", false, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Should fork to use tool prompt
			newSessionID := GetSessionID(result.Session.Path)
			if newSessionID == sessionID {
				t.Error("Expected fork when upgrading from default non-tool to tool prompt")
			}
			if !notifier.forked {
				t.Error("Expected fork notification")
			}

			// Verify new session has tool prompt
			newSystemMsg, err := GetSystemMessage(result.Session.Path)
			if err != nil {
				t.Fatal(err)
			}
			if newSystemMsg == nil || *newSystemMsg != prompts.ToolSystemPrompt {
				t.Errorf("New session should have tool prompt, got: %v", newSystemMsg)
			}
		})

		// Sub-case 2b: Session has default non-tool prompt, not using tools
		t.Run("DefaultNonToolPrompt_NoTools_ShouldNotFork", func(t *testing.T) {
			// Create session with default non-tool prompt
			sess, err := CreateSession(prompts.DefaultSystemPrompt, log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Resume without tools
			cfg := &mockConfigForForkTest{
				resume:              sessionID,
				systemPrompt:        prompts.DefaultSystemPrompt,
				systemExplicitlySet: false,
				enableTool:          false,
			}
			notifier := &mockNotifierForForkTest{}

			coordinator := NewCoordinator(cfg, notifier)
			result, err := coordinator.PrepareSession(ctx, "test", false, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Should NOT fork
			newSessionID := GetSessionID(result.Session.Path)
			if newSessionID != sessionID {
				t.Errorf("Should not fork. Original: %s, New: %s", sessionID, newSessionID)
			}
			if notifier.forked {
				t.Error("Should not have fork notification")
			}
		})

		// Sub-case 2c: Session has default tool prompt
		t.Run("DefaultToolPrompt_ShouldNotFork", func(t *testing.T) {
			// Create session with tool prompt
			sess, err := CreateSession(prompts.ToolSystemPrompt, log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Test both with and without tools - neither should fork
			for _, enableTool := range []bool{true, false} {
				testName := "WithTools"
				if !enableTool {
					testName = "WithoutTools"
				}

				t.Run(testName, func(t *testing.T) {
					cfg := &mockConfigForForkTest{
						resume:              sessionID,
						systemPrompt:        prompts.DefaultSystemPrompt,
						systemExplicitlySet: false,
						enableTool:          enableTool,
					}
					notifier := &mockNotifierForForkTest{}

					coordinator := NewCoordinator(cfg, notifier)
					result, err := coordinator.PrepareSession(ctx, "test", false, nil)
					if err != nil {
						t.Fatal(err)
					}

					// Should NOT fork
					newSessionID := GetSessionID(result.Session.Path)
					if newSessionID != sessionID {
						t.Errorf("Should not fork with tool prompt. Original: %s, New: %s", sessionID, newSessionID)
					}
					if notifier.forked {
						t.Error("Should not have fork notification")
					}
				})
			}
		})

		// Sub-case 2d: Session has custom prompt
		t.Run("CustomPrompt_ShouldNotFork", func(t *testing.T) {
			// Create session with custom prompt
			customPrompt := "You are a specialized assistant for testing"
			sess, err := CreateSession(customPrompt, log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Test both with and without tools - neither should fork
			for _, enableTool := range []bool{true, false} {
				testName := "WithTools"
				if !enableTool {
					testName = "WithoutTools"
				}

				t.Run(testName, func(t *testing.T) {
					cfg := &mockConfigForForkTest{
						resume:              sessionID,
						systemPrompt:        prompts.DefaultSystemPrompt,
						systemExplicitlySet: false,
						enableTool:          enableTool,
					}
					notifier := &mockNotifierForForkTest{}

					coordinator := NewCoordinator(cfg, notifier)
					result, err := coordinator.PrepareSession(ctx, "test", false, nil)
					if err != nil {
						t.Fatal(err)
					}

					// Should NOT fork (preserve custom prompt)
					newSessionID := GetSessionID(result.Session.Path)
					if newSessionID != sessionID {
						t.Errorf("Should not fork with custom prompt. Original: %s, New: %s", sessionID, newSessionID)
					}
					if notifier.forked {
						t.Error("Should not have fork notification")
					}

					// Verify custom prompt is preserved
					systemMsg, err := GetSystemMessage(result.Session.Path)
					if err != nil {
						t.Fatal(err)
					}
					if systemMsg == nil || *systemMsg != customPrompt {
						t.Errorf("Custom prompt should be preserved, got: %v", systemMsg)
					}
				})
			}
		})

		// Sub-case 2e: Session has no system prompt, using tools
		t.Run("NoPrompt_WithTools_ShouldFork", func(t *testing.T) {
			// Create session with no prompt
			sess, err := CreateSession("", log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Resume with tools
			cfg := &mockConfigForForkTest{
				resume:              sessionID,
				systemPrompt:        prompts.DefaultSystemPrompt,
				systemExplicitlySet: false,
				enableTool:          true,
			}
			notifier := &mockNotifierForForkTest{}

			coordinator := NewCoordinator(cfg, notifier)
			result, err := coordinator.PrepareSession(ctx, "test", false, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Should fork to add tool prompt
			newSessionID := GetSessionID(result.Session.Path)
			if newSessionID == sessionID {
				t.Error("Expected fork when adding tool prompt to session without prompt")
			}
			if !notifier.forked {
				t.Error("Expected fork notification")
			}

			// Verify new session has tool prompt
			newSystemMsg, err := GetSystemMessage(result.Session.Path)
			if err != nil {
				t.Fatal(err)
			}
			if newSystemMsg == nil || *newSystemMsg != prompts.ToolSystemPrompt {
				t.Errorf("New session should have tool prompt, got: %v", newSystemMsg)
			}
		})

		// Sub-case 2f: Session has no system prompt, not using tools
		t.Run("NoPrompt_NoTools_ShouldNotFork", func(t *testing.T) {
			// Create session with no prompt
			sess, err := CreateSession("", log)
			if err != nil {
				t.Fatal(err)
			}
			sessionID := GetSessionID(sess.Path)

			// Add a message
			_, err = AppendMessageWithToolInteraction(ctx, sess, Message{
				Role:      core.RoleUser,
				Content:   "Hello",
				Timestamp: time.Now(),
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Resume without tools
			cfg := &mockConfigForForkTest{
				resume:              sessionID,
				systemPrompt:        prompts.DefaultSystemPrompt,
				systemExplicitlySet: false,
				enableTool:          false,
			}
			notifier := &mockNotifierForForkTest{}

			coordinator := NewCoordinator(cfg, notifier)
			result, err := coordinator.PrepareSession(ctx, "test", false, nil)
			if err != nil {
				t.Fatal(err)
			}

			// Should NOT fork (keeping no prompt is valid)
			newSessionID := GetSessionID(result.Session.Path)
			if newSessionID != sessionID {
				t.Errorf("Should not fork. Original: %s, New: %s", sessionID, newSessionID)
			}
			if notifier.forked {
				t.Error("Should not have fork notification")
			}
		})
	})
}

// TestCoordinatorForkingEdgeCases tests edge cases and error conditions
func TestCoordinatorForkingEdgeCases(t *testing.T) {
	// Setup test environment
	tempDir := t.TempDir()
	SetSessionsDir(tempDir)
	SetSkipFlockCheck(true)
	defer SetSessionsDir("")
	defer SetSkipFlockCheck(false)

	ctx := context.Background()
	var log core.Logger

	t.Run("NoResumeFlag_ShouldNotCheckForking", func(t *testing.T) {
		// Create new session (no resume)
		cfg := &mockConfigForForkTest{
			resume:              "", // No resume
			systemPrompt:        "Test prompt",
			systemExplicitlySet: true,
		}
		notifier := &mockNotifierForForkTest{}

		coordinator := NewCoordinator(cfg, notifier)
		result, err := coordinator.PrepareSession(ctx, "test", false, nil)
		if err != nil {
			t.Fatal(err)
		}

		// Should create new session, not fork
		if notifier.forked {
			t.Error("Should not have fork notification when creating new session")
		}

		// Verify session was created with the prompt
		systemMsg, err := GetSystemMessage(result.Session.Path)
		if err != nil {
			t.Fatal(err)
		}
		if systemMsg == nil || *systemMsg != "Test prompt" {
			t.Errorf("New session should have specified prompt, got: %v", systemMsg)
		}
	})

	t.Run("ExplicitEmptyPrompt_ShouldFork", func(t *testing.T) {
		// Create session with a prompt
		sess, err := CreateSession("Original prompt", log)
		if err != nil {
			t.Fatal(err)
		}
		sessionID := GetSessionID(sess.Path)

		// Resume with explicitly set empty prompt
		cfg := &mockConfigForForkTest{
			resume:              sessionID,
			systemPrompt:        "", // Empty prompt
			systemExplicitlySet: true,
		}
		notifier := &mockNotifierForForkTest{}

		coordinator := NewCoordinator(cfg, notifier)
		result, err := coordinator.PrepareSession(ctx, "test", false, nil)
		if err != nil {
			t.Fatal(err)
		}

		// Should fork to remove prompt
		newSessionID := GetSessionID(result.Session.Path)
		if newSessionID == sessionID {
			t.Error("Expected fork when explicitly setting empty prompt")
		}
		if !notifier.forked {
			t.Error("Expected fork notification")
		}

		// Verify new session has no prompt
		newSystemMsg, err := GetSystemMessage(result.Session.Path)
		if err != nil {
			t.Fatal(err)
		}
		if newSystemMsg != nil {
			t.Errorf("New session should have no prompt, got: %v", newSystemMsg)
		}
	})
}
