//go:build darwin || freebsd || linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func (t *terminalReader) readEditedLine(ctx context.Context, bound int) (line inputLine, err error) {
	defer func() {
		if errors.Is(context.Cause(ctx), errInterrupted) {
			// Ctrl-C flushes the kernel queue, but cannot discard bytes
			// already read here. They belong to the interrupted input.
			t.held = nil
			t.editor.skipLF = false
			_, _ = io.WriteString(t.editor.out, "^C")
			// Cancellation can arrive during Enter's final redraw too.
			err = ctx.Err()
		}
	}()
	e := newEditBuffer(bound, t.editor.history)
	painted := terminalLine{text: t.editor.prompt, cursor: len(t.editor.prompt), columns: t.columns()}
	dirty, batch := false, 0
	redraw := func() {
		columns := t.columns()
		if !dirty && columns == painted.columns {
			return
		}
		text, cursor := e.view(t.editor.prompt, columns)
		painted.draw(t.editor.out, text, cursor, columns)
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
				return inputLine{text: "unfinished paste"}, err
			}
			if paste.eof {
				return inputLine{eof: true, text: "unfinished paste"}, nil
			}
			e.paste(paste.text, paste.tooLong)
			if !e.searching {
				painted.clear(t.editor.out, t.columns())
				showTextPreview(ui.NewNotifierWithWriter(t.editor.out), "Draft", string(e.text), maxEditorPreviewBytes)
				_, _ = io.WriteString(t.editor.out, "Enter sends; arrows edit; Ctrl-C discards.\n")
			}
		case key == "\x04":
			redraw()
			return e.inputLine(true), nil
		case (key == "\r" || key == "\n") && !e.searching:
			redraw()
			_, _ = io.WriteString(t.editor.out, "\n")
			return e.inputLine(false), nil
		default:
			searching := e.searching
			e.key(key)
			if !searching && e.searching {
				painted.clear(t.editor.out, t.columns())
				_, _ = io.WriteString(t.editor.out, "History search: Enter accepts; Esc restores draft; Ctrl-R finds older.\n")
			}
		}
		dirty = true
		batch++
	}
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
	var size struct{ Rows, Cols, X, Y uint16 }
	if terminalIoctl(t.fd, syscall.TIOCGWINSZ, unsafe.Pointer(&size)) == nil && size.Cols > 0 {
		return int(size.Cols)
	}
	return 80
}
