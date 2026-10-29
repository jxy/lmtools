package main

import (
	"strings"
	"testing"
)

// PAGER is a shell command given the file as its last argument; without it
// lmc looks for less, and without less there is no pager.
func TestPagerCommand(t *testing.T) {
	command, ok := pagerCommand("less -R")
	if !ok {
		t.Fatal("PAGER was not used")
	}
	if args := command("/tmp/show.txt").Args; len(args) != 5 || args[2] != `exec less -R "$1"` || args[4] != "/tmp/show.txt" {
		t.Fatalf("pager command = %q", args)
	}
	t.Setenv("PATH", t.TempDir())
	if _, ok := pagerCommand(""); ok {
		t.Fatal("a pager was found on an empty PATH")
	}
}

// Text is measured in the rows the terminal shows: a long line takes as many
// as the terminal wraps it over, an empty line takes one, wide characters
// take two cells, and controls other than a tab take none.
func TestDisplayRowsCountsWrappedRows(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		rows       int
	}{
		{"short line", "short\n", 1},
		{"full row", strings.Repeat("x", 80) + "\n", 1},
		{"one cell over", strings.Repeat("x", 81) + "\n", 2},
		{"long answer", strings.Repeat("word ", 1100) + "\n", 69},
		{"empty lines", "a\n\nb\n", 3},
		{"no final newline", "a\nb", 2},
		{"wide characters", strings.Repeat("\u754c", 41) + "\n", 2},
		{"combining marks", strings.Repeat("e\u0301", 80) + "\n", 1},
		{"tabs", strings.Repeat("\t", 10) + "x\n", 2},
		{"controls", strings.Repeat("\x00", 100) + "\n", 1},
	} {
		if got := displayRows(tt.text, 80); got != tt.rows {
			t.Errorf("%s: %d rows, want %d", tt.name, got, tt.rows)
		}
	}
}
