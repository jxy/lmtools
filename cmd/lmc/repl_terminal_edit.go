//go:build darwin || freebsd || linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/ui"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

type terminalEditor struct {
	out      io.Writer
	history  *replHistory
	prompt   string
	original syscall.Termios
	active   bool
	skipLF   bool
	// echoed hears that the editor showed the ^C of an interrupt, so the
	// turn it interrupted does not show it again.
	echoed func(bool)
}

// onInterruptEcho tells echoed whenever the editor shows an interrupt's ^C.
func (o *inputOwner) onInterruptEcho(echoed func(bool)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t, ok := o.term.(*terminalReader); ok && t.editor != nil {
		t.editor.echoed = echoed
	}
}

func (o *inputOwner) enableEditing(out io.Writer, history *replHistory) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.term.(*terminalReader)
	if !ok || os.Getenv("TERM") == "dumb" {
		return false, nil
	}
	e := &terminalEditor{out: out, history: history, prompt: freshPrompt}
	if err := terminalIoctl(t.fd, terminalReadTermios, unsafe.Pointer(&e.original)); err != nil {
		return false, err
	}
	t.editor = e
	if err := t.resumeEditing(); err != nil {
		t.editor = nil
		return false, err
	}
	o.editing = true
	return true, nil
}

func (t *terminalReader) resumeEditing() error {
	if t.editor == nil {
		return nil
	}
	settings := t.editor.original
	settings.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ECHONL | syscall.IEXTEN
	settings.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.INLCR | syscall.IGNCR
	settings.Cc[syscall.VMIN] = 1
	settings.Cc[syscall.VTIME] = 0
	if err := terminalIoctl(t.fd, terminalWriteTermios, unsafe.Pointer(&settings)); err != nil {
		return err
	}
	t.editor.active = true
	_, err := io.WriteString(t.editor.out, "\x1b[?2004h")
	if err != nil {
		_ = t.pauseEditing()
	}
	return err
}

func (t *terminalReader) pauseEditing() error {
	if t.editor == nil || !t.editor.active {
		return nil
	}
	_, writeErr := io.WriteString(t.editor.out, "\x1b[?2004l")
	modeErr := terminalIoctl(t.fd, terminalWriteTermios, unsafe.Pointer(&t.editor.original))
	if modeErr == nil {
		t.editor.active = false
	}
	return errors.Join(writeErr, modeErr)
}

func (o *inputOwner) disableEditing() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.editing = false
	if t, ok := o.term.(*terminalReader); ok {
		return t.pauseEditing()
	}
	return nil
}

func (t *terminalReader) setPrompt(prompt string) {
	if t.editor != nil {
		t.editor.prompt = prompt
	}
}

func (t *terminalReader) editByte(ctx context.Context, beforeWait func()) (byte, error) {
	if len(t.held) == 0 {
		beforeWait()
	}
	chunk, err := t.read(ctx)
	if err != nil {
		return 0, err
	}
	if len(chunk) == 0 {
		return 0, io.EOF
	}
	b := chunk[0]
	// read's slice can alias its syscall buffer; no subsequent read occurs
	// until held is exhausted.
	t.held = chunk[1:]
	return b, nil
}

func (t *terminalReader) editKey(ctx context.Context, beforeWait func()) (string, error) {
	b, err := t.editByte(ctx, beforeWait)
	if err != nil {
		return "", err
	}
	if t.editor.skipLF {
		t.editor.skipLF = false
		if b == '\n' {
			return t.editKey(ctx, beforeWait)
		}
	}
	if b == 0x1b {
		sequence := []byte{b}
		intermediate := false
		escapeCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
		defer cancel()
		for len(sequence) < 24 {
			next, err := t.editByte(escapeCtx, beforeWait)
			if err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, io.EOF) {
					return "", err
				}
				return "\x1b", nil
			}
			valid := next >= 0x20 && next <= 0x7e
			if len(sequence) == 1 {
				valid = next == '[' || next == 'O'
			} else if next >= 0x30 && next <= 0x3f && intermediate {
				valid = false // Parameters must precede intermediate bytes.
			}
			if !valid {
				// Escape cancels search, but the independent key after it still
				// belongs to the caller. Keep it in the queue approvals flush.
				t.held = append([]byte{next}, t.held...)
				return "\x1b", nil
			}
			sequence = append(sequence, next)
			if len(sequence) > 2 && next >= 0x40 && next <= 0x7e {
				return string(sequence), nil
			}
			intermediate = intermediate || (next >= 0x20 && next <= 0x2f)
		}
		return "\x1b", nil
	}
	sequence := []byte{b}
	for !utf8.FullRune(sequence) && len(sequence) < utf8.UTFMax {
		next, err := t.editByte(ctx, beforeWait)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // Keep an incomplete prefix as invalid input before EOF.
			}
			return "", err
		}
		sequence = append(sequence, next)
	}
	// FullRune also accepts invalid prefixes. The byte that revealed one may
	// begin another rune or an input key; keep it in the queue approvals flush.
	_, size := utf8.DecodeRune(sequence)
	if size < len(sequence) {
		t.held = append(sequence[size:], t.held...)
	}
	return string(sequence[:size]), nil
}

