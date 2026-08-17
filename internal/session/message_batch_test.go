package session

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func batchTestEntries(count int) []MessageEntry {
	entries := make([]MessageEntry, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, MessageEntry{
			Message: Message{
				Role:      core.RoleUser,
				Content:   fmt.Sprintf("message %d", i),
				Timestamp: time.Now(),
			},
		})
	}
	return entries
}

func batchTestSession(t *testing.T) *Session {
	t.Helper()
	UseTestSessionDir(t)
	sess, err := CreateSession("", nil)
	if err != nil {
		t.Fatalf("CreateSession error = %v", err)
	}
	return sess
}

func sessionMessageIDs(t *testing.T, sessionPath string) []string {
	t.Helper()
	ids, err := listMessages(sessionPath)
	if err != nil {
		t.Fatalf("listMessages error = %v", err)
	}
	return ids
}

func TestAppendMessagesWithBlocksAssignsSequentialIDs(t *testing.T) {
	ctx := context.Background()
	sess := batchTestSession(t)

	result, err := AppendMessagesWithBlocks(ctx, sess, batchTestEntries(5))
	if err != nil {
		t.Fatalf("AppendMessagesWithBlocks error = %v", err)
	}
	if result.MessageID != "0004" {
		t.Fatalf("last MessageID = %q, want 0004", result.MessageID)
	}
	if result.Path != sess.Path {
		t.Fatalf("result.Path = %q, want %q", result.Path, sess.Path)
	}

	ids := sessionMessageIDs(t, sess.Path)
	want := []string{"0000", "0001", "0002", "0003", "0004"}
	if len(ids) != len(want) {
		t.Fatalf("message IDs = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("message IDs = %v, want %v", ids, want)
		}
	}
}

// A batch must land in the same place as the equivalent one-at-a-time loop,
// including continuing after messages that already exist.
func TestAppendMessagesWithBlocksMatchesSequentialAppends(t *testing.T) {
	ctx := context.Background()

	batched := batchTestSession(t)
	if _, err := AppendMessagesWithBlocks(ctx, batched, batchTestEntries(4)); err != nil {
		t.Fatalf("AppendMessagesWithBlocks error = %v", err)
	}

	looped := batchTestSession(t)
	for _, entry := range batchTestEntries(4) {
		result, err := AppendMessageWithBlocks(ctx, looped, entry.Message, entry.ToolCalls, entry.ToolResults, entry.Blocks)
		if err != nil {
			t.Fatalf("AppendMessageWithBlocks error = %v", err)
		}
		looped.Path = result.Path
	}

	batchedIDs := sessionMessageIDs(t, batched.Path)
	loopedIDs := sessionMessageIDs(t, looped.Path)
	if len(batchedIDs) != len(loopedIDs) {
		t.Fatalf("batched IDs = %v, sequential IDs = %v", batchedIDs, loopedIDs)
	}
	for i := range batchedIDs {
		if batchedIDs[i] != loopedIDs[i] {
			t.Fatalf("batched IDs = %v, sequential IDs = %v", batchedIDs, loopedIDs)
		}
	}
}

func TestAppendMessagesWithBlocksAppendsAfterExistingMessages(t *testing.T) {
	ctx := context.Background()
	sess := batchTestSession(t)

	if _, err := AppendMessagesWithBlocks(ctx, sess, batchTestEntries(2)); err != nil {
		t.Fatalf("first batch error = %v", err)
	}
	result, err := AppendMessagesWithBlocks(ctx, sess, batchTestEntries(2))
	if err != nil {
		t.Fatalf("second batch error = %v", err)
	}
	if result.MessageID != "0003" {
		t.Fatalf("last MessageID = %q, want 0003", result.MessageID)
	}
	if got := len(sessionMessageIDs(t, sess.Path)); got != 4 {
		t.Fatalf("message count = %d, want 4", got)
	}
}

