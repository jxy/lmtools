//go:build darwin || freebsd || linux

package main

import (
	"context"
	"errors"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/ui"
	"strings"
	"testing"
	"time"
)

// editorWriteHook places an interrupt at a deterministic point in a redraw,
// while the sole input reader still holds bytes from the cancelled submission.
type editorWriteHook func(string)

func (f editorWriteHook) Write(p []byte) (int, error) {
	f(string(p))
	return len(p), nil
}

func TestTerminalEditorInterruptDiscardsReadAhead(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct{ name, keys, interruptAt string }{
		{"typed input", strings.Repeat("x", 256) + "UNSENT\n", "xxxxx"},
		{"history search", "\x12query\r\nUNSENT\n", "History search:"},
		{"paste start", "draft\x1b[200~UNSENT\n\x1b[201~\n", "> draft"},
		{"paste preview", "\x1b[200~draft\x1b[201~\nUNSENT\n", "End draft."},
		{"partial UTF-8", "draft\xc3", "> draft"},
		{"partial escape", "draft\x1b[", "> draft"},
		{"Enter redraw", "draft\rUNSENT\n", "> draft"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner, pty, _ := terminalOwner(t)
			deadline, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			ctx, cancel := context.WithCancelCause(deadline)
			defer cancel(nil)
			interrupted := false
			var screen strings.Builder
			out := editorWriteHook(func(text string) {
				screen.WriteString(text)
				if !interrupted && strings.Contains(text, tt.interruptAt) {
					interrupted = true
					cancel(errInterrupted)
				}
			})
			history := &replHistory{}
			history.add("earlier prompt")
			if _, err := owner.enableEditing(out, history); err != nil {
				t.Fatal(err)
			}
			owner.term.(*terminalReader).held = []byte(tt.keys)
			_, err := owner.readLineUnder(ctx, 1024)
			if !interrupted || !errors.Is(err, context.Canceled) {
				t.Errorf("interrupt lost during input: interrupted=%v err=%v", interrupted, err)
			}
			if count := strings.Count(screen.String(), "^C"); count != 1 {
				t.Errorf("interrupt marker appeared %d times: %q", count, screen.String())
			}

			// New input must start cleanly, including a blank Enter after a
			// cancelled CR. A retained suffix or skipLF changes these lines.
			typeKeys(t, pty, "\nfresh\n")
			readCtx, end := context.WithTimeout(context.Background(), time.Second)
			defer end()
			for _, want := range []string{"", "fresh"} {
				if line, err := owner.readLineUnder(readCtx, 1024); err != nil || line.text != want || line.eof || line.tooLong {
					t.Fatalf("fresh input=%+v err=%v; want %q", line, err, want)
				}
			}
		})
	}
}

func TestTerminalEditorGenericCancellationKeepsQueuedInput(t *testing.T) {
	t.Setenv("TERM", "xterm")
	owner, _, _ := terminalOwner(t)
	if _, err := owner.enableEditing(io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	owner.term.(*terminalReader).held = []byte("queued\r\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.readLineUnder(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	readCtx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if line, err := owner.readLineUnder(readCtx, 100); err != nil || line.text != "queued" {
		t.Fatalf("generic cancellation consumed queued input: %+v, %v", line, err)
	}
}

func TestTerminalEditorInterruptedApprovalCannotBecomePrompt(t *testing.T) {
	t.Setenv("TERM", "xterm")
	owner, pty, _ := terminalOwner(t)
	reader := owner.term.(*terminalReader)
	deadline, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	ctx, cancel := context.WithCancelCause(deadline)
	defer cancel(nil)
	shown, interrupted := false, false
	out := editorWriteHook(func(text string) {
		if !shown && strings.HasSuffix(text, "\n      > ") {
			shown = true
			// Input arrives after the approval's initial flush.
			reader.held = []byte(strings.Repeat("x", 256) + "yes\n")
		}
		if !interrupted && strings.Contains(text, "xxxxx") {
			interrupted = true
			cancel(errInterrupted)
		}
	})
	if _, err := owner.enableEditing(out, nil); err != nil {
		t.Fatal(err)
	}
	approver := newApprover(ui.NewREPLNotifier(out), owner)
	decision, err := approver.Approve(ctx, core.UniversalCommandArgs{})
	if !shown || !interrupted || !errors.Is(err, context.Canceled) || decision.Approved || decision.Reason != "" {
		t.Fatalf("cancelled approval=%+v err=%v shown=%v interrupted=%v", decision, err, shown, interrupted)
	}
	typeKeys(t, pty, "fresh\n")
	readCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if line, err := owner.readLineUnder(readCtx, 1024); err != nil || line.text != "fresh" {
		t.Fatalf("cancelled approval leaked into prompt: %+v err=%v", line, err)
	}
}

func TestREPLInterruptedDraftDoesNotSendBufferedSuffix(t *testing.T) {
	t.Setenv("TERM", "xterm")
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	loop, _, notes := newTestLoop(t, newTurnTestEnv(t, server))
	owner, pty, _ := terminalOwner(t)
	loop.input, loop.terminal = owner, true
	interrupted, exitSent := false, false
	out := editorWriteHook(func(text string) {
		if !interrupted && strings.Contains(text, "xxxxx") {
			interrupted = true
			loop.signals.interrupt()
		}
	})
	if _, err := owner.enableEditing(out, &loop.history); err != nil {
		t.Fatal(err)
	}
	owner.term.(*terminalReader).held = []byte(strings.Repeat("x", 256) + "UNSENT\n")
	loop.diag = editorWriteHook(func(text string) {
		if interrupted && !exitSent && text == freshPrompt {
			exitSent = true
			typeKeys(t, pty, "/exit\n")
		}
	})
	timer := time.AfterFunc(3*time.Second, func() { loop.signals.endProcess(context.DeadlineExceeded) })
	defer timer.Stop()
	if err := loop.run(false); err != nil {
		t.Fatal(err)
	}
	if !exitSent || notes.count("Discarded the unfinished turn.") != 1 {
		t.Fatalf("did not return to a fresh prompt: %q", notes.all())
	}
	if requests := server.GetRequests(); len(requests) != 0 {
		t.Fatalf("cancelled draft became a provider request: %s", requests[0].Body)
	}
}