type pasteBuilder struct {
	lineBuilder
	skipLF bool
}

// add normalizes the reader's scratch chunk in place before counting bytes.
// Only the CR state crosses chunks; no second whole-paste buffer is needed.
func (b *pasteBuilder) add(part []byte) {
	n := 0
	for _, c := range part {
		if b.skipLF && c == '\n' {
			b.skipLF = false
			continue
		}
		b.skipLF = c == '\r'
		if b.skipLF {
			c = '\n'
		}
		part[n] = c
		n++
	}
	b.lineBuilder.add(part[:n])
}

func (t *terminalReader) readPaste(ctx context.Context, bound int) (inputLine, error) {
	const end = "\x1b[201~"
	b := pasteBuilder{lineBuilder: lineBuilder{bound: bound}}
	var pending []byte
	for {
		chunk, err := t.read(ctx)
		if err != nil {
			return inputLine{}, err
		}
		if len(chunk) == 0 {
			return b.line(true), nil
		}
		data := append(pending, chunk...)
		if i := bytes.Index(data, []byte(end)); i >= 0 {
			b.add(data[:i])
			t.held = append([]byte(nil), data[i+len(end):]...)
			return b.line(false), nil
		}
		keep := min(len(end)-1, len(data))
		b.add(data[:len(data)-keep])
		pending = append([]byte(nil), data[len(data)-keep:]...)
	}
}

// submittedEchoBytes bounds how much of a submitted turn is reprinted for the
// scrollback; a turn may be ten megabytes.
const submittedEchoBytes = 16 * 1024