// claimBatchAllocatedID installs a writer that races the batch: once the batch
// has scanned for its next ID, it claims the ID `offset` places along, which is
// the one entry number `offset` is about to use. It returns the claimed ID.
func claimBatchAllocatedID(t *testing.T, sessionPath string, offset int) *string {
	t.Helper()
	var claimed string
	afterGetNextMessageIDForTest = func(path, msgID string) {
		if claimed != "" || path != sessionPath {
			return
		}
		id, err := strconv.ParseUint(msgID, 16, 64)
		if err != nil {
			t.Errorf("ParseUint(%q) error = %v", msgID, err)
			return
		}
		claimed = formatVariableWidthHexID(int(id) + offset)
		if err := writeMessage(path, claimed, Message{
			ID:        claimed,
			Role:      core.RoleUser,
			Content:   "claimed by another writer",
			Timestamp: time.Now(),
		}); err != nil {
			t.Errorf("writeMessage error = %v", err)
		}
	}
	t.Cleanup(func() { afterGetNextMessageIDForTest = nil })
	return &claimed
}

// A writer that claims an ID the batch had already allocated is the stale-ID
// conflict the per-message path answers by forking, and the batch has to answer
// it the same way. Finishing with a fresh scan instead would pick the first free
// ID past that writer's message and append behind it, so a stranger's turn would
// sit in the middle of this response's lineage. Nothing may be lost either: every
// remaining message still has to be written.
func TestAppendMessagesWithBlocksForksOnIDConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		// offset is how far into the batch the conflict lands: 0 contests the very
		// first ID, 1 lets one entry commit in the original directory first.
		offset       int
		wantInParent int
	}{
		{name: "the first allocated ID", offset: 0, wantInParent: 0},
		{name: "an ID part way through the batch", offset: 1, wantInParent: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			sess := batchTestSession(t)
			sessionPath := sess.Path
			claimed := claimBatchAllocatedID(t, sessionPath, tc.offset)

			entries := batchTestEntries(3)
			result, err := AppendMessagesWithBlocks(ctx, sess, entries)
			if err != nil {
				t.Fatalf("AppendMessagesWithBlocks error = %v", err)
			}
			if *claimed == "" {
				t.Fatal("the injected writer never ran; the batch did not allocate an ID")
			}
			if sess.Path != result.Path {
				t.Fatalf("session.Path = %q, want it advanced to %q", sess.Path, result.Path)
			}

			// The fork is named for the contested ID, which is what keeps that
			// message out of the branch's lineage.
			isSibling, branchID, _ := IsSiblingDir(filepath.Base(result.Path))
			if filepath.Dir(result.Path) != sessionPath || !isSibling || branchID != *claimed {
				t.Fatalf("result.Path = %q, want a sibling of %q branching at the contested ID %s", result.Path, sessionPath, *claimed)
			}

			// The original directory keeps whatever committed before the conflict
			// plus the other writer's message, and gains nothing behind it.
			parentIDs := sessionMessageIDs(t, sessionPath)
			if len(parentIDs) != tc.wantInParent+1 {
				t.Fatalf("original directory holds %v, want %d appended messages plus the injected one", parentIDs, tc.wantInParent)
			}
			// Every entry the batch did not commit lands in the fork.
			if got := len(sessionMessageIDs(t, result.Path)); got != len(entries)-tc.wantInParent {
				t.Fatalf("sibling holds %d messages, want %d", got, len(entries)-tc.wantInParent)
			}
		})
	}
}

// A cancelled context stops the batch instead of writing the rest of the
// transcript, and the failure must not be mistaken for a fast-path miss that
// the per-message fallback should finish.
func TestAppendMessagesWithBlocksStopsOnCanceledContext(t *testing.T) {
	sess := batchTestSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := AppendMessagesWithBlocks(ctx, sess, batchTestEntries(3))
	if !stdErrors.Is(err, context.Canceled) {
		t.Fatalf("AppendMessagesWithBlocks error = %v, want context.Canceled", err)
	}
	if got := len(sessionMessageIDs(t, sess.Path)); got != 0 {
		t.Fatalf("message count = %d, want 0; the fallback wrote under a dead context", got)
	}
}

