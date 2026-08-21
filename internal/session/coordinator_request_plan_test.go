package session

import (
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCoordinatorPrepareRequestNewSessionDefersWritesUntilCommit(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	cfg := newTestCoordinatorConfig()
	cfg.System = "plan system"

	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "hello", false, nil, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got, want := typedPlanTexts(plan.Messages), []string{"system:plan system", "user:hello"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan messages = %#v, want %#v", got, want)
	}
	assertSessionsDirEntryCount(t, 0)

	sess, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if sess == nil {
		t.Fatal("Commit() returned nil session")
	}
	messages, err := GetLineage(sess.Path)
	if err != nil {
		t.Fatalf("GetLineage() error = %v", err)
	}
	if got, want := storedPlanTexts(messages), []string{"system:plan system", "user:hello"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stored messages = %#v, want %#v", got, want)
	}
}

func TestLoadSessionWithRetryPreservesNotExistError(t *testing.T) {
	setupCoordinatorTestEnv(t)

	_, err := loadSessionWithRetry("missing-session")
	if err == nil {
		t.Fatal("loadSessionWithRetry() error = nil, want missing session error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loadSessionWithRetry() error = %v, want errors.Is(os.ErrNotExist)", err)
	}
}

func TestCoordinatorPrepareRequestResumeAppendsInputWithoutWritingUntilCommit(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "session system")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "hello")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "hi")

	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(sess.Path)
	cfg.System = "session system"

	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "preview question", false, nil, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got, want := typedPlanTexts(plan.Messages), []string{
		"system:session system",
		"user:hello",
		"assistant:hi",
		"user:preview question",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan messages = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(sess.Path, "0003.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PrepareRequest() wrote resumed user message before Commit, stat err = %v", err)
	}

	committed, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if committed.Path != sess.Path {
		t.Fatalf("committed path = %s, want original path %s", committed.Path, sess.Path)
	}
	if _, err := os.Stat(filepath.Join(sess.Path, "0003.json")); err != nil {
		t.Fatalf("Commit() did not save resumed user message: %v", err)
	}
}

func TestCoordinatorPrepareRequestPreviewPendingToolsUsesPlaceholderWithoutWriting(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "session system")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "run a tool")
	appendPlanAssistantToolCall(t, ctx, sess, "call-1", "universal_command")

	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(sess.Path)
	cfg.System = "session system"
	cfg.ToolEnabled = true

	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "", false, nil, PendingToolPreview)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if !plan.HasPendingTools {
		t.Fatal("HasPendingTools = false, want true")
	}
	if _, err := os.Stat(filepath.Join(sess.Path, "0003.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PrepareRequest() wrote preview tool result before Commit, stat err = %v", err)
	}

	if len(plan.Messages) == 0 {
		t.Fatal("plan messages are empty")
	}
	toolResultMsg := plan.Messages[len(plan.Messages)-1]
	if toolResultMsg.Role != string(core.RoleUser) {
		t.Fatalf("last message role = %q, want user", toolResultMsg.Role)
	}
	if len(toolResultMsg.Blocks) != 1 {
		t.Fatalf("last message blocks = %d, want 1", len(toolResultMsg.Blocks))
	}
	result, ok := toolResultMsg.Blocks[0].(core.ToolResultBlock)
	if !ok {
		t.Fatalf("last block = %T, want ToolResultBlock", toolResultMsg.Blocks[0])
	}
	if result.ToolUseID != "call-1" || result.Name != "universal_command" {
		t.Fatalf("tool result metadata = %#v", result)
	}
	if !strings.Contains(result.Content, `[print-curl placeholder] Tool "universal_command" (call call-1) was not executed.`) {
		t.Fatalf("tool result content = %q, want placeholder", result.Content)
	}
}

func TestAppendPendingToolPreviewResultsMatchesCommittedMessage(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "session system")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "run a tool")
	appendPlanAssistantToolCall(t, ctx, sess, "call-1", "universal_command")

	pending := pendingToolResolution{
		HasPending:     true,
		PreviewCalls:   []core.ToolCall{{ID: "call-1", Name: "universal_command"}},
		PreviewResults: []core.ToolResult{{ID: "call-1", Output: "partial output", Truncated: true}},
		AdditionalText: "Note: Output for tool 'universal_command' was truncated to 1MB\n",
	}

	staged := appendPendingToolPreviewResults(nil, pending)
	if len(staged) != 1 {
		t.Fatalf("staged message count = %d, want 1", len(staged))
	}
	note, ok := staged[0].Blocks[len(staged[0].Blocks)-1].(core.TextBlock)
	if !ok || note.Text != pending.AdditionalText {
		t.Fatalf("staged trailing block = %#v, want truncation note %q",
			staged[0].Blocks[len(staged[0].Blocks)-1], pending.AdditionalText)
	}

	if err := commitPendingToolResults(ctx, sess, pending, core.NewTestLogger(false)); err != nil {
		t.Fatalf("commitPendingToolResults() error = %v", err)
	}
	rebuilt, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	if got, want := rebuilt[len(rebuilt)-1], staged[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("committed tool-results message = %#v, want staged preview %#v", got, want)
	}
}

