//go:build darwin || freebsd || linux

package main

import (
	"bytes"
	"context"
	"lmtools/internal/core"
	"lmtools/internal/ui"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestDumbTerminalKeepsPlainInput(t *testing.T) {
	t.Setenv("TERM", "dumb")
	owner, _, _ := terminalOwner(t)
	var out bytes.Buffer
	if enabled, err := owner.enableEditing(&out, nil); enabled || err != nil || out.Len() != 0 {
		t.Fatalf("editing=%v err=%v output=%q", enabled, err, out.String())
	}
}

func TestTerminalEditorPasteIsOneDraftAndRestoresMode(t *testing.T) {
	t.Setenv("TERM", "xterm")
	owner, pty, screen := terminalOwner(t)
	var before syscall.Termios
	if err := terminalIoctl(int(pty.Slave.Fd()), terminalReadTermios, unsafe.Pointer(&before)); err != nil {
		t.Fatal(err)
	}
	if enabled, err := owner.enableEditing(pty.Slave, &replHistory{}); err != nil || !enabled {
		t.Fatalf("enable=%v,%v", enabled, err)
	}
	owner.term.(*terminalReader).buf = make([]byte, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan inputLine, 1)
	errs := make(chan error, 1)
	go func() { line, err := owner.readLineUnder(ctx, 100); result <- line; errs <- err }()
	typeKeys(t, pty, "\x1b[200~one\n\ntwo\\\x1b[201~")
	if err := screen.Expect("Enter sends; arrows edit; Ctrl-C discards.", time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-result:
		t.Fatalf("paste submitted without Enter: %#v", line)
	default:
	}
	typeKeys(t, pty, "\n")
	line := <-result
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if line.text != "one\n\ntwo\\" || !line.literal {
		t.Fatalf("paste=%#v", line)
	}
	if strings.Contains(screen.String(), "\x1b[31m") {
		t.Fatal("editor introduced colors")
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	var after syscall.Termios
	if err := terminalIoctl(int(pty.Slave.Fd()), terminalReadTermios, unsafe.Pointer(&after)); err != nil {
		t.Fatal(err)
	}
	// BSD sets PENDIN when switching back to canonical mode. It is a kernel
	// reprocessing marker, not an application-selected editing setting.
	if before.Lflag&^syscall.PENDIN != after.Lflag&^syscall.PENDIN || before.Iflag != after.Iflag || before.Cc != after.Cc {
		t.Fatalf("terminal settings not restored: before=%#v after=%#v", before, after)
	}
}

func TestTerminalEditorKeepsQueuedInputOutOfApproval(t *testing.T) {
	t.Setenv("TERM", "xterm")
	owner, pty, _ := terminalOwner(t)
	if _, err := owner.enableEditing(pty.Slave, &replHistory{}); err != nil {
		t.Fatal(err)
	}
	typeKeys(t, pty, "yes\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shown := make(chan bool, 1)
	done := make(chan inputLine, 1)
	errs := make(chan error, 1)
	go func() {
		line, err := owner.answer(ctx, 100, func(discarded bool) { shown <- discarded })
		done <- line
		errs <- err
	}()
	if !<-shown {
		t.Fatal("queued approval was not reported as discarded")
	}
	select {
	case line := <-done:
		t.Fatalf("queued input answered approval: %#v", line)
	default:
	}
	typeKeys(t, pty, "no\n")
	if line := <-done; line.text != "no" {
		t.Fatalf("answer=%#v", line)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestTerminalEditorEOFDiscardsApprovalWithoutSubmitting(t *testing.T) {
	for _, input := range []string{"yes", "\x1b[200~yes\x1b[201~", "\x12\x1b[200~yes\x1b[201~\r\n"} {
		t.Run(input, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			owner, pty, screen := terminalOwner(t)
			history := &replHistory{}
			history.add("yes")
			if _, err := owner.enableEditing(pty.Slave, history); err != nil {
				t.Fatal(err)
			}
			a := newApprover(ui.NewREPLNotifier(pty.Slave), owner)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan core.Approval, 1)
			errs := make(chan error, 1)
			go func() { decision, err := a.Approve(ctx, core.UniversalCommandArgs{}); result <- decision; errs <- err }()
			if err := screen.Expect("      > ", time.Second); err != nil {
				t.Fatal(err)
			}
			typeKeys(t, pty, input+"\x04")
			decision := <-result
			if err := <-errs; err != nil || decision.Approved || decision.Reason != "" {
				t.Fatalf("EOF submitted an approval: %+v err=%v", decision, err)
			}
		})
	}
}

func TestTerminalEditorPasteSearchAndAcceptance(t *testing.T) {
	for _, test := range []struct{ name, keys, want string }{
		{"paste query", "\x12\x1b[200~wanted\x1b[201~\n\n", "wanted prompt"},
		{"CRLF accepts only", "\x12wanted\r\nX\n", "wanted promptX"},
		{"paste and CRLF", "\x12\x1b[200~wanted\x1b[201~\r\nX\n", "wanted promptX"},
		{"Unicode multiline", "\x12\x1b[200~café\r\n界\\\x1b[201~\n\n", "café\n界\\"},
		{"empty history", "draft\x12\x1b[200~absent\x1b[201~\n\n", "draft"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			owner, pty, _ := terminalOwner(t)
			h := &replHistory{}
			if test.name != "empty history" {
				h.add("café\n界\\")
				h.add("wanted prompt")
				h.add("unrelated prompt")
			}
			if _, err := owner.enableEditing(pty.Slave, h); err != nil {
				t.Fatal(err)
			}
			// Exercise UTF-8 and terminal escape decoding across read boundaries.
			owner.term.(*terminalReader).buf = make([]byte, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan lineResult, 1)
			go func() { line, err := owner.readLineUnder(ctx, 1000); done <- lineResult{line: line, err: err} }()
			typeKeys(t, pty, test.keys)
			result := <-done
			if result.err != nil || result.line.text != test.want {
				t.Fatalf("input=%q, err=%v; want %q", result.line.text, result.err, test.want)
			}
		})
	}
}

func TestTerminalEditorBatchesHeldInput(t *testing.T) {
	t.Setenv("TERM", "xterm")
	owner, pty, _ := terminalOwner(t)
	var out bytes.Buffer
	if _, err := owner.enableEditing(&out, nil); err != nil {
		t.Fatal(err)
	}
	const size = 10000
	text := strings.Repeat("a", size) + "end"
	reader := owner.term.(*terminalReader)
	reader.held = []byte(text + "\r\nqueued secret\nyes\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	line, err := owner.readLineUnder(ctx, len(text))
	if err != nil || line.text != text {
		t.Fatalf("batched input: bytes=%d err=%v", len(line.text), err)
	}
	if redraws := strings.Count(out.String(), "\x1b[2K"); redraws < 2 || redraws > size/256+3 {
		t.Fatalf("%d redraws for %d keys", redraws, size)
	}
	if strings.Contains(out.String(), "queued secret") || !strings.Contains(out.String(), "end\r") {
		t.Fatal("did not render the submitted line alone")
	}
	shown := make(chan bool, 1)
	done := make(chan lineResult, 1)
	go func() {
		answer, err := owner.answer(ctx, 100, func(discarded bool) { shown <- discarded })
		done <- lineResult{line: answer, err: err}
	}()
	if !<-shown {
		t.Fatal("batched input escaped the approval flush")
	}
	// The flush also discards the previous line's pending CRLF state. This
	// fresh Enter must be a blank answer, not a swallowed LF from that line.
	typeKeys(t, pty, "\n")
	if result := <-done; result.err != nil || result.line.text != "" || result.line.eof {
		t.Fatalf("approval did not accept a fresh blank Enter: %#v", result)
	}
}

func TestTerminalEditorRedrawsBeforeWaitingForKeyRemainder(t *testing.T) {
	for _, test := range []struct{ name, held, screen, rest, want string }{
		{"UTF-8", "abc\xc3", "> abc", "\xa9\n", "abcé"},
		{"CRLF", "\x12wanted\r\n", "> wanted prompt", "X\n", "wanted promptX"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			owner, pty, screen := terminalOwner(t)
			h := &replHistory{}
			h.add("wanted prompt")
			if _, err := owner.enableEditing(pty.Slave, h); err != nil {
				t.Fatal(err)
			}
			owner.term.(*terminalReader).held = []byte(test.held)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan lineResult, 1)
			go func() { line, err := owner.readLineUnder(ctx, 100); done <- lineResult{line: line, err: err} }()
			if err := screen.Expect(test.screen, time.Second); err != nil {
				t.Fatal(err)
			}
			typeKeys(t, pty, test.rest)
			if result := <-done; result.err != nil || result.line.text != test.want {
				t.Fatalf("fragmented input: %#v", result)
			}
		})
	}
}

func TestTerminalWakeDuringPartialKey(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct{ prefix, rest, want string }{
		{"\xc3", "\xa9", "é"}, {"\x1b[", "D", "\x1b[D"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			owner, pty, _ := terminalOwner(t)
			if _, err := owner.enableEditing(pty.Slave, nil); err != nil {
				t.Fatal(err)
			}
			reader := owner.term.(*terminalReader)
			reader.held = []byte(tt.prefix)
			woken := false
			reader.onWake = func() {
				woken = true
				// Deliver the rest only after the reader handles the wake,
				// so this cannot pass by reading a complete queued key.
				typeKeys(t, pty, tt.rest)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			key, err := reader.editKey(ctx, reader.wake)
			reader.onWake = nil
			if err != nil || key != tt.want || !woken {
				t.Fatalf("wake lost partial key: key=%q err=%v woken=%v", key, err, woken)
			}
		})
	}
}
