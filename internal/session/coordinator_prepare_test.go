package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
	"testing"
	"time"
)

// TestCoordinatorPrepareSessionNewSession tests creating a new session
func TestCoordinatorPrepareSessionNewSession(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	cfg := newTestCoordinatorConfig()
	cfg.System = "You are a helpful assistant"
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	coordinator := NewCoordinator(cfg, notifier)

	// Test creating new session
	result, err := coordinator.PrepareSession(ctx, "Hello, world!", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}
	if result.ExecutedPending {
		t.Error("Expected ExecutedPending=false for new session")
	}

	// Verify session was created with system prompt
	// Verify system prompt was set correctly by checking the first message
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}
	if len(messages) > 0 && messages[0].Role == core.RoleSystem {
		if messages[0].Content != cfg.System {
			t.Errorf("Expected system=%q, got %q", cfg.System, messages[0].Content)
		}
	}

	// Verify user message was saved
	messages, err = GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages (system + user), got %d", len(messages))
	}
	if messages[0].Role != core.RoleSystem || messages[0].Content != cfg.System {
		t.Errorf("Unexpected system message: %+v", messages[0])
	}
	if messages[1].Role != core.RoleUser || messages[1].Content != "Hello, world!" {
		t.Errorf("Unexpected user message: %+v", messages[1])
	}
}

// TestCoordinatorPrepareSessionResume tests resuming an existing session
func TestCoordinatorPrepareSessionResume(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	// Create an existing session
	existingSession, err := CreateSession("Original system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message to the session
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "First message",
		Timestamp: time.Now(),
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, userMsg, nil, nil); err != nil {
		t.Fatalf("Failed to append message: %v", err)
	}

	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(existingSession.Path)
	cfg.System = "Original system prompt"
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	coordinator := NewCoordinator(cfg, notifier)

	// Test resuming session
	result, err := coordinator.PrepareSession(ctx, "Second message", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}
	if result.ExecutedPending {
		t.Error("Expected ExecutedPending=false (no pending tools)")
	}

	// Verify same session path (no fork)
	if result.Session.Path != existingSession.Path {
		t.Errorf("Expected same session path, got %s", result.Session.Path)
	}

	// Verify messages
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages (system + 2 user), got %d", len(messages))
	}
	if messages[2].Content != "Second message" {
		t.Errorf("Expected second message content='Second message', got %q", messages[2].Content)
	}
}

// TestCoordinatorPrepareSessionForkOnSystemChange tests forking when system prompt changes
func TestCoordinatorPrepareSessionForkOnSystemChange(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	// Create an existing session
	existingSession, err := CreateSession("Original system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message to the session
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "First message",
		Timestamp: time.Now(),
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, userMsg, nil, nil); err != nil {
		t.Fatalf("Failed to append message: %v", err)
	}

	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(existingSession.Path)
	cfg.System = "Different system prompt"
	cfg.SystemExplicitlySet = true
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	coordinator := NewCoordinator(cfg, notifier)

	// Test resuming with different system prompt
	result, err := coordinator.PrepareSession(ctx, "Second message", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify forked to new session
	if result.Session.Path == existingSession.Path {
		t.Error("Expected different session path after fork")
	}

	// Verify fork notification
	foundForkNotification := infoMessagesContain(notifier, "Forked session due to system prompt change")
	if !foundForkNotification {
		t.Error("Expected fork notification in notifier messages")
	}

	// Verify new session has new system prompt
	// Verify system prompt was set correctly by checking the first message
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}
	if len(messages) > 0 && messages[0].Role == core.RoleSystem {
		if messages[0].Content != cfg.System {
			t.Errorf("Expected system=%q, got %q", cfg.System, messages[0].Content)
		}
	}

	// Verify messages were copied
	messages, err = GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages in forked session (new system + copied user + new user), got %d", len(messages))
	}
}