func (t *terminalReader) readEditedLine(ctx context.Context, bound int) (line inputLine, err error) {
	// area is what the editor painted; the shell-like prompt was written by
	// the caller, so the first repaint starts by erasing it.
	area := terminalArea{widths: []int{len(t.editor.prompt)}, cursorCol: len(t.editor.prompt)}
	defer func() {
		if errors.Is(context.Cause(ctx), errInterrupted) {
			// Ctrl-C flushes the kernel queue, but cannot discard bytes
			// already read here. They belong to the interrupted input.
			t.held = nil
			t.editor.skipLF = false
			area.toEnd(t.editor.out)
			_, _ = io.WriteString(t.editor.out, "^C")
			if t.editor.echoed != nil {
				t.editor.echoed(true)
			}
			// Cancellation can arrive during Enter's final redraw too.
			err = ctx.Err()
		}
	}()
	e := newEditBuffer(bound, t.editor.history)
	paintedColumns := t.columns()
	dirty, batch := false, 0
	// previewed is the draft as a paste preview showed it whole, with its
	// runes and lines, so the part of a draft it showed is not printed twice
	// when the draft is sent.
	previewed := ""
	previewedRunes, previewedLines := 0, 0
	redraw := func() {
		columns := t.columns()
		if !dirty && columns == paintedColumns {
			return
		}
		rows, cursorRow, cursorCol := e.rowsView(t.editor.prompt, columns, t.rows()-1)
		area.draw(t.editor.out, rows, cursorRow, cursorCol, columns)
		paintedColumns = columns
		dirty, batch = false, 0
	}
	stopResize := t.watchResize(redraw)
	defer stopResize()
	for {
		// Coalesce keys already held by the sole input reader. Repaint before
		// waiting, and bound each batch so continuous input remains responsive.
		if len(t.held) == 0 || batch >= 256 {
			redraw()
		}
		key, err := t.editKey(ctx, redraw)
		if err != nil {
			redraw()
			area.toEnd(t.editor.out)
			if errors.Is(err, io.EOF) {
				return e.inputLine(true), nil
			}
			return e.inputLine(false), err
		}
		// One CRLF is one Enter, including when Enter only accepts a search.
		if key == "\r" {
			t.editor.skipLF = true
		}
		switch {
		case key == "\x1b[200~":
			redraw()
			paste, err := t.readPaste(ctx, e.pasteLimit())
			if err != nil {
				area.toEnd(t.editor.out)
				return inputLine{text: "unfinished paste"}, err
			}
			if paste.eof {
				area.toEnd(t.editor.out)
				return inputLine{eof: true, text: "unfinished paste"}, nil
			}
			e.paste(paste)
			if !e.searching && e.lines > 1 {
				// The editor shows the line under the cursor; the preview
				// shows the whole draft once.
				area.clear(t.editor.out, t.columns())
				draft := string(e.text)
				showTextPreview(ui.NewNotifierWithWriter(t.editor.out), "Pasted draft", draft, maxEditorPreviewBytes)
				_, _ = io.WriteString(t.editor.out, "Enter sends; arrows edit; Ctrl-C discards. Type \\ and Enter to continue.\n")
				previewed = ""
				if len(draft) <= maxEditorPreviewBytes {
					previewed, previewedRunes, previewedLines = draft, len(e.text), e.lines
				}
			}
		case key == "\x04":
			// Ctrl-D ends input only on a line with nothing to edit: empty,
			// or rejected as too long. With text it deletes the character
			// under the cursor, as readline does.
			if (len(e.text) == 0 || e.tooLong) && !e.searching {
				redraw()
				area.toEnd(t.editor.out)
				return e.inputLine(true), nil
			}
			if !e.searching && e.cursor < len(e.text) {
				e.key("\x1b[3~")
			}
		case (key == "\r" || key == "\n") && !e.searching:
			// The scrollback keeps what was sent: the whole draft rather
			// than the row of it the editor last showed.
			columns := t.columns()
			switch {
			case e.tooLong:
				redraw()
				area.toEnd(t.editor.out)
				_, _ = io.WriteString(t.editor.out, "\n")
			case previewed != "" && len(e.text) >= previewedRunes && string(e.text[:previewedRunes]) == previewed:
				// A draft that still starts with what the preview showed
				// refers to it, followed by what was typed after it.
				area.clear(t.editor.out, columns)
				head := fmt.Sprintf("%s[the pasted draft above, %s]", t.editor.prompt, core.Plural(previewedLines, "line"))
				_, _ = io.WriteString(t.editor.out, e.submittedText(head, continuationFor(t.editor.prompt), previewedRunes, submittedEchoBytes))
			default:
				area.clear(t.editor.out, columns)
				_, _ = io.WriteString(t.editor.out, e.submittedText(t.editor.prompt, continuationFor(t.editor.prompt), 0, submittedEchoBytes))
			}
			return e.inputLine(false), nil
		default:
			searching := e.searching
			e.key(key)
			if !searching && e.searching {
				area.clear(t.editor.out, t.columns())
				_, _ = io.WriteString(t.editor.out, "History search: Enter accepts; Esc restores draft; Ctrl-R finds older.\n")
			}
		}
		dirty = true
		batch++
	}
}

// continuationFor is the prompt the echo of a multiline draft puts before
// each line after the first.
func continuationFor(prompt string) string {
	switch prompt {
	case freshPrompt:
		return continuationPrompt
	case approvalPrompt:
		return approvalContinuationPrompt
	}
	return prompt
}

// The signal watcher only wakes the existing reader. Join it before returning,
// so it cannot write to a closed/reused pipe or outlive an editor handoff.
func (t *terminalReader) watchResize(redraw func()) func() {
	resized := make(chan os.Signal, 1)
	stop, done := make(chan struct{}), make(chan struct{})
	signal.Notify(resized, syscall.SIGWINCH)
	t.onWake = redraw
	go func() {
		defer close(done)
		for {
			select {
			case <-resized:
				t.wake()
			case <-stop:
				return
			}
		}
	}()
	return func() {
		signal.Stop(resized)
		close(stop)
		<-done
		t.onWake = nil
	}
}

func (t *terminalReader) columns() int {
	_, columns := windowSize(uintptr(t.fd))
	return columns
}

// rows is the terminal's height, which bounds the rows the editor may paint
// and still reach the first of them again.
func (t *terminalReader) rows() int {
	rows, _ := windowSize(uintptr(t.fd))
	return rows
}
