package session

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// show prints the history ending at at.
func show(t *testing.T, s *Store, at Ref, options ShowOptions) string {
	t.Helper()
	var b bytes.Buffer
	if err := s.Show(context.Background(), &b, at, options); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// -show labels each message with its ID and its parent's, since IDs from
// other branches can fall between them, names the alternatives of a message
// that has them, and lists what a settings record sets.
func TestShowLabelsParentsAndAlternatives(t *testing.T) {
	s := newStore(t)
	sess := conversation(t, s)
	asked := commit(t, prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "q2"}))
	other := commit(t, prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "other q2"}))
	reply(t, other, "a2")
	out := show(t, s, other.Ref(), ShowOptions{})
	for _, want := range []string{
		"Conversation " + other.Ref().String() + ":",
		"[settings] ", "· 0001, the first message", "system prompt: sys",
		"[user] ", "· 0002 after 0001",
		"[assistant/model] ", "· 0003 after 0002",
		"· 0005 after 0003 · alternatives " + asked.Head, "other q2",
		"· 0006 after 0005", "a2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("-show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nq2\n") {
		t.Fatalf("-show printed the other branch:\n%s", out)
	}
}

// The last turns start at a question; the system prompt alone says which
// prompt is in force.
func TestShowLastTurnsAndSystemPrompt(t *testing.T) {
	s := newStore(t)
	sess := conversation(t, s)
	asked := commit(t, prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "q2"}))
	reply(t, asked, "a2")
	out := show(t, s, asked.Ref(), ShowOptions{LastTurns: 1})
	if !strings.Contains(out, "last 1 turn") || strings.Contains(out, "q1") || !strings.Contains(out, "q2") {
		t.Fatalf("the last turn:\n%s", out)
	}
	if out := show(t, s, asked.Ref(), ShowOptions{SystemOnly: true}); !strings.Contains(out, "System prompt of conversation") || !strings.Contains(out, "sys") {
		t.Fatalf("custom prompt:\n%s", out)
	}
	builtin := commit(t, prepare(t, s, asked.Ref(), Change{Builtin: true}, Turn{Prompt: "q3"}))
	if out := show(t, s, builtin.Ref(), ShowOptions{SystemOnly: true}); !strings.Contains(out, "built-in system prompt") {
		t.Fatalf("built-in prompt:\n%s", out)
	}
	none := commit(t, prepare(t, s, asked.Ref(), Change{System: custom("")}, Turn{Prompt: "q3"}))
	if out := show(t, s, none.Ref(), ShowOptions{SystemOnly: true}); !strings.Contains(out, "sends no system prompt") {
		t.Fatalf("no prompt:\n%s", out)
	}
	empty := create(t, s)
	if out := show(t, s, Ref{Session: empty.Number}, ShowOptions{}); !strings.Contains(out, "has no messages") {
		t.Fatalf("an empty session:\n%s", out)
	}
}

// -show-sessions lists each session with its message tree, indented where
// alternatives start, and names a session in the old format with how to
// remove it.
func TestListShowsTreesAndOldSessions(t *testing.T) {
	s := newStore(t)
	var b bytes.Buffer
	if err := s.List(&b); err != nil || !strings.Contains(b.String(), "No sessions found.") {
		t.Fatalf("an empty directory: %v\n%s", err, b.String())
	}
	sess := conversation(t, s)
	commit(t, prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "q2"}))
	commit(t, prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "other q2"}))
	makeOld(t, s, "0002")
	b.Reset()
	if err := s.List(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"0001 • ", "5 messages • " + sess.Identity,
		"  0001 settings\n", "  0002 user: q1\n", "  0003 assistant: a1\n",
		"  ├─ 0004 user: q2\n", "  └─ 0005 user: other q2\n",
		"0002 • older format", "-delete 0002",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("-show-sessions lacks %q:\n%s", want, out)
		}
	}
}
