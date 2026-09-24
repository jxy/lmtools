package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCoordinatorPrepareRequestNewSession tests creating a new session
func TestCoordinatorPrepareRequestNewSession(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	cfg := newTestCoordinatorConfig()
	cfg.System = "You are a helpful assistant"
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	// Test creating new session
	sess, executedPending, err := prepareSessionForTest(ctx, cfg, notifier, "Hello, world!", false, approver)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	// Verify result
	if sess == nil {
		t.Fatal("Expected non-nil session")
	}
	if executedPending {
		t.Error("Expected ExecutedPending=false for new session")
	}

	// Verify session was created with system prompt
	// Verify system prompt was set correctly by checking the first message
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}
	if len(messages) > 0 && messages[0].Role == core.RoleSystem {
		if messages[0].Content != cfg.System {
			t.Errorf("Expected system=%q, got %q", cfg.System, messages[0].Content)
		}
	}

	// Verify user message was saved
	messages, err = GetLineage(sess.Path)
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

// TestCoordinatorPrepareRequestResume tests resuming an existing session
func TestCoordinatorPrepareRequestResume(t *testing.T) {
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

	// Test resuming session
	sess, executedPending, err := prepareSessionForTest(ctx, cfg, notifier, "Second message", false, approver)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	// Verify result
	if sess == nil {
		t.Fatal("Expected non-nil session")
	}
	if executedPending {
		t.Error("Expected ExecutedPending=false (no pending tools)")
	}

	// Verify same session path (no fork)
	if sess.Path != existingSession.Path {
		t.Errorf("Expected same session path, got %s", sess.Path)
	}

	// Verify messages
	messages, err := GetLineage(sess.Path)
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

// TestCoordinatorPrepareRequestForkOnSystemChange tests forking when system prompt changes
func TestCoordinatorPrepareRequestForkOnSystemChange(t *testing.T) {
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

	// Test resuming with different system prompt
	sess, _, err := prepareSessionForTest(ctx, cfg, notifier, "Second message", false, approver)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	// Verify result
	if sess == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify forked to new session
	if sess.Path == existingSession.Path {
		t.Error("Expected different session path after fork")
	}

	// Verify fork notification
	foundForkNotification := infoMessagesContain(notifier, "Forked session due to system prompt change")
	if !foundForkNotification {
		t.Error("Expected fork notification in notifier messages")
	}

	// Verify new session has new system prompt
	// Verify system prompt was set correctly by checking the first message
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}
	if len(messages) > 0 && messages[0].Role == core.RoleSystem {
		if messages[0].Content != cfg.System {
			t.Errorf("Expected system=%q, got %q", cfg.System, messages[0].Content)
		}
	}

	// Verify messages were copied
	messages, err = GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages in forked session (new system + copied user + new user), got %d", len(messages))
	}
}

// TestCoordinatorPrepareRequestBranch tests explicit branching
func TestCoordinatorPrepareRequestBranch(t *testing.T) {
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

	// Test branching from first message
	sess, _, err := prepareSessionForTest(ctx, cfg, notifier, "Alternative message 2", false, approver)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	// Verify result
	if sess == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify branched to sibling
	if sess.Path == existingSession.Path {
		t.Error("Expected different session path after branch")
	}

	// Verify sibling path format
	expectedPrefix := existingSession.Path + "/" + res1.MessageID + ".s."
	if !strings.HasPrefix(sess.Path, expectedPrefix) {
		t.Errorf("Expected sibling path to start with %s, got %s", expectedPrefix, sess.Path)
	}

	// The branch replaces the first user message and keeps the system prompt
	// its root begins with.
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// Debug: log the messages
	for i, msg := range messages {
		t.Logf("Message %d: Role=%s, Content=%q", i, msg.Role, msg.Content)
	}

	if len(messages) != 2 || messages[0].Role != core.RoleSystem || messages[0].Content != "System prompt" {
		t.Fatalf("branch lineage has %d messages, want the root's system prompt and then the alternative message", len(messages))
	}
	if messages[1].Content != "Alternative message 2" {
		t.Errorf("Expected message content='Alternative message 2', got %q", messages[1].Content)
	}
}

