package session

import (
	"context"
	"lmtools/internal/core"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConversationSnapshotRefreshIncludesCommitAfterLineageScan(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	sess, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	first := appendConversationSnapshotTestMessage(t, ctx, sess, "first")
	path := manager.ResolveSessionPath(sess.Path)
	refs, err := lineageMessageRefsWithManager(manager, path)
	if err != nil {
		t.Fatalf("lineageMessageRefsWithManager() error = %v", err)
	}

	// Simulate another process committing after rebuild's lineage scan but
	// before the snapshot installs its bookkeeping.
	second := appendConversationSnapshotTestMessage(t, ctx, sess, "concurrent")
	snapshot := &conversationSnapshot{manager: manager}
	snapshot.replaceLineage(path, refs)
	if got, want := snapshot.activeMessageIDs, []string{first.MessageID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs installed from scanned lineage = %v, want %v", got, want)
	}

	messages, err := snapshot.buildTypedMessages(ctx, path)
	if err != nil {
		t.Fatalf("buildTypedMessages() error = %v", err)
	}
	if got, want := conversationSnapshotRefIDs(snapshot.refs), []string{first.MessageID, second.MessageID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ref IDs after refresh = %v, want %v", got, want)
	}
	if len(messages) != 2 {
		t.Fatalf("message count after refresh = %d, want 2", len(messages))
	}

	if _, err := snapshot.buildTypedMessages(ctx, path); err != nil {
		t.Fatalf("repeated buildTypedMessages() error = %v", err)
	}
	if got, want := conversationSnapshotRefIDs(snapshot.refs), []string{first.MessageID, second.MessageID}; !reflect.DeepEqual(got, want) {
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

	base := appendConversationSnapshotTestMessage(t, ctx, sess, "base")
	snapshot, err := newConversationSnapshotWithManager(manager, sess.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}

	first := appendConversationSnapshotTestMessage(t, ctx, sess, "first append")
	second := appendConversationSnapshotTestMessage(t, ctx, sess, "second append")
	metadataPath := buildMessageFilePaths(sess.Path, second.MessageID).JSONPath
	originalMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read second message metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt second message metadata: %v", err)
	}

	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err == nil {
		t.Fatal("buildTypedMessages() error = nil, want appended-message read failure")
	} else if !strings.Contains(err.Error(), "read appended message "+second.MessageID) {
		t.Fatalf("buildTypedMessages() error = %v, want second appended message", err)
	}
	if got, want := conversationSnapshotRefIDs(snapshot.refs), []string{base.MessageID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ref IDs after failed refresh = %v, want unchanged %v", got, want)
	}
	if got, want := snapshot.activeMessageIDs, []string{base.MessageID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active IDs after failed refresh = %v, want unchanged %v", got, want)
	}

	if err := os.WriteFile(metadataPath, originalMetadata, 0o600); err != nil {
		t.Fatalf("restore second message metadata: %v", err)
	}
	messages, err := snapshot.buildTypedMessages(ctx, sess.Path)
	if err != nil {
		t.Fatalf("buildTypedMessages() after repair error = %v", err)
	}
	wantIDs := []string{base.MessageID, first.MessageID, second.MessageID}
	if got := conversationSnapshotRefIDs(snapshot.refs); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("ref IDs after retry = %v, want %v", got, wantIDs)
	}
	if len(messages) != len(wantIDs) {
		t.Fatalf("message count after retry = %d, want %d", len(messages), len(wantIDs))
	}

	if _, err := snapshot.buildTypedMessages(ctx, sess.Path); err != nil {
		t.Fatalf("repeated buildTypedMessages() error = %v", err)
	}
	if got := conversationSnapshotRefIDs(snapshot.refs); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("ref IDs after repeated refresh = %v, want %v", got, wantIDs)
	}
}

func TestConversationSnapshotRebuildReportsCorruptMessage(t *testing.T) {
	manager, _ := NewTestManager(t)
	ctx := context.Background()
	initial, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("create initial session: %v", err)
	}
	appendConversationSnapshotTestMessage(t, ctx, initial, "initial")
	snapshot, err := newConversationSnapshotWithManager(manager, initial.Path)
	if err != nil {
		t.Fatalf("newConversationSnapshotWithManager() error = %v", err)
	}

	broken, err := manager.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("create broken session: %v", err)
	}
	brokenMessage := appendConversationSnapshotTestMessage(t, ctx, broken, "broken")
	metadataPath := buildMessageFilePaths(brokenMessage.Path, brokenMessage.MessageID).JSONPath
	if err := os.WriteFile(metadataPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt message metadata: %v", err)
	}

	if _, err := snapshot.buildTypedMessages(ctx, broken.Path); err == nil {
		t.Fatal("buildTypedMessages() error = nil, want rebuild failure")
	} else if !strings.Contains(err.Error(), "read message "+brokenMessage.MessageID) {
		t.Fatalf("buildTypedMessages() error = %v, want corrupt message ID", err)
	}
	if snapshot.sessionPath != manager.ResolveSessionPath(initial.Path) {
		t.Fatalf("session path after failed rebuild = %q, want %q", snapshot.sessionPath, initial.Path)
	}
}

func appendConversationSnapshotTestMessage(t *testing.T, ctx context.Context, sess *Session, content string) SaveResult {
	t.Helper()
	result, err := AppendMessageWithToolInteraction(ctx, sess, Message{
		Role:      core.RoleUser,
		Content:   content,
		Timestamp: time.Now(),
	}, nil, nil)
	if err != nil {
		t.Fatalf("append message %q: %v", content, err)
	}
	return result
}

func conversationSnapshotRefIDs(refs []lineageMessageRef) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.message.ID)
	}
	return ids
}
