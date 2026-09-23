package session

import (
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/core"
	"os"
	"strings"
	"testing"
	"time"
)

// intrude appends a question and an answer through a session value that pins
// no head, the way a second lmc run resuming the same session would.
func intrude(t *testing.T, ctx context.Context, sessionPath string) {
	t.Helper()
	other, err := LoadSession(sessionPath)
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	appendPlanMessage(t, ctx, other, core.RoleUser, "intruder question")
	appendPlanMessage(t, ctx, other, core.RoleAssistant, "intruder answer")
}

func lineageTexts(t *testing.T, ctx context.Context, sessionPath string) []string {
	t.Helper()
	messages, err := BuildMessagesWithToolInteractions(ctx, sessionPath)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions(%s) error = %v", sessionPath, err)
	}
	var texts []string
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if text, ok := block.(core.TextBlock); ok {
				texts = append(texts, msg.Role+":"+text.Text)
			}
		}
	}
	return texts
}

func containsText(texts []string, want string) bool {
	for _, text := range texts {
		if text == want {
			return true
		}
	}
	return false
}

func resumePlanConfig(sess *Session) core.RequestOptions {
	cfg := newTestCoordinatorConfig()
	cfg.Resume = GetSessionID(sess.Path)
	return cfg
}

// A plan prepared before another writer appended lands in a fork through the
// head it was prepared against, and the original keeps the other messages.
func TestPinnedPlanCommitForksWhenAnotherWriterAppended(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "custom system")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first answer")

	plan, err := PrepareRequest(ctx, resumePlanConfig(sess), core.NewTestNotifier(), core.TestToolUI{}, "next question", false, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	intrude(t, ctx, sess.Path)

	committed, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if GetRootSession(committed.Path) == GetRootSession(sess.Path) {
		t.Fatalf("committed to %s, want a fork of %s", committed.Path, sess.Path)
	}
	if len(committed.ConflictForks) != 1 || committed.ConflictForks[0].From != GetSessionID(sess.Path) {
		t.Fatalf("ConflictForks = %+v, want one fork from %s", committed.ConflictForks, GetSessionID(sess.Path))
	}

	forked := lineageTexts(t, ctx, committed.Path)
	want := []string{"system:custom system", "user:first question", "assistant:first answer", "user:next question"}
	if strings.Join(forked, "|") != strings.Join(want, "|") {
		t.Fatalf("fork lineage = %q, want %q", forked, want)
	}
	original := lineageTexts(t, ctx, sess.Path)
	if !containsText(original, "user:intruder question") || containsText(original, "user:next question") {
		t.Fatalf("original lineage = %q, want the intruder's messages and not this turn's", original)
	}
}

// A write later in the turn, the answer save, checks the head too.
func TestPinnedAnswerSaveForksAfterAForeignAppend(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first answer")

	plan, err := PrepareRequest(ctx, resumePlanConfig(sess), core.NewTestNotifier(), core.TestToolUI{}, "next question", false, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	committed, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if len(committed.ConflictForks) != 0 {
		t.Fatalf("ConflictForks = %+v before any other writer", committed.ConflictForks)
	}
	intrude(t, ctx, committed.Path)

	store := NewStore(committed, core.NewTestLogger(false))
	if _, _, err := store.SaveAssistantResponse(ctx, core.Response{Text: "next answer"}, "test-model"); err != nil {
		t.Fatalf("SaveAssistantResponse() error = %v", err)
	}
	if len(committed.ConflictForks) != 1 {
		t.Fatalf("ConflictForks = %+v, want the answer save to fork", committed.ConflictForks)
	}
	forked := lineageTexts(t, ctx, committed.Path)
	want := []string{"user:first question", "assistant:first answer", "user:next question", "assistant:next answer"}
	if strings.Join(forked, "|") != strings.Join(want, "|") {
		t.Fatalf("fork lineage = %q, want %q", forked, want)
	}
}

// A follow-up request built during a tool round leaves out what another
// writer appended past the head.
func TestCachedBuilderExcludesMessagesPastThePinnedHead(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
	sess, err := OpenSession(GetSessionID(sess.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "our answer")

	builder, err := CreateCachedMessageBuilderForSession(ctx, sess)
	if err != nil {
		t.Fatalf("CreateCachedMessageBuilderForSession() error = %v", err)
	}
	intrude(t, ctx, sess.Path)

	messages, err := builder(sess.Path)
	if err != nil {
		t.Fatalf("builder() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("built %d messages, want the 2 through the pinned head", len(messages))
	}
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if text, ok := block.(core.TextBlock); ok && strings.HasPrefix(text.Text, "intruder") {
				t.Fatalf("built messages include %q from past the head", text.Text)
			}
		}
	}
}

// When the expected head is a tool results message, which has the user role,
// and the intervening message has it too, a sibling would splice back to the
// assistant message and drop the results. The fork keeps them.
func TestConflictForkKeepsAToolResultsHead(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "run it")
	call := echoCall("call_1", core.NewInvocationID(), "hi")
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant, Content: "running", Timestamp: time.Now()}, []core.ToolCall{call}, nil); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	sess, err := OpenSession(GetSessionID(sess.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: "call_1", Output: "hi\n"}}, ""); err != nil {
		t.Fatalf("SaveToolResults() error = %v", err)
	}

	other, err := LoadSession(sess.Path)
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	appendPlanMessage(t, ctx, other, core.RoleUser, "intruder question")

	if _, err := SaveAssistantResponse(ctx, sess, core.Response{Text: "done"}, "test-model"); err != nil {
		t.Fatalf("SaveAssistantResponse() error = %v", err)
	}
	if len(sess.ConflictForks) != 1 {
		t.Fatalf("ConflictForks = %+v, want one", sess.ConflictForks)
	}
	messages, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	var sawUse, sawResult bool
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			switch value := block.(type) {
			case core.ToolUseBlock:
				sawUse = sawUse || value.ID == "call_1"
			case core.ToolResultBlock:
				sawResult = sawResult || value.ToolUseID == "call_1"
			}
		}
	}
	if !sawUse || !sawResult {
		t.Fatalf("fork kept tool use = %v and tool result = %v, want both", sawUse, sawResult)
	}
}

