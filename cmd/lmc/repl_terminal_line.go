package main

import (
	"fmt"
	"io"
)

// terminalArea is a block of rows painted last, by the editor or as the
// status line: the width of each row and the row and column of the cursor.
// Rows are painted at most columns-1 cells wide, so none wraps when painted;
// after a shrink the terminal may wrap them, which clear allows for.
type terminalArea struct {
	widths               []int
	cursorRow, cursorCol int
}

// physicalRows is how many terminal rows a painted row of cells occupies at
// columns.
func physicalRows(cells, columns int) int {
	columns = max(1, columns)
	return max(1, (cells+columns-1)/columns)
}

// cursorRowsDown is how many terminal rows below the start of its painted
// row the cursor sits. At the end of a row exactly as wide as the terminal,
// the cursor waits on that row rather than starting the next.
func cursorRowsDown(col, width, columns int) int {
	columns = max(1, columns)
	if col > 0 && col == width && col%columns == 0 {
		return col/columns - 1
	}
	return col / columns
}

// clear moves to the area's first row and erases from there down.
func (a *terminalArea) clear(out io.Writer, columns int) {
	up := 0
	for row := 0; row < a.cursorRow && row < len(a.widths); row++ {
		up += physicalRows(a.widths[row], columns)
	}
	if a.cursorRow < len(a.widths) {
		up += cursorRowsDown(a.cursorCol, a.widths[a.cursorRow], columns)
	}
	_, _ = io.WriteString(out, "\r")
	if up > 0 {
		_, _ = fmt.Fprintf(out, "\x1b[%dA", up)
	}
	_, _ = io.WriteString(out, "\x1b[J")
	*a = terminalArea{}
}

// draw replaces the area with rows and puts the cursor at cursorRow and
// cursorCol, counted in cells.
func (a *terminalArea) draw(out io.Writer, rows []string, cursorRow, cursorCol, columns int) {
	a.clear(out, columns)
	widths := make([]int, len(rows))
	for i, row := range rows {
		if i > 0 {
			_, _ = io.WriteString(out, "\r\n")
		}
		_, _ = io.WriteString(out, row)
		for _, r := range row {
			widths[i] += editRuneWidth(r)
		}
	}
	if up := len(rows) - 1 - cursorRow; up > 0 {
		_, _ = fmt.Fprintf(out, "\x1b[%dA", up)
	}
	_, _ = io.WriteString(out, "\r")
	if cursorCol > 0 {
		_, _ = fmt.Fprintf(out, "\x1b[%dC", cursorCol)
	}
	*a = terminalArea{widths: widths, cursorRow: cursorRow, cursorCol: cursorCol}
}

// toEnd puts the cursor after the area's last row, where what follows the
// area belongs: the ^C of an interrupt, or the line Ctrl-D ends.
func (a *terminalArea) toEnd(out io.Writer) {
	if len(a.widths) == 0 {
		return
	}
	last := len(a.widths) - 1
	if down := last - a.cursorRow; down > 0 {
		_, _ = fmt.Fprintf(out, "\x1b[%dB", down)
	}
	_, _ = io.WriteString(out, "\r")
	if a.widths[last] > 0 {
		_, _ = fmt.Fprintf(out, "\x1b[%dC", a.widths[last])
	}
	a.cursorRow, a.cursorCol = last, a.widths[last]
}