// Cancellation that arrives once the batch is already holding the lock has to be
// seen between entries, not only on the way in.
func TestAppendMessagesWithBlocksStopsWhenCanceledMidBatch(t *testing.T) {
	sess := batchTestSession(t)
	sessionPath := sess.Path
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	scanned := false
	afterGetNextMessageIDForTest = func(path, _ string) {
		if path != sessionPath {
			return
		}
		scanned = true
		cancel()
	}
	t.Cleanup(func() { afterGetNextMessageIDForTest = nil })

	_, err := AppendMessagesWithBlocks(ctx, sess, batchTestEntries(3))
	if !scanned {
		t.Fatal("the batch never reached the ID scan")
	}
	if !stdErrors.Is(err, context.Canceled) {
		t.Fatalf("AppendMessagesWithBlocks error = %v, want context.Canceled", err)
	}
	if got := len(sessionMessageIDs(t, sess.Path)); got != 0 {
		t.Fatalf("message count = %d, want 0 after cancellation", got)
	}
}

func TestAppendMessagesWithBlocksEmpty(t *testing.T) {
	ctx := context.Background()
	sess := batchTestSession(t)

	result, err := AppendMessagesWithBlocks(ctx, sess, nil)
	if err != nil {
		t.Fatalf("AppendMessagesWithBlocks error = %v", err)
	}
	if result.Path != sess.Path || result.MessageID != "" {
		t.Fatalf("result = %#v, want the session path and no message", result)
	}
	if got := len(sessionMessageIDs(t, sess.Path)); got != 0 {
		t.Fatalf("message count = %d, want 0", got)
	}
}

func TestAppendMessagesWithBlocksPreservesToolInteractionsAndBlocks(t *testing.T) {
	ctx := context.Background()
	sess := batchTestSession(t)

	entries := []MessageEntry{{
		Message: Message{Role: core.RoleAssistant, Content: "calling a tool", Timestamp: time.Now()},
		ToolCalls: []core.ToolCall{{
			ID:   "call_1",
			Name: "shell",
			Args: json.RawMessage(`{"command":"ls"}`),
		}},
		Blocks: []core.Block{core.TextBlock{Text: "calling a tool"}},
	}}

	result, err := AppendMessagesWithBlocks(ctx, sess, entries)
	if err != nil {
		t.Fatalf("AppendMessagesWithBlocks error = %v", err)
	}
	for _, suffix := range []string{".json", ".tools.json", ".blocks.json"} {
		path := filepath.Join(sess.Path, result.MessageID+suffix)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", filepath.Base(path), err)
		}
	}
}

func TestAppendMessagesWithBlocksStagesWholeBatchBeforeCommit(t *testing.T) {
	ctx := context.Background()
	sess := batchTestSession(t)
	entries := batchTestEntries(2)
	entries[1].Blocks = []core.Block{core.ReasoningBlock{Raw: json.RawMessage(`{`)}}

	if _, err := AppendMessagesWithBlocks(ctx, sess, entries); err == nil {
		t.Fatal("AppendMessagesWithBlocks error = nil, want invalid block JSON error")
	}
	if got := len(sessionMessageIDs(t, sess.Path)); got != 0 {
		t.Fatalf("message count = %d, want no committed prefix after staging failed", got)
	}
	tempFiles, err := filepath.Glob(filepath.Join(sess.Path, ".tmp-*"))
	if err != nil {
		t.Fatalf("Glob temporary files error = %v", err)
	}
	if len(tempFiles) != 0 {
		t.Fatalf("temporary files after staging failure = %v, want none", tempFiles)
	}
}
