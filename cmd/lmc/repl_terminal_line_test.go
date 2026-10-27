package main

import (
	"bytes"
	"strings"
	"testing"
)

// After a shrink the terminal may wrap a painted row; clear climbs to the
// row's first terminal row. A cursor at the end of a row exactly as wide as
// the terminal waits on that row.
func TestTerminalAreaClearsReflowedRows(t *testing.T) {
	for _, tt := range []struct {
		name                string
		width, col, columns int
		want                string
	}{
		{"one row", 5, 5, 12, "\r\x1b[J"},
		{"shrink at end", 14, 14, 5, "\r\x1b[2A\x1b[J"},
		{"shrink at start", 14, 2, 5, "\r\x1b[J"},
		{"cursor at wrap", 14, 5, 5, "\r\x1b[1A\x1b[J"},
		{"exactly filled", 8, 8, 4, "\r\x1b[1A\x1b[J"},
		{"one column", 3, 3, 1, "\r\x1b[2A\x1b[J"},
		{"grow", 7, 7, 80, "\r\x1b[J"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			area := terminalArea{widths: []int{tt.width}, cursorCol: tt.col}
			var out bytes.Buffer
			area.clear(&out, tt.columns)
			if out.String() != tt.want || len(area.widths) != 0 {
				t.Fatalf("clear = %q, state %+v; want %q", out.String(), area, tt.want)
			}
		})
	}
}

func TestInlineStatusClearsRowsAfterResize(t *testing.T) {
	var out bytes.Buffer
	columns := 80
	screen := &replScreen{statusOut: &out, inlineStatus: true, columns: func() int { return columns }}
	screen.setStatus("Waiting for response...")
	columns = 10
	screen.setStatus("")
	if got := out.String(); got != "Waiting for response...\r\x1b[2A\x1b[J" {
		t.Fatalf("wrapped waiting status was not erased: %q", got)
	}
	out.Reset()
	screen.setStatus("Retrying in 1s (attempt 2/3)...")
	screen.setStatus("")
	if got := out.String(); got != "Retrying \r\x1b[J" {
		t.Fatalf("status did not clip to the new width: %q", got)
	}
	if strings.Contains(out.String(), "attempt") {
		t.Fatalf("clipped status shows its tail: %q", out.String())
	}
}
