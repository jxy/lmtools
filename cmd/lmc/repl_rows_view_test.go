package main

import (
	"bytes"
	"strings"
	"testing"
)

func rowCells(row string) int {
	cells := 0
	for _, r := range row {
		cells += editRuneWidth(r)
	}
	return cells
}

// A line longer than the terminal wraps across rows, prompt first, the way it
// will read once submitted, instead of scrolling sideways behind < and >.
func TestRowsViewWrapsTheLineUnderTheCursor(t *testing.T) {
	e := newEditBuffer(1000, nil)
	e.insert("The quick brown fox jumps over the lazy dog and keeps running far away")
	rows, row, col := e.rowsView(freshPrompt, 20, 10)
	if got := strings.Join(rows, ""); got != freshPrompt+"The quick brown fox jumps over the lazy dog and keeps running far away" {
		t.Fatalf("rows lost text: %q", rows)
	}
	for _, r := range rows {
		if rowCells(r) > 19 {
			t.Fatalf("row %q passes the last column", r)
		}
	}
	if row != len(rows)-1 || col != rowCells(rows[len(rows)-1]) {
		t.Fatalf("cursor at end = row %d col %d, rows %q", row, col, rows)
	}
	e.moveCursor(4) // after "The "
	if _, row, col = e.rowsView(freshPrompt, 20, 10); row != 0 || col != len(freshPrompt)+4 {
		t.Fatalf("cursor in the first row = row %d col %d", row, col)
	}
	e.moveCursor(20)
	if _, row, col = e.rowsView(freshPrompt, 20, 10); row != 1 || col != len(freshPrompt)+20-19 {
		t.Fatalf("cursor in the second row = row %d col %d", row, col)
	}
}

func TestRowsViewKeepsWideCharactersWhole(t *testing.T) {
	e := newEditBuffer(1000, nil)
	e.insert(strings.Repeat("界", 20))
	rows, _, _ := e.rowsView(freshPrompt, 12, 20)
	for _, r := range rows {
		if rowCells(r) > 11 {
			t.Fatalf("row %q passes the last column", r)
		}
	}
	if strings.Join(rows, "") != freshPrompt+strings.Repeat("界", 20) {
		t.Fatalf("rows = %q", rows)
	}
}

// Only rows around the cursor are returned when the line is taller than the
// terminal, and a line longer than the span the view inspects is marked where
// it is cut.
func TestRowsViewWindowsAndClipsLongLines(t *testing.T) {
	e := newEditBuffer(100000, nil)
	e.insert(strings.Repeat("a", 50000))
	e.moveCursor(25000)
	rows, row, col := e.rowsView(freshPrompt, 40, 6)
	if len(rows) != 6 || row < 0 || row >= 6 || col < 0 || col > 39 {
		t.Fatalf("window rows=%d cursor=%d,%d", len(rows), row, col)
	}
	e.moveCursor(0)
	rows, _, _ = e.rowsView(freshPrompt, 40, 6)
	if !strings.HasPrefix(rows[0], freshPrompt+"a") {
		t.Fatalf("first row = %q", rows[0])
	}
	e.moveCursor(50000)
	rows, _, _ = e.rowsView(freshPrompt, 40, 6)
	if strings.Contains(rows[0], freshPrompt) {
		t.Fatalf("window at the end shows the prompt row: %q", rows)
	}
	allocs := testing.AllocsPerRun(5, func() { e.rowsView(freshPrompt, 40, 6) })
	if allocs > 1000 {
		t.Fatalf("%.0f allocations per repaint of a long line", allocs)
	}
}

func TestRowsViewNamesTheLineOfAMultilineDraft(t *testing.T) {
	e := newEditBuffer(1000, nil)
	e.paste("first\nsecond\nthird", false)
	rows, _, _ := e.rowsView(freshPrompt, 80, 10)
	if len(rows) != 1 || rows[0] != "[3/3] "+freshPrompt+"third" {
		t.Fatalf("rows = %q", rows)
	}
	e.searching = true
	if rows, _, _ = e.rowsView(freshPrompt, 80, 10); len(rows) != 1 || !strings.HasPrefix(rows[0], "search") {
		t.Fatalf("search view = %q", rows)
	}
}

func TestSubmittedTextShowsTheWholeDraft(t *testing.T) {
	e := newEditBuffer(1000, nil)
	e.paste("first\nsecond\x1b[31m", false)
	if got := e.submittedText(freshPrompt, continuationPrompt, 1000); got != freshPrompt+"first\n"+continuationPrompt+`second\u001b[31m`+"\n" {
		t.Fatalf("echo = %q", got)
	}
	if got := e.submittedText(freshPrompt, continuationPrompt, 3); got != freshPrompt+"fir [14 more characters not shown]\n" {
		t.Fatalf("bounded echo = %q", got)
	}
}

func TestTerminalAreaRedrawsFromItsFirstRow(t *testing.T) {
	var out bytes.Buffer
	var area terminalArea
	area.draw(&out, []string{"you> one", "two", "three"}, 1, 2, 20)
	if got := out.String(); got != "\r\x1b[Jyou> one\r\ntwo\r\nthree\x1b[1A\r\x1b[2C" {
		t.Fatalf("draw = %q", got)
	}
	out.Reset()
	area.draw(&out, []string{"you> x"}, 0, 6, 20)
	if got := out.String(); got != "\r\x1b[1A\x1b[Jyou> x\r\x1b[6C" {
		t.Fatalf("redraw = %q", got)
	}
	// After a shrink the terminal may wrap the rows; clearing climbs the
	// rows they now take.
	area = terminalArea{widths: []int{30, 30}, cursorRow: 1, cursorCol: 25}
	out.Reset()
	area.clear(&out, 20)
	if got := out.String(); got != "\r\x1b[3A\x1b[J" {
		t.Fatalf("clear after shrink = %q", got)
	}
	area = terminalArea{widths: []int{8, 3, 5}, cursorRow: 0, cursorCol: 2}
	out.Reset()
	area.toEnd(&out)
	if got := out.String(); got != "\x1b[2B\r\x1b[5C" {
		t.Fatalf("toEnd = %q", got)
	}
}
