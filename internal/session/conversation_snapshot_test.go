package session

import (
	"context"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConversationSnapshotRefreshIncludesCommitAfterLineageScan(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	first := appendPlanMessage(t, ctx, sess, core.RoleUser, "first")
	path := manager.ResolveSessionPath(sess.Path)
	scan, err := scanLineage(manager, path)
	if err != nil {
		t.Fatalf("scanLineage() error = %v", err)
	}

	// Simulate another process committing after rebuild's lineage scan but
	// before the snapshot installs it.
	second := appendPlanMessage(t, ctx, sess, core.RoleUser, "concurrent")
	snapshot := &conversationSnapshot{manager: manager, sessionPath: path, refs: scan.refs, activeIDs: scan.activeIDs}
	if got, want := snapshot.activeIDs, []string{first}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs recorded by the lineage scan = %v, want %v", got, want)
	}

	messages, err := snapshot.buildTypedMessages(ctx, path)
	if err != nil {
		t.Fatalf("buildTypedMessages() error = %v", err)
	}
	if got, want := lineageRefIDs(snapshot.refs), []string{first, second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ref IDs after refresh = %v, want %v", got, want)
	}
	if len(messages) != 2 {
		t.Fatalf("message count after refresh = %d, want 2", len(messages))
	}

	if _, err := snapshot.buildTypedMessages(ctx, path); err != nil {
		t.Fatalf("repeated buildTypedMessages() error = %v", err)
	}
	if got, want := lineageRefIDs(snapshot.refs), []string{first, second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ref IDs after repeated refresh = %v, want %v", got, want)
	}
}

func TestConversationSnapshotRefreshRetryDoesNotDuplicatePartialSuffix(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	base := appendPlanMessage(t, ctx, sess, core.RoleUser, "base")
	snapshot, err := newConversationSnapshotWithManager(manager, sess.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}

	first := appendPlanMessage(t, ctx, sess, core.RoleUser, "first append")
	second := appendPlanMessage(t, ctx, sess, core.RoleUser, "second append")
	metadataPath := buildMessageFilePaths(sess.Path, second).JSONPath
	originalMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read second message metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt second message metadata: %v", err)
	}

	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err == nil {
		t.Fatal("buildTypedMessages() error = nil, want appended-message read failure")
	} else if !strings.Contains(err.Error(), "read appended message "+second) {
		t.Fatalf("buildTypedMessages() error = %v, want second appended message", err)
	}
	if got, want := lineageRefIDs(snapshot.refs), []string{base}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ref IDs after failed refresh = %v, want unchanged %v", got, want)
	}
	if got, want := snapshot.activeIDs, []string{base}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active IDs after failed refresh = %v, want unchanged %v", got, want)
	}

	if err := os.WriteFile(metadataPath, originalMetadata, 0o600); err != nil {
		t.Fatalf("restore second message metadata: %v", err)
	}
	messages, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("buildTypedMessages() after repair error = %v", err)
	}
	wantIDs := []string{base, first, second}
	if got := lineageRefIDs(snapshot.refs); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("ref IDs after retry = %v, want %v", got, wantIDs)
	}
	if len(messages) != len(wantIDs) {
		t.Fatalf("message count after retry = %d, want %d", len(messages), len(wantIDs))
	}

	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
		t.Fatalf("repeated buildTypedMessages() error = %v", err)
	}
	if got := lineageRefIDs(snapshot.refs); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("ref IDs after repeated refresh = %v, want %v", got, wantIDs)
	}
}

// TestConversationSnapshotRebuildKeepsPathAfterFailure pins that a rebuild that
// cannot complete leaves the snapshot on the session it was already serving. An
// unreadable message no longer qualifies — loadMessagesInDir skips it — so the
// failure used here is a session directory that cannot be listed at all.
func TestConversationSnapshotRebuildKeepsPathAfterFailure(t *testing.T) {
	manager, sessionsDir := NewTestManager(t)
	ctx := context.Background()
	initial, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("create initial session: %v", err)
	}
	appendPlanMessage(t, ctx, initial, core.RoleUser, "initial")
	snapshot, err := newConversationSnapshotWithManager(manager, initial.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}

	missing := filepath.Join(sessionsDir, "beef")
	if _, err := snapshot.buildTypedMessages(ctx, missing); err == nil {
		t.Fatal("buildTypedMessages() error = nil, want rebuild failure")
	} else if !strings.Contains(err.Error(), "load messages in "+missing) {
		t.Fatalf("buildTypedMessages() error = %v, want directory read failure", err)
	}
	if snapshot.sessionPath != manager.ResolveSessionPath(initial.Path) {
		t.Fatalf("session path after failed rebuild = %q, want %q", snapshot.sessionPath, initial.Path)
	}
	if got, want := len(snapshot.refs), 1; got != want {
		t.Fatalf("ref count after failed rebuild = %d, want %d", got, want)
	}
}