func TestConflictForkPreservesTheSystemPrompt(t *testing.T) {
	for _, system := range []string{"custom system", ""} {
		t.Run("system="+system, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			sess := createPlanSession(t, system)
			appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
			sess, err := OpenSession(GetSessionID(sess.Path))
			if err != nil {
				t.Fatalf("OpenSession() error = %v", err)
			}
			intrude(t, ctx, sess.Path)
			if _, err := SaveAssistantResponse(ctx, sess, core.Response{Text: "answer"}, "test-model"); err != nil {
				t.Fatalf("SaveAssistantResponse() error = %v", err)
			}
			if len(sess.ConflictForks) != 1 {
				t.Fatalf("ConflictForks = %+v, want one", sess.ConflictForks)
			}
			got, err := GetSystemMessage(sess.Path)
			if err != nil {
				t.Fatalf("GetSystemMessage() error = %v", err)
			}
			switch {
			case system == "" && got != nil:
				t.Fatalf("fork system message = %q, want none", *got)
			case system != "" && (got == nil || *got != system):
				t.Fatalf("fork system message = %v, want %q", got, system)
			}
		})
	}
}

func TestForkThroughHeadFailsOnAnUnreadableSidecar(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "run it")
	callID := appendPlanAssistantToolCall(t, ctx, sess, "call_1", "universal_command")
	if err := os.WriteFile(buildMessageFilePaths(sess.Path, callID).ToolsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt tool interaction: %v", err)
	}
	before := sessionDirNames(t)

	head, ok, err := LastMessageRefWithManager(DefaultManager(), sess.Path)
	if err != nil || !ok {
		t.Fatalf("LastMessageRefWithManager() = %v, %v", ok, err)
	}
	if _, err := ForkThroughHead(ctx, DefaultManager(), sess.Path, head); err == nil {
		t.Fatal("ForkThroughHead() error = nil, want the unreadable tool interaction reported")
	}
	if after := sessionDirNames(t); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("sessions after the failed fork = %v, want %v with no partial session left", after, before)
	}
}

// sessionDirNames lists the session directories in the sessions root. Lock
// files sit beside them and dot directories hold the journal; neither is a
// session.
func sessionDirNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(GetSessionsDir())
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestConflictForkKeepsInvocationIdentity(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "run it")
	invocation := core.NewInvocationID()
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant, Content: "running", Timestamp: time.Now()}, []core.ToolCall{echoCall("call_1", invocation, "hi")}, nil); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	sess, err := OpenSession(GetSessionID(sess.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	intrude(t, ctx, sess.Path)

	head := *sess.Head
	fork, err := ForkThroughHead(ctx, DefaultManager(), sess.Path, head)
	if err != nil {
		t.Fatalf("ForkThroughHead() error = %v", err)
	}
	pending, err := CheckForPendingToolCalls(ctx, fork.Path)
	if err != nil {
		t.Fatalf("CheckForPendingToolCalls() error = %v", err)
	}
	if len(pending) != 1 || pending[0].InvocationID != invocation {
		t.Fatalf("fork pending calls = %+v, want the call with invocation %s", pending, invocation)
	}
}

func TestPinnedWritesFromOneWriterAdvanceTheHead(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	sess, err := OpenSession(GetSessionID(sess.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	for i, role := range []core.Role{core.RoleUser, core.RoleAssistant, core.RoleUser} {
		result, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: role, Content: string(role), Timestamp: time.Now()}, nil, nil)
		if err != nil {
			t.Fatalf("append %d error = %v", i, err)
		}
		if sess.Head == nil || sess.Head.ID != result.MessageID || sess.Head.Path != result.Path {
			t.Fatalf("head after append %d = %+v, want %+v", i, sess.Head, result)
		}
	}
	if len(sess.ConflictForks) != 0 {
		t.Fatalf("ConflictForks = %+v, want none for a single writer", sess.ConflictForks)
	}
}

func TestHeadMovedErrorIsDistinct(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	head, _, err := LastMessageRefWithManager(DefaultManager(), sess.Path)
	if err != nil {
		t.Fatalf("LastMessageRefWithManager() error = %v", err)
	}
	intrude(t, ctx, sess.Path)

	staged, err := stageMessageFiles(sess.Path, Message{Role: core.RoleAssistant, Content: "late", Timestamp: time.Now()}, nil)
	if err != nil {
		t.Fatalf("stageMessageFiles() error = %v", err)
	}
	defer staged.Close()
	mc := &messageCommitter{sessionPath: sess.Path, expected: &head}
	_, _, _, err = mc.Commit(ctx, staged)
	var moved *HeadMovedError
	if !errors.Is(err, ErrHeadMoved) || !errors.As(err, &moved) || moved.Expected != head {
		t.Fatalf("Commit() error = %v, want a HeadMovedError expecting %+v", err, head)
	}
	if _, err := json.Marshal(moved.Error()); err != nil || !strings.Contains(moved.Error(), "changed") {
		t.Fatalf("HeadMovedError message = %q", moved.Error())
	}
}
