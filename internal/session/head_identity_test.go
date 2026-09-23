//go:build !windows

package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// revisionsIn returns the revision of every message in a session directory,
// by message ID.
func revisionsIn(t *testing.T, sessionPath string) map[string]string {
	t.Helper()
	ids, err := listMessages(sessionPath)
	if err != nil {
		t.Fatalf("listMessages(%s) error = %v", sessionPath, err)
	}
	revisions := make(map[string]string, len(ids))
	for _, id := range ids {
		revision, err := readRevision(sessionPath, id)
		if err != nil {
			t.Fatalf("readRevision(%s) error = %v", id, err)
		}
		revisions[id] = revision
	}
	return revisions
}

// stripRevisions rewrites the metadata of every message in a session
// directory without a revision, the way a binary from before revisions
// writes it.
func stripRevisions(t *testing.T, sessionPath string) {
	t.Helper()
	ids, err := listMessages(sessionPath)
	if err != nil {
		t.Fatalf("listMessages(%s) error = %v", sessionPath, err)
	}
	for _, id := range ids {
		path := buildMessageFilePaths(sessionPath, id).JSONPath
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		var metadata MessageMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		metadata.Revision = ""
		if data, err = json.MarshalIndent(metadata, "", "  "); err != nil {
			t.Fatalf("MarshalIndent() error = %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
}

// Every commit writes a new revision into the message it commits: the
// system message, an append, a batch entry, and each copy a fork makes,
// which never shares its source's revision. A copy keeps its tool call's
// invocation identity.
func TestEveryCommitGivesItsMessageANewRevision(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	source := createPlanSession(t, "source system")
	var results []SaveResult
	question, err := AppendMessageWithToolInteraction(ctx, source, Message{Role: core.RoleUser, Content: "run it", Timestamp: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("append question: %v", err)
	}
	invocation := core.NewInvocationID()
	call, err := AppendMessageWithToolInteraction(ctx, source, Message{Role: core.RoleAssistant, Content: "running", Timestamp: time.Now()}, []core.ToolCall{echoCall("call_1", invocation, "hi")}, nil)
	if err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	batch, err := AppendMessagesWithBlocks(ctx, source, []MessageEntry{
		{Message: Message{Role: core.RoleUser, Content: "results", Timestamp: time.Now()}, ToolResults: []core.ToolResult{{ID: "call_1", Output: "hi"}}},
	})
	if err != nil {
		t.Fatalf("append batch: %v", err)
	}
	results = append(results, question, call, batch)

	sourceRevisions := revisionsIn(t, source.Path)
	for _, result := range results {
		if result.Revision == "" || sourceRevisions[result.MessageID] != result.Revision {
			t.Fatalf("SaveResult %+v, stored revision %q; want the new revision reported and stored", result, sourceRevisions[result.MessageID])
		}
	}

	pinned, err := OpenSession(ctx, GetSessionID(source.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	fork, err := ForkThroughHead(ctx, DefaultManager(), source.Path, *pinned.Head)
	if err != nil {
		t.Fatalf("ForkThroughHead() error = %v", err)
	}
	forkRevisions := revisionsIn(t, fork.Path)
	seen := map[string]bool{}
	for _, revisions := range []map[string]string{sourceRevisions, forkRevisions} {
		for id, revision := range revisions {
			if revision == "" || seen[revision] {
				t.Fatalf("message %s has revision %q, want a new one per commit; source %v, fork %v", id, revision, sourceRevisions, forkRevisions)
			}
			seen[revision] = true
		}
	}
	if len(forkRevisions) != len(sourceRevisions) || fork.Head.Revision != forkRevisions[fork.Head.ID] {
		t.Fatalf("fork head %+v, revisions %v; want the fork's own last revision pinned", fork.Head, forkRevisions)
	}
	interaction, err := LoadToolInteraction(fork.Path, call.MessageID)
	if err != nil || interaction == nil || len(interaction.Calls) != 1 || interaction.Calls[0].InvocationID != invocation {
		t.Fatalf("fork tool interaction = %+v, %v; want the call with invocation %s", interaction, err, invocation)
	}
}

// pinnedSource creates a session holding a system prompt, a question, and an
// answer, and returns it with a head pinned at the answer.
func pinnedSource(t *testing.T, ctx context.Context) (*Session, MessageRef) {
	t.Helper()
	source := createPlanSession(t, "source system")
	appendPlanMessage(t, ctx, source, core.RoleUser, "question")
	appendPlanMessage(t, ctx, source, core.RoleAssistant, "answer")
	pinned, err := OpenSession(ctx, GetSessionID(source.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	return source, *pinned.Head
}

// forkAgainAtItsPath deletes a fork and forks the same source through the
// same message again under another system prompt, which lands at the
// recycled path. Every copied message keeps its ID, timestamp, and text;
// only the history differs.
func forkAgainAtItsPath(t *testing.T, ctx context.Context, fork, source *Session, through MessageRef) {
	t.Helper()
	if err := DeleteNode(fork.Path); err != nil {
		t.Fatalf("DeleteNode() error = %v", err)
	}
	system := "another system"
	replacement, err := ForkSessionThroughMessageWithManager(ctx, DefaultManager(), source.Path, through.Path, through.ID, &system)
	if err != nil {
		t.Fatalf("fork the replacement: %v", err)
	}
	if replacement.Path != fork.Path {
		t.Fatalf("replacement at %s, want it at the recycled %s", replacement.Path, fork.Path)
	}
}

// continuations are the ways a turn continues a pinned head. Each prepares
// before the head's session is replaced, which the cached build needs to
// have read it, and returns the continuation.
var continuations = []struct {
	name    string
	prepare func(t *testing.T, ctx context.Context, sess *Session) func() error
}{
	{name: "append", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() error {
		return func() error {
			_, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleUser, Content: "next", Timestamp: time.Now()}, nil, nil)
			return err
		}
	}},
	{name: "batch", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() error {
		return func() error {
			_, err := AppendMessagesWithBlocks(ctx, sess, []MessageEntry{{Message: Message{Role: core.RoleUser, Content: "next", Timestamp: time.Now()}}})
			return err
		}
	}},
	{name: "cached build", prepare: func(t *testing.T, ctx context.Context, sess *Session) func() error {
		build, err := CreateCachedMessageBuilderForSession(ctx, sess)
		if err != nil {
			t.Fatalf("CreateCachedMessageBuilderForSession() error = %v", err)
		}
		if _, err := build(sess.Path); err != nil {
			t.Fatalf("first cached build: %v", err)
		}
		return func() error {
			_, err := build(sess.Path)
			return err
		}
	}},
	{name: "build", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() error {
		return func() error {
			_, err := BuildMessagesForSession(ctx, sess)
			return err
		}
	}},
	{name: "prepare", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() error {
		return func() error {
			_, err := PrepareRequestAt(ctx, newTestCoordinatorConfig(), core.NewTestNotifier(), core.TestToolUI{}, sess, "next", false, PendingToolSkip)
			return err
		}
	}},
	{name: "fork through the head", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() error {
		return func() error {
			_, err := ForkThroughHead(ctx, DefaultManager(), sess.Path, *sess.Head)
			return err
		}
	}},
	{name: "recover", prepare: func(_ *testing.T, ctx context.Context, sess *Session) func() error {
		return func() error {
			_, err := ResolvePendingToolCalls(ctx, sess, pendingToolTestConfig(), core.NewTestLogger(false), core.NewTestNotifier(), core.TestToolUI{}, &MockApprover{shouldApprove: true})
			return err
		}
	}},
}

// refusesReplacedHead runs continue and checks that it failed with a
// HeadReplacedError, wrote nothing to the session, and made no fork.
func refusesReplacedHead(t *testing.T, ctx context.Context, sess *Session, continueHead func() error) {
	t.Helper()
	lineage := lineageTexts(t, ctx, sess.Path)
	sessions := sessionDirNames(t)
	err := continueHead()
	if !errors.Is(err, ErrHeadReplaced) || errors.Is(err, ErrHeadMoved) {
		t.Fatalf("continuing a replaced head: error = %v, want %v and no conflict fork", err, ErrHeadReplaced)
	}
	if after := lineageTexts(t, ctx, sess.Path); !slices.Equal(after, lineage) {
		t.Fatalf("session lineage = %q, want it untouched: %q", after, lineage)
	}
	if after := sessionDirNames(t); !slices.Equal(after, sessions) {
		t.Fatalf("sessions = %v, want no fork: %v", after, sessions)
	}
}

// A session deleted and forked again at the same path from the same source,
// under another system prompt, holds a message with the head's path, ID,
// timestamp, and text. The head names its message by revision, so every way
// of continuing it refuses the replacement.
func TestPinnedHeadRefusesASessionForkedAgainAtItsPath(t *testing.T) {
	for _, tt := range continuations {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			source, through := pinnedSource(t, ctx)
			fork, err := ForkThroughHead(ctx, DefaultManager(), source.Path, through)
			if err != nil {
				t.Fatalf("ForkThroughHead() error = %v", err)
			}
			continueHead := tt.prepare(t, ctx, fork)
			forkAgainAtItsPath(t, ctx, fork, source, through)
			refusesReplacedHead(t, ctx, fork, continueHead)
		})
	}
}

// A head whose message was deleted, or deleted and written again under the
// same ID by another run, is replaced too. A write refuses it and makes no
// fork, since the history to fork through is gone.
func TestPinnedHeadRefusesADeletedMessage(t *testing.T) {
	tests := []struct {
		name    string
		rewrite bool
	}{
		{name: "deleted"},
		{name: "written again", rewrite: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			source, through := pinnedSource(t, ctx)
			pinned, err := OpenSession(ctx, GetSessionID(source.Path))
			if err != nil {
				t.Fatalf("OpenSession() error = %v", err)
			}
			if err := DeleteNode(filepath.Join(source.Path, through.ID)); err != nil {
				t.Fatalf("DeleteNode() error = %v", err)
			}
			if tt.rewrite {
				if id := appendPlanMessage(t, ctx, source, core.RoleAssistant, "answer"); id != through.ID {
					t.Fatalf("rewritten answer is %s, want it at the deleted %s", id, through.ID)
				}
			}
			refusesReplacedHead(t, ctx, pinned, func() error {
				_, err := AppendMessageWithToolInteraction(ctx, pinned, Message{Role: core.RoleUser, Content: "next", Timestamp: time.Now()}, nil, nil)
				return err
			})
		})
	}
}

// A message committed before revisions existed is pinned by a fingerprint of
// the lineage through it. A legacy lineage that did not change continues,
// and the head moves to the revision of the message the write commits.
func TestLegacyHeadContinuesWhileItsLineageIsUnchanged(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "legacy system")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "answer")
	stripRevisions(t, sess.Path)
	sess, err := OpenSession(ctx, GetSessionID(sess.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	if !strings.HasPrefix(sess.Head.Revision, lineageFingerprint) {
		t.Fatalf("legacy head = %+v, want it pinned by a lineage fingerprint", sess.Head)
	}

	result, err := AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleUser, Content: "next", Timestamp: time.Now()}, nil, nil)
	if err != nil {
		t.Fatalf("append after a legacy head: %v", err)
	}
	if result.Revision == "" || sess.Head.Revision != result.Revision {
		t.Fatalf("head after the write = %+v, want the new message's revision %q", sess.Head, result.Revision)
	}
}

// The same replacement among messages from before revisions. The head's own
// metadata matches the replacement's byte for byte, because a copy keeps its
// source's timestamp, so only the fingerprint of the lineage through the head
// tells the two histories apart, and it refuses the replacement.
func TestLegacyHeadRefusesASessionForkedAgainAtItsPath(t *testing.T) {
	for _, tt := range continuations {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			source, through := pinnedSource(t, ctx)
			fork, err := ForkThroughHead(ctx, DefaultManager(), source.Path, through)
			if err != nil {
				t.Fatalf("ForkThroughHead() error = %v", err)
			}
			stripRevisions(t, fork.Path)
			if fork, err = OpenSession(ctx, GetSessionID(fork.Path)); err != nil {
				t.Fatalf("OpenSession() error = %v", err)
			}
			if !strings.HasPrefix(fork.Head.Revision, lineageFingerprint) {
				t.Fatalf("legacy head = %+v, want it pinned by a lineage fingerprint", fork.Head)
			}
			headFile := buildMessageFilePaths(fork.Path, fork.Head.ID).JSONPath
			original, err := os.ReadFile(headFile)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			continueHead := tt.prepare(t, ctx, fork)

			forkAgainAtItsPath(t, ctx, fork, source, through)
			stripRevisions(t, fork.Path)
			if replaced, err := os.ReadFile(headFile); err != nil || !bytes.Equal(replaced, original) {
				t.Fatalf("replacement head metadata = %q, %v; the test needs it identical to %q", replaced, err, original)
			}
			refusesReplacedHead(t, ctx, fork, continueHead)
		})
	}
}