// corruptCommittedBlocks makes a committed .blocks.json undecodable. A build
// that still succeeds afterwards provably did not read it again.
func corruptCommittedBlocks(t *testing.T, sessionPath, msgID string) {
	t.Helper()
	path := buildMessageFilePaths(sessionPath, msgID).BlocksPath
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat blocks for %s: %v", msgID, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt blocks for %s: %v", msgID, err)
	}
}

// TestConversationSnapshotReusesBuiltMessages pins the memoization itself. The
// tool loop calls this once per round, and re-decoding the whole transcript
// every time is quadratic in the number of rounds.
func TestConversationSnapshotReusesBuiltMessages(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	first := appendPlanMessage(t, ctx, sess, core.RoleUser, "first")
	second := appendPlanMessage(t, ctx, sess, core.RoleUser, "second")

	snapshot, err := newConversationSnapshotWithManager(manager, sess.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}
	built, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("first buildTypedMessages() error = %v", err)
	}

	corruptCommittedBlocks(t, sess.Path, first)
	corruptCommittedBlocks(t, sess.Path, second)

	reused, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("second buildTypedMessages() error = %v, want the already-built messages", err)
	}
	if !reflect.DeepEqual(reused, built) {
		t.Fatalf("second build = %#v, want %#v", reused, built)
	}

	// The returned slice must not alias the memo: appending to it here must not
	// disturb what the next round reads.
	if extended := append(reused, core.TypedMessage{Role: string(core.RoleUser)}); len(extended) != len(built)+1 {
		t.Fatalf("appending to the returned slice produced %d messages, want %d", len(extended), len(built)+1)
	}
	again, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("third buildTypedMessages() error = %v", err)
	}
	if !reflect.DeepEqual(again, built) {
		t.Fatalf("third build = %#v, want %#v", again, built)
	}
}

// TestConversationSnapshotBuildsAppendedMessagesAfterReuse pins that reuse does
// not stop at the messages already built: the round just committed has to show
// up, with the content it was actually committed with.
func TestConversationSnapshotBuildsAppendedMessagesAfterReuse(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	appendPlanMessage(t, ctx, sess, core.RoleUser, "run the tool")

	snapshot, err := newConversationSnapshotWithManager(manager, sess.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}
	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
		t.Fatalf("first buildTypedMessages() error = %v", err)
	}

	call := core.ToolCall{ID: "call-1", Name: "universal_command", Args: []byte(`{"command":["echo","hi"]}`)}
	if _, err := SaveAssistantResponseWithTools(ctx, sess, "on it", []core.ToolCall{call}, "test-model"); err != nil {
		t.Fatalf("save assistant round: %v", err)
	}
	// Build with the assistant turn committed but its results not yet, so the
	// two land in different increments and the tool name has to survive between
	// them.
	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
		t.Fatalf("buildTypedMessages() between rounds error = %v", err)
	}
	if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: call.ID, Output: "hi"}}, ""); err != nil {
		t.Fatalf("save tool results: %v", err)
	}

	messages, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("buildTypedMessages() after append error = %v", err)
	}
	fresh, err := BuildMessagesWithToolInteractionsWithManager(ctx, manager, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractionsWithManager() error = %v", err)
	}
	if !reflect.DeepEqual(messages, fresh) {
		t.Fatalf("build after append = %#v, want an uncached build %#v", messages, fresh)
	}

	// The tool name is carried by the running index the memo shares, so a result
	// committed anonymously still resolves against the assistant turn built in an
	// earlier round.
	result, ok := messages[2].Blocks[0].(core.ToolResultBlock)
	if !ok || result.ToolUseID != call.ID || result.Name != call.Name {
		t.Fatalf("tool result block = %#v, want %s named %s", messages[2].Blocks[0], call.ID, call.Name)
	}
}

