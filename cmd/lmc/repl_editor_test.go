package main

import (
	"context"
	"encoding/json"
	"lmtools/internal/mockserver"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEditorSelectionUsesHistoryIDsAndFullTextRegex(t *testing.T) {
	var history replHistory
	for _, text := range []string{"match 42\nsecond line", "unrelated", "match 42 later", "fourth"} {
		history.add(text)
	}
	before := append([]replPrompt(nil), history.entries...)
	for _, tc := range []struct{ arg, want string }{
		{"", "current draft"},
		{"4", "fourth"},
		{"[4]2", "match 42\nsecond line\n\nmatch 42 later"},
		{"(?s)match.*second", "match 42\nsecond line"},
		{"match 42 later", "match 42 later"},
	} {
		got, err := selectEditorSeed(&history, "current draft", tc.arg, 1000)
		if err != nil || got != tc.want {
			t.Fatalf("select %q = %q, %v; want %q", tc.arg, got, err, tc.want)
		}
	}
	for _, arg := range []string{"0", "99", "9999999999999999999999999999", "[", "no match"} {
		if _, err := selectEditorSeed(&history, "current draft", arg, 1000); err == nil {
			t.Fatalf("selection %q succeeded", arg)
		}
	}
	if _, err := selectEditorSeed(&history, "old", "match", 35); err == nil {
		t.Fatal("oversized joined selection succeeded")
	}
	if _, err := selectEditorSeed(&history, "old", "1", 3); err == nil {
		t.Fatal("oversized numeric selection succeeded")
	}
	if !reflect.DeepEqual(history.entries, before) {
		t.Fatal("editing selection changed history")
	}
	for i := 0; i < maxREPLHistoryEntries; i++ {
		history.add("recent")
	}
	if _, err := selectEditorSeed(&history, "old", "1", 1000); err == nil {
		t.Fatal("expired history number remained selectable")
	}
}

func TestEditorDraftReadBoundsAndTextValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		ok   bool
	}{
		{"plain", []byte("/exit\n界"), true},
		{"empty", nil, true},
		{"too large", []byte(strings.Repeat("x", 11)), false},
		{"invalid UTF8", []byte{0xff}, false},
		{"NUL", []byte{'a', 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "draft")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readEditorDraft(path, 10)
			if (err == nil) != tc.ok || (tc.ok && got != string(tc.data)) {
				t.Fatalf("read = %q, %v; want valid=%v", got, err, tc.ok)
			}
		})
	}
	dir := t.TempDir()
	if _, err := readEditorDraft(dir, 10); err == nil {
		t.Fatal("directory accepted as draft")
	}
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("outside draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readEditorDraft(link, 100); err == nil {
		t.Fatal("replacement symlink accepted as draft")
	}
}

func TestEditorCommandPassesFilenameSeparately(t *testing.T) {
	path := filepath.Join(t.TempDir(), `file ' with $variables ; spaces.txt`)
	cmd := editorCommand(`/bin/sh -c 'printf "%s" "$1"' editor-test`, path)
	out, err := cmd.Output()
	if err != nil || string(out) != path {
		t.Fatalf("editor output=%q, err=%v, want filename %q", out, err, path)
	}
	if args := editorCommand(" ", path).Args; len(args) != 2 || args[0] != "ed" || args[1] != path {
		t.Fatalf("fallback command=%q", args)
	}
}

func TestREPLEditorRejectsPipesAndFormerResend(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, notes := newTestLoop(t, te)
	loop.draft = "prior draft"
	typeTurns(t, w, "/ed\n/ed 1\n/editor\n/again 1\n/exit\n", true)
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	if len(server.GetRequests()) != 0 || loop.draft != "prior draft" || len(loop.history.entries) != 0 {
		t.Fatal("editor command sent a prompt or changed the prior draft")
	}
	if notes.count("/ed requires terminal input") != 2 || notes.count("Unknown REPL command") != 1 || notes.count("Use /ed N") != 1 {
		t.Fatalf("notes=%q", notes.all())
	}
}

func TestREPLDraftSubmissionAndAppend(t *testing.T) {
	for _, tc := range []struct{ draft, input, want string }{
		{"draft", "\n/exit\n", "draft"},
		{"draft", " tail\\\nnext\n/exit\n", "draft tail\nnext"},
		{"/exit", "\n/exit\n", "/exit"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			server := mockserver.NewMockServer(numberedAnswers())
			defer server.Close()
			te := newTurnTestEnv(t, server)
			loop, w, _ := newTestLoop(t, te)
			loop.draft = tc.draft
			typeTurns(t, w, tc.input, true)
			if err := loop.run(false); err != nil {
				t.Fatal(err)
			}
			requests := server.GetRequests()
			encoded, _ := json.Marshal(tc.want)
			if len(requests) != 1 || !strings.Contains(requests[0].Body, string(encoded)) || loop.draft != "" || len(loop.history.entries) != 1 || loop.history.entries[0].text != tc.want {
				t.Fatalf("requests=%v, draft=%q, history=%v", requests, loop.draft, loop.history.entries)
			}
		})
	}
}

func TestReadTurnEditorContinuationAndLiteralEscape(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		how         turnRead
	}{
		{"first\\\nsecond\\\n/ed\n", "first\nsecond", readEdit},
		{"first\\\n//ed\n", "first\n/ed", readTurnText},
	} {
		owner, w := pipeOwner(t)
		owner.terminal = true // Exercise the terminal command boundary over a controlled stream.
		typeTurns(t, w, tc.input, true)
		got, how, err := readTurn(context.Background(), owner, func(string) {}, func(string, ...interface{}) {}, func() {}, false)
		if err != nil || got != tc.want || how != tc.how {
			t.Fatalf("read=%q, %v, %v; want %q, %v", got, how, err, tc.want, tc.how)
		}
		_, _, _ = readTurn(context.Background(), owner, func(string) {}, func(string, ...interface{}) {}, func() {}, false)
	}
}