func TestCoordinatorPrepareRequestBranchAssistantDefersSiblingUntilCommit(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "system prompt")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first user")
	assistantID := appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first assistant")

	cfg := newTestCoordinatorConfig()
	cfg.Branch = GetSessionID(sess.Path) + "/" + assistantID
	cfg.System = "system prompt"

	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "", true, nil, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got, want := typedPlanTexts(plan.Messages), []string{"system:system prompt", "user:first user"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan messages = %#v, want %#v", got, want)
	}
	if hasSiblingForMessage(t, sess.Path, assistantID) {
		t.Fatalf("PrepareRequest() created sibling before Commit")
	}

	committed, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if committed.Path == sess.Path {
		t.Fatalf("Commit() did not create branch")
	}
	if !hasSiblingForMessage(t, sess.Path, assistantID) {
		t.Fatalf("Commit() did not create sibling for assistant message")
	}
}

func TestCoordinatorPrepareRequestBranchUserUsesPreviousAssistant(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "system prompt")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first user")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first assistant")
	userID := appendPlanMessage(t, ctx, sess, core.RoleUser, "second user")

	cfg := newTestCoordinatorConfig()
	cfg.Branch = GetSessionID(sess.Path) + "/" + userID
	cfg.System = "system prompt"

	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "alternate second user", false, nil, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got, want := typedPlanTexts(plan.Messages), []string{
		"system:system prompt",
		"user:first user",
		"assistant:first assistant",
		"user:alternate second user",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan messages = %#v, want %#v", got, want)
	}
}

func TestCoordinatorPrepareRequestNestedBranchBubblesToOriginalAnchor(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "system prompt")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first user")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first assistant")
	secondUserID := appendPlanMessage(t, ctx, sess, core.RoleUser, "second user")

	siblingPath, err := CreateSibling(ctx, sess.Path, secondUserID)
	if err != nil {
		t.Fatalf("CreateSibling() error = %v", err)
	}
	sibling, err := LoadSession(siblingPath)
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	appendPlanMessage(t, ctx, sibling, core.RoleUser, "alternate second user")
	alternateAssistantID := appendPlanMessage(t, ctx, sibling, core.RoleAssistant, "alternate assistant")

	cfg := newTestCoordinatorConfig()
	cfg.Branch = GetSessionID(sibling.Path) + "/" + alternateAssistantID
	cfg.System = "system prompt"

	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "", true, nil, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got, want := typedPlanTexts(plan.Messages), []string{
		"system:system prompt",
		"user:first user",
		"assistant:first assistant",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan messages = %#v, want %#v", got, want)
	}
}

func TestRequestPlanCommitRejectsSecondCommit(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)

	cfg := newTestCoordinatorConfig()
	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "hello", false, nil, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if _, err := plan.Commit(ctx); err != nil {
		t.Fatalf("first Commit() error = %v", err)
	}
	if _, err := plan.Commit(ctx); err == nil {
		t.Fatalf("second Commit() succeeded, want error")
	}
}

func createPlanSession(t *testing.T, system string) *Session {
	t.Helper()
	sess, err := CreateSession(system, core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return sess
}

func appendPlanMessage(t *testing.T, ctx context.Context, sess *Session, role core.Role, content string) string {
	t.Helper()
	result, err := AppendMessageWithToolInteraction(ctx, sess, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}, nil, nil)
	if err != nil {
		t.Fatalf("AppendMessageWithToolInteraction(%s, %q) error = %v", role, content, err)
	}
	return result.MessageID
}

func appendPlanAssistantToolCall(t *testing.T, ctx context.Context, sess *Session, id, name string) string {
	t.Helper()
	result, err := AppendMessageWithToolInteraction(ctx, sess, Message{
		Role:      core.RoleAssistant,
		Content:   "I will run a tool",
		Timestamp: time.Now(),
		Model:     "test-model",
	}, []core.ToolCall{{
		ID:   id,
		Name: name,
		Args: json.RawMessage(`{"command":["echo","hi"]}`),
	}}, nil)
	if err != nil {
		t.Fatalf("AppendMessageWithToolInteraction(tool call) error = %v", err)
	}
	return result.MessageID
}

func typedPlanTexts(messages []core.TypedMessage) []string {
	var out []string
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if text, ok := block.(core.TextBlock); ok {
				out = append(out, msg.Role+":"+text.Text)
			}
		}
	}
	return out
}

func storedPlanTexts(messages []Message) []string {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		out = append(out, string(msg.Role)+":"+msg.Content)
	}
	return out
}

func assertSessionsDirEntryCount(t *testing.T, want int) {
	t.Helper()
	entries, err := os.ReadDir(GetSessionsDir())
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", GetSessionsDir(), err)
	}
	if len(entries) != want {
		t.Fatalf("session dir entry count = %d, want %d", len(entries), want)
	}
}

func hasSiblingForMessage(t *testing.T, sessionPath, messageID string) bool {
	t.Helper()
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", sessionPath, err)
	}
	prefix := messageID + ".s."
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			return true
		}
	}
	return false
}