// TestConversationSnapshotDropsBuiltMessagesForSiblingPath pins invalidation on
// a branch. The sibling lineage keeps the parent prefix but replaces the branch
// point, so a memo carried across the switch serves the wrong assistant turn.
func TestConversationSnapshotDropsBuiltMessagesForSiblingPath(t *testing.T) {
	WithTestSessionDir(t, func(_ string) {
		ctx := context.Background()
		sess, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("CreateSession() error = %v", err)
		}
		appendPlanMessage(t, ctx, sess, core.RoleUser, "choose a tool")
		rootResponse, err := SaveAssistantResponseWithTools(ctx, sess, "root choice", []core.ToolCall{{
			ID: "root-call", Name: "universal_command", Args: []byte(`{"command":["echo","root"]}`),
		}}, "test-model")
		if err != nil {
			t.Fatalf("save root assistant: %v", err)
		}

		snapshot, err := newConversationSnapshotWithManager(DefaultManager(), sess.Path)
		if err != nil {
			t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
		}
		if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
			t.Fatalf("root buildTypedMessages() error = %v", err)
		}

		siblingPath, err := CreateSibling(ctx, sess.Path, rootResponse.MessageID)
		if err != nil {
			t.Fatalf("CreateSibling() error = %v", err)
		}
		sibling := &Session{Path: siblingPath}
		if _, err := SaveAssistantResponseWithTools(ctx, sibling, "branch choice", []core.ToolCall{{
			ID: "branch-call", Name: "universal_command", Args: []byte(`{"command":["echo","branch"]}`),
		}}, "test-model"); err != nil {
			t.Fatalf("save branch assistant: %v", err)
		}

		messages, err := snapshot.buildTypedMessages(ctx, sibling.Path)
		if err != nil {
			t.Fatalf("sibling buildTypedMessages() error = %v", err)
		}
		fresh, err := BuildMessagesWithToolInteractionsWithManager(ctx, DefaultManager(), sibling.Path)
		if err != nil {
			t.Fatalf("BuildMessagesWithToolInteractionsWithManager() error = %v", err)
		}
		if !reflect.DeepEqual(messages, fresh) {
			t.Fatalf("sibling build = %#v, want an uncached build %#v", messages, fresh)
		}
		branchUse, ok := messages[1].Blocks[1].(core.ToolUseBlock)
		if !ok || branchUse.ID != "branch-call" {
			t.Fatalf("branch tool use = %#v, want branch-call rather than the root turn", messages[1].Blocks[1])
		}
	})
}

// TestConversationSnapshotDropsBuiltMessagesWhenActiveIDsDiverge pins the other
// invalidation route: the committed prefix no longer matches, so refresh has to
// rebuild, and a memo that survived it would keep serving deleted messages.
func TestConversationSnapshotDropsBuiltMessagesWhenActiveIDsDiverge(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	kept := appendPlanMessage(t, ctx, sess, core.RoleUser, "kept")
	dropped := appendPlanMessage(t, ctx, sess, core.RoleUser, "dropped")

	snapshot, err := newConversationSnapshotWithManager(manager, sess.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}
	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
		t.Fatalf("first buildTypedMessages() error = %v", err)
	}

	// Rewrite the surviving message and remove the one after it, so the active
	// listing shrinks and the retained message no longer says what it did.
	keptPaths := buildMessageFilePaths(sess.Path, kept)
	if err := os.WriteFile(keptPaths.TxtPath, []byte("rewritten"), 0o600); err != nil {
		t.Fatalf("rewrite kept content: %v", err)
	}
	if err := os.Remove(keptPaths.BlocksPath); err != nil {
		t.Fatalf("remove kept blocks: %v", err)
	}
	droppedPaths := buildMessageFilePaths(sess.Path, dropped)
	for _, path := range []string{droppedPaths.JSONPath, droppedPaths.TxtPath, droppedPaths.BlocksPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", path, err)
		}
	}

	messages, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("buildTypedMessages() after divergence error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count after divergence = %d, want 1", len(messages))
	}
	text, ok := messages[0].Blocks[0].(core.TextBlock)
	if !ok || text.Text != "rewritten" {
		t.Fatalf("rebuilt block = %#v, want the rewritten content", messages[0].Blocks[0])
	}
	if got, want := snapshot.activeIDs, []string{kept}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active IDs after divergence = %v, want %v", got, want)
	}
}

// lineageRefIDs is shared by the snapshot and message-scan tests.
func lineageRefIDs(refs []lineageMessageRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.message.ID)
	}
	return ids
}