// TestCoordinatorPrepareRequestPendingTools checks that preparing a request
// never runs a tool. Pending calls at the head are refused until
// ResolvePendingToolCalls has resolved them, however often preparation is
// tried, and the plan prepared afterwards carries their results.
func TestCoordinatorPrepareRequestPendingTools(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	existingSession, err := CreateSession("System prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "ran")
	call := core.ToolCall{
		ID:           "call_123",
		Name:         "universal_command",
		Args:         json.RawMessage(fmt.Sprintf(`{"command":["touch",%q]}`, marker)),
		InvocationID: core.NewInvocationID(),
	}
	claim, err := NewToolJournal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	claim.Release()
	assistantMsg := Message{Role: core.RoleAssistant, Content: "I'll help you with that", Timestamp: time.Now()}
	if _, err := AppendMessageWithToolInteraction(ctx, existingSession, assistantMsg, []core.ToolCall{call}, nil); err != nil {
		t.Fatalf("Failed to append message with tools: %v", err)
	}

	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(existingSession.Path)
	cfg.System = "System prompt"
	cfg.ToolEnabled = true
	notifier := core.NewTestNotifier()

	for attempt := 0; attempt < 2; attempt++ {
		_, err := PrepareRequest(ctx, cfg, notifier, core.TestToolUI{}, "", false, PendingToolExecute)
		if err == nil || !strings.Contains(err.Error(), "must be resolved") {
			t.Fatalf("PrepareRequest() attempt %d error = %v, want the unresolved pending call refused", attempt+1, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("preparing a request ran the pending command, marker stat err = %v", err)
	}

	sess, err := OpenSession(ctx, cfg.Resume)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	resolution, err := ResolvePendingToolCalls(ctx, sess, cfg, core.NewTestLogger(false), notifier, core.TestToolUI{}, core.NewTestApprover(true))
	if err != nil || !resolution.Committed {
		t.Fatalf("ResolvePendingToolCalls() = %+v, %v; want the call run and its result committed", resolution, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("resolution did not run the pending command: %v", err)
	}

	plan, err := PrepareRequestAt(ctx, cfg, notifier, core.TestToolUI{}, sess, "", false, PendingToolExecute)
	if err != nil {
		t.Fatalf("PrepareRequestAt() error = %v", err)
	}
	last := plan.Messages[len(plan.Messages)-1]
	if _, ok := last.Blocks[0].(core.ToolResultBlock); last.Role != string(core.RoleUser) || !ok {
		t.Fatalf("last planned message = %#v, want the committed tool results", last)
	}
}

// TestCoordinatorPrepareRequestRegeneration tests regeneration (no user message saved)
func TestCoordinatorPrepareRequestRegeneration(t *testing.T) {
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

	// Test regeneration (isRegeneration=true)
	sess, _, err := prepareSessionForTest(ctx, cfg, notifier, "This should not be saved", true, approver)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	// Verify result
	if sess == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify no new message was saved
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages (system + first user, regeneration shouldn't save new input), got %d", len(messages))
	}
}

// TestCoordinatorPrepareRequestEmptyInput tests handling empty input
func TestCoordinatorPrepareRequestEmptyInput(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	cfg := newTestCoordinatorConfig()
	cfg.System = "System prompt"
	notifier := core.NewTestNotifier()
	approver := core.NewTestApprover(true)

	// Test with empty input
	sess, _, err := prepareSessionForTest(ctx, cfg, notifier, "", false, approver)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	// Verify result
	if sess == nil {
		t.Fatal("Expected non-nil session")
	}

	// Verify no message was saved
	messages, err := GetLineage(sess.Path)
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
