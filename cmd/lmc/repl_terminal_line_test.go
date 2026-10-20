package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalLineClearsReflowedRows(t *testing.T) {
	for _, tt := range []struct {
		name, text                 string
		cursor, columns, last, row int
		want                       string
	}{
		{"one row", "> abc", 5, 12, 0, 0, "\r\x1b[2K"},
		{"shrink at end", "> abcdefghijkl", 14, 5, 2, 2, "\r\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"},
		{"shrink at start", "> abcdefghijkl", 2, 5, 2, 0, "\r\x1b[2B\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"},
		{"cursor at wrap", "> abcdefghijkl", 5, 5, 2, 1, "\r\x1b[1B\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"},
		{"exactly filled", "> abcdef", 8, 4, 1, 1, "\r\x1b[2K\x1b[1A\r\x1b[2K"},
		{"wide rune wraps early", "> ab界界界", 10, 5, 2, 2, "\r\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"},
		{"combining mark", "> e\u0301x", 4, 5, 0, 0, "\r\x1b[2K"},
		{"one column", "> x", 3, 1, 2, 2, "\r\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K"},
		{"grow", "> <tail", 7, 80, 0, 0, "\r\x1b[2K"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line := terminalLine{text: tt.text, cursor: tt.cursor, columns: 80}
			if last, row := line.rows(tt.columns); last != tt.last || row != tt.row {
				t.Fatalf("rows=%d cursor row=%d; want %d %d", last, row, tt.last, tt.row)
			}
			var out bytes.Buffer
			line.clear(&out, tt.columns)
			if out.String() != tt.want || line.text != "" {
				t.Fatalf("clear=%q state=%+v", out.String(), line)
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
	if got := out.String(); got != "Waiting for response...\r\x1b[2K\x1b[1A\r\x1b[2K\x1b[1A\r\x1b[2K" {
		t.Fatalf("wrapped waiting status was not erased: %q", got)
	}
	out.Reset()
	screen.setStatus("Retrying in 1s (attempt 2/3)...")
	screen.setStatus("")
	if strings.Count(out.String(), "\x1b[2K") != 1 {
		t.Fatalf("status did not clip to the new width: %q", out.String())
	}
}
