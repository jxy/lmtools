package main

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"testing"
	"time"
)

// store opens the sessions directory of a test environment.
func (te turnTestEnv) store() *session.Store {
	return session.Open(te.sessionsDir)
}

// textEntry is a message of role saying text.
func textEntry(role core.Role, text string) session.Entry {
	return session.Entry{Role: role, Time: time.Now().UTC(), Content: session.Content{Blocks: []core.Block{core.TextBlock{Text: text}}}}
}

// newTestSession makes a session in store holding entries as a chain, and
// returns the session at the last of them.
func newTestSession(t *testing.T, store *session.Store, entries ...session.Entry) *session.Session {
	t.Helper()
	info, _, err := store.Create(context.Background())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	sess := &session.Session{Store: store, Number: info.Number, Identity: info.Identity}
	if len(entries) > 0 {
		if _, err := sess.Append(context.Background(), entries...); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	return sess
}

// appendTestSessionMessage saves a message of role saying content after the
// session's head.
func appendTestSessionMessage(t *testing.T, ctx context.Context, sess *session.Session, role core.Role, content string) {
	t.Helper()
	if _, err := sess.Append(ctx, textEntry(role, content)); err != nil {
		t.Fatalf("Append(%s, %q) error = %v", role, content, err)
	}
}

// saveToolCalls saves a reply holding calls after the session's head.
func saveToolCalls(t *testing.T, sess *session.Session, text string, calls []core.ToolCall) session.Message {
	t.Helper()
	m, err := sess.SaveReply(context.Background(), core.Response{Text: text, ToolCalls: calls}, "gpt-test")
	if err != nil {
		t.Fatalf("SaveReply() error = %v", err)
	}
	return m
}

// historyOf returns the history at the session's head as requests carry it,
// settings records included as empty system messages.
func historyOf(t *testing.T, sess *session.Session) []core.TypedMessage {
	t.Helper()
	h, err := sess.History()
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	return h.Typed()
}

// historyAt returns the history ending at the message at.
func historyAt(t *testing.T, store *session.Store, at session.Ref) session.History {
	t.Helper()
	h, err := store.History(at)
	if err != nil {
		t.Fatalf("History(%s) error = %v", at, err)
	}
	return h
}

// newestTools returns the tool calls or results the newest message of the
// session at holds.
func newestTools(t *testing.T, sess *session.Session) *core.ToolInteraction {
	t.Helper()
	c, err := sess.Store.Content(sess.Ref())
	if err != nil {
		t.Fatalf("Content(%s) error = %v", sess.Ref(), err)
	}
	return c.Tools
}

// awaitsAnswer reports whether the session's head awaits an answer.
func awaitsAnswer(t *testing.T, sess *session.Session) bool {
	t.Helper()
	awaits, err := sess.AwaitsAnswer()
	if err != nil {
		t.Fatalf("AwaitsAnswer() error = %v", err)
	}
	return awaits
}

// claimedCalls gives calls invocation IDs and records them in the journal as
// claimed and never started, as a run that saved them and stopped leaves
// them, so a later run executes them.
func claimedCalls(t *testing.T, store *session.Store, calls []core.ToolCall) []core.ToolCall {
	t.Helper()
	core.AssignInvocationIDs(calls)
	claim, err := store.Journal().Claim(calls)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	claim.Release()
	return calls
}
