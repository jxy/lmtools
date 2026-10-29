package main

import (
	"context"
	"errors"
	"io"
	"lmtools/internal/termwidth"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// view shows text a command rendered. On a terminal with the line editor,
// text taller than the terminal, counting the rows where the terminal wraps
// a long line, opens in the operator's pager; quitting returns to the prompt
// or question. Anything else is printed.
func (l *replLoop) view(ctx context.Context, text string) {
	if l.input != nil && l.input.editing && l.tallerThanTerminal(text) {
		if cmd, ok := pagerCommand(os.Getenv("PAGER")); ok {
			err := l.page(ctx, cmd, text)
			if err == nil {
				return
			}
			if errors.Is(err, errEditorTerminal) {
				l.signals.endProcess(err)
				return
			}
			if ctx.Err() != nil {
				return
			}
			l.env.notifier.Warnf("Pager failed: %v; printing instead.", err)
		}
	}
	_, _ = io.WriteString(l.diag, text)
}

// tallerThanTerminal reports text that would not fit on the terminal the
// editor reads from, with two rows left for the prompt.
func (l *replLoop) tallerThanTerminal(text string) bool {
	rows, columns := 24, 80
	if t, ok := l.input.term.(interface{ rows() int }); ok {
		rows = t.rows()
	}
	if t, ok := l.input.term.(interface{ columns() int }); ok {
		columns = t.columns()
	}
	return displayRows(text, columns) > rows-2
}

// displayRows is how many terminal rows text takes when printed at columns:
// a line takes one row, or more where the terminal wraps it. A tab reaches
// the next multiple of eight cells, and other control characters take none.
func displayRows(text string, columns int) int {
	rows := 0
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		cells := 0
		for _, r := range line {
			switch {
			case r == '\t':
				cells += 8 - cells%8
			case unicode.IsControl(r):
			default:
				cells += termwidth.RuneWidth(r)
			}
		}
		rows += physicalRows(cells, columns)
	}
	return rows
}

// pagerCommand returns the operator's pager as a command that takes a file
// as its last argument: $PAGER, a shell command, or else less when it is
// installed.
func pagerCommand(configured string) (func(path string) *exec.Cmd, bool) {
	if strings.TrimSpace(configured) != "" {
		return func(path string) *exec.Cmd {
			// PAGER is the operator's shell command; the file stays a
			// positional argument even when the command has its own.
			return exec.Command("/bin/sh", "-c", "exec "+configured+` "$1"`, "lmc-pager", path)
		}, true
	}
	less, err := exec.LookPath("less")
	if err != nil {
		return nil, false
	}
	return func(path string) *exec.Cmd { return exec.Command(less, path) }, true
}

// page shows text in the pager, which reads it from a file private to the
// user and gets the terminal the way /ed's editor does.
func (l *replLoop) page(ctx context.Context, command func(path string) *exec.Cmd, text string) error {
	dir, err := os.MkdirTemp("", "lmc-show-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "show.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	return l.input.runEditor(ctx, command(path), func(discarded bool) {
		if discarded {
			l.env.notifier.Infof("Discarded input queued before the pager opened.")
		}
	})
}