// TestCoordinatorPrepareSessionBranch tests explicit branching
func TestCoordinatorPrepareSessionBranch(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	// Create an existing session
	existingSession, err := CreateSession("System prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add messages
	msg1 := Message{Role: core.RoleUser, Content: "Message 1", Timestamp: time.Now()}
	res1, err := AppendMessageWithToolInteraction(context.Background(), existingSession, msg1, nil, nil)
	if err != nil {
		t.Fatalf("Failed to append message 1: %v", err)
	}

	msg2 := Message{Role: core.RoleAssistant, Content: "Response 1", Timestamp: time.Now()}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, msg2, nil, nil); err != nil {
		t.Fatalf("Failed to append message 2: %v", err)
	}

	cfg := newTestCoordinatorConfig()
	cfg.Branch = GetSessionID(existingSession.Path) + "/" + res1.MessageID
	cfg.System = "System prompt"
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	coordinator := NewCoordinator(cfg, notifier)

	// Test branching from first message
	result, err := coordinator.PrepareSession(ctx, "Alternative message 2", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify branched to sibling
	if result.Session.Path == existingSession.Path {
		t.Error("Expected different session path after branch")
	}

	// Verify sibling path format
	expectedPrefix := existingSession.Path + "/" + res1.MessageID + ".s."
	if !strings.HasPrefix(result.Session.Path, expectedPrefix) {
		t.Errorf("Expected sibling path to start with %s, got %s", expectedPrefix, result.Session.Path)
	}

	// Verify only first message was copied
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// Debug: log the messages
	for i, msg := range messages {
		t.Logf("Message %d: Role=%s, Content=%q", i, msg.Role, msg.Content)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message in branch (new alternative message), got %d", len(messages))
	}
	if messages[0].Content != "Alternative message 2" {
		t.Errorf("Expected message content='Alternative message 2', got %q", messages[0].Content)
	}
}

// TestCoordinatorPrepareSessionPendingTools tests executing pending tools on resume
func TestCoordinatorPrepareSessionPendingTools(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	// Create an existing session
	existingSession, err := CreateSession("System prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message with pending tool calls
	assistantMsg := Message{
		Role:      core.RoleAssistant,
		Content:   "I'll help you with that",
		Timestamp: time.Now(),
	}
	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{
				ID:   "call_123",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["echo","hello"]}`),
			},
		},
	}
	if _, err := AppendMessageWithToolInteraction(ctx, existingSession, assistantMsg, toolInteraction.Calls, toolInteraction.Results); err != nil {
		t.Fatalf("Failed to append message with tools: %v", err)
	}

	// Create a config that enables tools
	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(existingSession.Path)
	cfg.System = "System prompt"
	cfg.IsToolEnabledFlag = true
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	coordinator := NewCoordinator(cfg, notifier)

	// Test resuming with pending tools
	result, err := coordinator.PrepareSession(ctx, "", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}
	if !result.ExecutedPending {
		t.Error("Expected ExecutedPending=true when pending tools found")
	}

	// Note: Actual tool execution would require more setup with the executor
	// This test verifies that pending tools are detected and the flag is set
}

// TestCoordinatorPrepareSessionRegeneration tests regeneration (no user message saved)
func TestCoordinatorPrepareSessionRegeneration(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	// Create an existing session
	existingSession, err := CreateSession("System prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message
	userMsg := Message{
		Role:      core.RoleUser,
		Content:   "First message",
		Timestamp: time.Now(),
	}
	if _, err := AppendMessageWithToolInteraction(context.Background(), existingSession, userMsg, nil, nil); err != nil {
		t.Fatalf("Failed to append message: %v", err)
	}

	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(existingSession.Path)
	cfg.System = "System prompt"
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	coordinator := NewCoordinator(cfg, notifier)

	// Test regeneration (isRegeneration=true)
	result, err := coordinator.PrepareSession(ctx, "This should not be saved", true, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify no new message was saved
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages (system + first user, regeneration shouldn't save new input), got %d", len(messages))
	}
}

// TestCoordinatorPrepareSessionEmptyInput tests handling empty input
func TestCoordinatorPrepareSessionEmptyInput(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	cfg := newTestCoordinatorConfig()
	cfg.System = "System prompt"
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	coordinator := NewCoordinator(cfg, notifier)

	// Test with empty input
	result, err := coordinator.PrepareSession(ctx, "", false, approver)
	if err != nil {
		t.Fatalf("PrepareSession failed: %v", err)
	}

	// Verify result
	if result.Session == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify no message was saved
	messages, err := GetLineage(result.Session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 1 {
		t.Errorf("Expected 1 message (system prompt only) for empty input, got %d", len(messages))
	}
	if len(messages) > 0 && messages[0].Role != core.RoleSystem {
		t.Errorf("Expected system message, got %v", messages[0].Role)
	}
}
