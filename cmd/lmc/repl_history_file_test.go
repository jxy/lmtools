package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type recordedWarnings []string

func (w *recordedWarnings) record(format string, args ...interface{}) {
	*w = append(*w, fmt.Sprintf(format, args...))
}

func writeHistoryLines(t *testing.T, dir string, lines ...string) {
	t.Helper()
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, replHistoryFileName), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func historyFileLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, replHistoryFileName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func historyTexts(entries []replPrompt) []string {
	texts := make([]string, len(entries))
	for i, entry := range entries {
		texts[i] = entry.text
	}
	return texts
}

// History reaches the prompts and commands of an earlier REPL, and commands
// come back as typed while prompts keep their protective slash. An entry
// whose saved line would pass the bound, through JSON's escaping of a short
// text or by its own length, stays in the REPL that typed it.
func TestREPLHistoryPersistsAcrossLoops(t *testing.T) {
	dir := t.TempDir()
	var first replHistory
	loadHistoryFile(&first, dir, nil)
	first.add("/literal prompt")
	first.addCommand("/info")
	escaped := strings.Repeat("<", maxSavedHistoryLineBytes/6+1)
	first.add(escaped)
	first.add(strings.Repeat("x", maxSavedHistoryLineBytes+1))
	if len(escaped) > maxSavedHistoryLineBytes/4 || first.entries[2].text != escaped || len(first.entries) != 4 {
		t.Fatalf("the escaped entry is %d bytes and kept as %d; %d entries", len(escaped), len(first.entries[2].text), len(first.entries))
	}

	if saved := historyFileLines(t, dir); len(saved) != 2 {
		t.Fatalf("history file holds %d lines, want the first two entries", len(saved))
	}

	var second replHistory
	loadHistoryFile(&second, dir, nil)
	if len(second.entries) != 2 || second.entries[0].text != "/literal prompt" || !second.entries[1].command {
		t.Fatalf("reloaded = %+v", second.entries)
	}
	e := newEditBuffer(1000, &second)
	e.key("\x1b[A")
	if string(e.text) != "/info" {
		t.Fatalf("command recall = %q", string(e.text))
	}
	e.key("\x1b[A")
	if string(e.text) != "//literal prompt" {
		t.Fatalf("prompt recall = %q", string(e.text))
	}
	if prompts := second.prompts(); len(prompts) != 1 {
		t.Fatalf("/history lists commands: %+v", prompts)
	}
	info, err := os.Stat(filepath.Join(dir, replHistoryFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("history file = %v, %v", info, err)
	}
	if err := second.clear(); err != nil {
		t.Fatal(err)
	}
	var third replHistory
	loadHistoryFile(&third, dir, nil)
	if len(third.entries) != 0 {
		t.Fatalf("cleared history came back: %+v", third.entries)
	}
}

// A long history is rewritten to what is kept when it is loaded.
func TestREPLHistoryFileIsCompacted(t *testing.T) {
	dir := t.TempDir()
	var h replHistory
	loadHistoryFile(&h, dir, nil)
	for i := 0; i < 3*maxREPLHistoryEntries; i++ {
		h.add("prompt")
	}
	var reloaded replHistory
	loadHistoryFile(&reloaded, dir, nil)
	if lines := historyFileLines(t, dir); len(lines) != maxREPLHistoryEntries {
		t.Fatalf("history file holds %d lines, want %d", len(lines), maxREPLHistoryEntries)
	}
}

// A line longer than any this file writes is passed over quietly, and the
// entries after it load.
func TestREPLHistorySkipsALineTooLongToHaveBeenSaved(t *testing.T) {
	dir := t.TempDir()
	long := `{"text":"` + strings.Repeat("x", 3*maxSavedHistoryLineBytes) + `"}`
	writeHistoryLines(t, dir, `{"text":"first"}`, long, `{"text":"second"}`, `{"text":"third"}`)
	var warned recordedWarnings
	var h replHistory
	loadHistoryFile(&h, dir, warned.record)
	if len(warned) != 0 {
		t.Fatalf("warned: %q", warned)
	}
	if got := historyTexts(h.entries); !slices.Equal(got, []string{"first", "second", "third"}) {
		t.Fatalf("loaded %q", got)
	}
}

// Compaction keeps the newest entries even when a damaged line sits among
// them.
func TestREPLHistoryCompactionKeepsEntriesAfterADamagedLine(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 3*maxREPLHistoryEntries; i++ {
		if i == 2*maxREPLHistoryEntries+50 {
			lines = append(lines, `{"text":"`+strings.Repeat("x", 3*maxSavedHistoryLineBytes)+`"}`)
		}
		lines = append(lines, fmt.Sprintf(`{"text":"p%d"}`, i))
	}
	writeHistoryLines(t, dir, lines...)
	var h replHistory
	loadHistoryFile(&h, dir, nil)
	texts := historyTexts(h.entries)
	if len(texts) != maxREPLHistoryEntries || texts[0] != "p200" || texts[len(texts)-1] != "p299" {
		t.Fatalf("loaded %d entries: %q", len(texts), texts)
	}
	saved := historyFileLines(t, dir)
	if len(saved) != maxREPLHistoryEntries || saved[len(saved)-1] != `{"text":"p299"}` {
		t.Fatalf("compacted file holds %d lines ending %q", len(saved), saved[len(saved)-1])
	}
}

// A file larger than the span a load reads is compacted, though it holds few
// lines.
func TestREPLHistoryFileIsCompactedBySize(t *testing.T) {
	dir := t.TempDir()
	text := strings.Repeat("x", 60*1024)
	count := maxHistoryFileBytes/len(text) + 10
	if count >= 2*maxREPLHistoryEntries {
		t.Fatalf("%d lines would be compacted by count", count)
	}
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fmt.Sprintf(`{"text":"%d %s"}`, i, text)
	}
	writeHistoryLines(t, dir, lines...)
	var h replHistory
	loadHistoryFile(&h, dir, nil)
	info, err := os.Stat(filepath.Join(dir, replHistoryFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxHistoryFileBytes {
		t.Fatalf("history file is %d bytes after a load, bound %d", info.Size(), maxHistoryFileBytes)
	}
	if last := h.entries[len(h.entries)-1].text; !strings.HasPrefix(last, fmt.Sprintf("%d ", count-1)) {
		t.Fatalf("newest entry starts %q", last[:10])
	}
}

// The first REPL can run before any session has created the sessions
// directory; its history is saved all the same.
func TestREPLHistoryCreatesTheSessionsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "sessions")
	var warned recordedWarnings
	var h replHistory
	loadHistoryFile(&h, dir, warned.record)
	h.add("first prompt")
	if len(warned) != 0 {
		t.Fatalf("warned: %q", warned)
	}
	var reloaded replHistory
	loadHistoryFile(&reloaded, dir, nil)
	if got := historyTexts(reloaded.entries); !slices.Equal(got, []string{"first prompt"}) {
		t.Fatalf("reloaded %q", got)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("sessions directory = %v, %v", info, err)
	}
}
