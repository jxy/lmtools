package main

import (
	"fmt"
	"io"
)

// terminalLine records only the displayed viewport, never the whole draft.
// After a shrink it can span several physical rows, with the cursor in any one.
type terminalLine struct {
	text            string
	cursor, columns int
}

func (l terminalLine) rows(columns int) (last, cursor int) {
	columns = max(1, columns)
	cells, column := 0, 0
	for _, r := range l.text {
		width := min(editRuneWidth(r), columns)
		if width > 0 && column+width > columns {
			last++
			column = 0
		}
		if cells <= l.cursor {
			cursor = last
		}
		column += width
		cells += width
	}
	return last, cursor
}

// clear erases from the last occupied row upward, ending at the line's origin.
// Walking rows also handles a cursor that was moved into the middle of input.
func (l *terminalLine) clear(out io.Writer, columns int) {
	last, cursor := l.rows(columns)
	_, _ = io.WriteString(out, "\r")
	if last > cursor {
		_, _ = fmt.Fprintf(out, "\x1b[%dB", last-cursor)
	}
	_, _ = io.WriteString(out, "\x1b[2K")
	for row := last; row > 0; row-- {
		_, _ = io.WriteString(out, "\x1b[1A\r\x1b[2K")
	}
	*l = terminalLine{columns: columns}
}

func (l *terminalLine) draw(out io.Writer, text string, cursor, columns int) {
	l.clear(out, columns)
	_, _ = fmt.Fprintf(out, "%s\r", text)
	if cursor > 0 {
		_, _ = fmt.Fprintf(out, "\x1b[%dC", cursor)
	}
	*l = terminalLine{text: text, cursor: cursor, columns: columns}
}
