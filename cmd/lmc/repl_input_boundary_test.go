//go:build darwin || freebsd || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/ui"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTerminalEditorDiscardsOversizedTurn(t *testing.T) {
	t.Setenv("TERM", "xterm")
	defer func(limit int) { maxTurnBytes = limit }(maxTurnBytes)
	maxTurnBytes = 10
	long := strings.Repeat("x", 16)
	for _, tt := range []struct{ name, keys string }{
		{"one line", long + "\n"},
		{"continued", long + "\\\nyes\\\ntail\n"},
		{"later line", "part\\\n" + long + "\\\nyes\n"},
		{"cumulative", "123456\\\n123456\\\nyes\n"},
		{"CRLF", long + "\\\r\nyes\r\n"},
		{"doubled backslash", long + "\\\\\n"},
		{"UTF-8", strings.Repeat("界", 6) + "\\\nyes\n"},
		{"literal paste", "\x1b[200~" + long + "\nyes\\\x1b[201~\n"},
		{"middle insertion preserves continued suffix", "123456789\\\x01abcdefgh\ntail\n"},
		{"middle insertion is not a trailing marker", "1234567890\x01ab\\\n"},
		{"insertion joins doubled backslash", "123456789\\\x1b[Dab\\\n"},
		{"middle insertion preserves doubled suffix", "12345678\\\\\x01abcd\\\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner, _, _ := terminalOwner(t)
			if _, err := owner.enableEditing(io.Discard, nil); err != nil {
				t.Fatal(err)
			}
			owner.term.(*terminalReader).held = []byte(tt.keys + "ok\n")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var notes []string
			text, how, err := readTurn(ctx, owner, func(string) {}, func(format string, args ...interface{}) {
				notes = append(notes, fmt.Sprintf(format, args...))
			}, func() {}, false)
			if err != nil || how != readTurnText || text != "ok" {
				t.Fatalf("discarded turn leaked into next prompt: text=%q how=%v err=%v", text, how, err)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], "longer than 10 bytes") {
				t.Fatalf("discard notices=%q", notes)
			}
		})
	}
}

func TestTerminalEditorOversizedApprovalDeniesWholeSubmission(t *testing.T) {
	t.Setenv("TERM", "xterm")
	defer func(limit int) { maxTurnBytes = limit }(maxTurnBytes)
	maxTurnBytes = 10
	long := strings.Repeat("x", 16)
	for _, tt := range []struct{ name, keys string }{
		{"one line", long + "\n"},
		{"continued", long + "\\\nyes\n"},
		{"later line", "part\\\n" + long + "\\\nyes\n"},
		{"cumulative", "123456\\\n123456\\\nyes\n"},
		{"CRLF", long + "\\\r\nyes\r\n"},
		{"literal paste", "\x1b[200~" + long + "\\\x1b[201~\n"},
		{"middle insertion preserves continued suffix", "123456789\\\x01abcdefgh\ntail\n"},
		{"middle insertion is not a trailing marker", "1234567890\x01ab\\\n"},
		{"insertion joins doubled backslash", "123456789\\\x1b[Dab\\\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner, pty, screen := terminalOwner(t)
			if _, err := owner.enableEditing(pty.Slave, nil); err != nil {
				t.Fatal(err)
			}
			a := newApprover(ui.NewREPLNotifier(pty.Slave), owner)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			type result struct {
				decision core.Approval
				err      error
			}
			done := make(chan result, 1)
			go func() { decision, err := a.Approve(ctx, core.UniversalCommandArgs{}); done <- result{decision, err} }()
			if err := screen.Expect("      > ", time.Second); err != nil {
				t.Fatal(err)
			}
			// Even an affirmative next submission must not salvage this one.
			typeKeys(t, pty, tt.keys+"yes\n")
			r := <-done
			if r.err != nil || r.decision.Approved || r.decision.Reason != "" {
				t.Fatalf("oversized approval=%+v err=%v", r.decision, r.err)
			}
			if err := screen.Expect("Approval response is too long; denying without a reason.", time.Second); err != nil {
				t.Fatal(err)
			}
			next, err := owner.readLineUnder(ctx, 10)
			if err != nil || next.text != "yes" {
				t.Fatalf("next independent input=%+v err=%v", next, err)
			}
		})
	}
}

func TestTerminalContinuedInputPreservesLiteralText(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct{ name, keys, want string }{
		{"pasted comment", "\x1b[200~// comment\nbody\\\x1b[201~\n", "// comment\nbody\\"},
		{"pasted editor command", "\x1b[200~/ed\x1b[201~\n", "/ed"},
		{"typed escape", "//ed\n", "/ed"},
		{"recalled escape", "\x1b[A\n", "/exit"},
		{"paste after recall", "\x1b[A\x1b[200~ suffix\x1b[201~\n", "/exit suffix"},
		{"paste after typed slashes", "//comment \x1b[200~body\x1b[201~\n", "//comment body"},
		{"history restores paste", "\x1b[200~// comment\x1b[201~\x1b[A\x1b[B\n", "// comment"},
	} {
		for _, approval := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/approval=%v", tt.name, approval), func(t *testing.T) {
				owner, _, _ := terminalOwner(t)
				history := &replHistory{}
				history.add("/exit")
				if _, err := owner.enableEditing(io.Discard, history); err != nil {
					t.Fatal(err)
				}
				owner.term.(*terminalReader).held = []byte("prefix\\\n" + tt.keys)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				var text string
				if approval {
					initial, err := owner.readLineUnder(ctx, maxTurnBytes+2)
					if err != nil {
						t.Fatal(err)
					}
					a := newApprover(ui.NewREPLNotifier(io.Discard), owner)
					line, err := a.composeAnswer(ctx, initial)
					if err != nil || line.tooLong || line.eof {
						t.Fatalf("approval=%+v err=%v", line, err)
					}
					text = line.text
				} else {
					var how turnRead
					var err error
					text, how, err = readTurn(ctx, owner, func(string) {}, func(string, ...interface{}) {}, func() {}, false)
					if err != nil || how != readTurnText {
						t.Fatalf("turn=%q how=%v err=%v", text, how, err)
					}
				}
				if want := "prefix\n" + tt.want; text != want {
					t.Fatalf("submitted %q, want %q", text, want)
				}
			})
		}
	}
}

func TestTerminalEditorEscapeRestoresDraftWithoutLosingNextKey(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct{ name, keys, want string }{
		{"Enter", "\x1b\r\n\n", "draft"},
		{"text", "\x1bX\n\n", "draftX"},
		{"UTF-8", "\x1bé\n\n", "drafté"},
		{"second Escape", "\x1b\x1b\n\n", "draft"},
		{"incomplete CSI then Enter", "\x1b[\n\n", "draft"},
		{"incomplete SS3 then Enter", "\x1bO\n\n", "draft"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner, _, _ := terminalOwner(t)
			h := &replHistory{}
			h.add("old prompt")
			if _, err := owner.enableEditing(io.Discard, h); err != nil {
				t.Fatal(err)
			}
			owner.term.(*terminalReader).held = []byte("draft\x12" + tt.keys)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			line, err := owner.readLineUnder(ctx, 100)
			if err != nil || line.text != tt.want {
				t.Fatalf("Escape lost draft or next key: %+v err=%v; want %q", line, err, tt.want)
			}
		})
	}
}

func TestOversizedContinuedApprovalDoesNotRunCommand(t *testing.T) {
	t.Setenv("TERM", "xterm")
	defer func(limit int) { maxTurnBytes = limit }(maxTurnBytes)
	maxTurnBytes = 10
	owner, pty, screen := terminalOwner(t)
	if _, err := owner.enableEditing(pty.Slave, nil); err != nil {
		t.Fatal(err)
	}
	executor, err := core.NewExecutor(core.RequestOptions{}, nil, newApprover(ui.NewREPLNotifier(pty.Slave), owner))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	args, err := json.Marshal(core.UniversalCommandArgs{Command: []string{"touch", marker}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan []core.ToolResult, 1)
	go func() {
		done <- executor.ExecuteParallel(ctx, []core.ToolCall{{ID: "call", Name: "universal_command", Args: args}}, core.TestToolUI{})
	}()
	if err := screen.Expect("      > ", time.Second); err != nil {
		t.Fatal(err)
	}
	typeKeys(t, pty, strings.Repeat("x", 16)+"\\\nyes\n")
	results := <-done
	if len(results) != 1 || !results[0].NotRun || results[0].Error == "" {
		t.Fatalf("rejected submission ran the command: %+v", results)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command created its output: %v", err)
	}
}

func TestTerminalEditorRejectedInputEndsOnEOFOrInterrupt(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupt), func(t *testing.T) {
			owner, pty, screen := terminalOwner(t)
			if _, err := owner.enableEditing(pty.Slave, nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			done := make(chan lineResult, 1)
			go func() { line, err := owner.readLineUnder(ctx, 5); done <- lineResult{line, err} }()
			typeKeys(t, pty, "abcdef\\")
			if err := screen.Expect("input too long", time.Second); err != nil {
				t.Fatal(err)
			}
			if interrupt {
				cancel(errInterrupted)
			} else {
				typeKeys(t, pty, "\x04")
			}
			select {
			case r := <-done:
				if !r.line.tooLong || r.line.text != "" || !strings.HasSuffix(r.line.tail, "\\") || r.line.eof == interrupt {
					t.Fatalf("lost rejected input state: %+v", r)
				}
				if interrupt && !errors.Is(r.err, context.Canceled) || !interrupt && r.err != nil {
					t.Fatalf("interrupt=%v err=%v", interrupt, r.err)
				}
			case <-time.After(time.Second):
				t.Fatal("reader did not stop")
			}
		})
	}
}

func TestTerminalEscapeDecoderPreservesSequencesAndFragmentedKeys(t *testing.T) {
	t.Setenv("TERM", "xterm")
	owner, pty, _ := terminalOwner(t)
	if _, err := owner.enableEditing(io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	reader := owner.term.(*terminalReader)
	reader.buf = make([]byte, 1)
	keys := []string{"\x1b[A", "\x1b[D", "\x1bOH", "\x1bOF", "\x1b[3~", "\x1b[200~", "\x1b[201~", "\x1b", "é", "\r"}
	typeKeys(t, pty, strings.Join(keys, ""))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, want := range keys {
		got, err := reader.editKey(ctx, func() {})
		if err != nil || got != want {
			t.Fatalf("fragmented key=%q err=%v; want %q", got, err, want)
		}
	}
	// Cancellation during lookahead is an error, not a standalone Escape.
	reader.held = []byte("\x1b")
	lookahead, stop := context.WithCancel(ctx)
	defer stop()
	if got, err := reader.editKey(lookahead, stop); got != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lookahead=%q err=%v", got, err)
	}
	// A lone Escape and an unfinished sequence both cancel search after timeout.
	for _, prefix := range []string{"\x1b", "\x1b["} {
		reader.held = []byte(prefix)
		if got, err := reader.editKey(ctx, func() {}); got != "\x1b" || err != nil {
			t.Fatalf("timed-out prefix %q: key=%q err=%v", prefix, got, err)
		}
	}
}

func TestTerminalEscapeLookaheadIsFlushedBeforeApproval(t *testing.T) {
	t.Setenv("TERM", "xterm")
	owner, pty, screen := terminalOwner(t)
	if _, err := owner.enableEditing(pty.Slave, nil); err != nil {
		t.Fatal(err)
	}
	reader := owner.term.(*terminalReader)
	reader.held = []byte("\x1byes\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if key, err := reader.editKey(ctx, func() {}); key != "\x1b" || err != nil {
		t.Fatalf("key=%q err=%v", key, err)
	}
	done := make(chan lineResult, 1)
	go func() {
		line, err := owner.answer(ctx, 100, func(discarded bool) {
			if !discarded {
				t.Error("lookahead input was not reported as discarded")
			}
			_, _ = io.WriteString(pty.Slave, "fresh approval> ")
		})
		done <- lineResult{line, err}
	}()
	if err := screen.Expect("fresh approval> ", time.Second); err != nil {
		t.Fatal(err)
	}
	typeKeys(t, pty, "no\n")
	if r := <-done; r.err != nil || r.line.text != "no" {
		t.Fatalf("stale lookahead answered approval: %+v", r)
	}
}

func TestTerminalEditorMalformedUTF8PreservesKeys(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct {
		name, keys, want string
		eof              bool
	}{
		{"Enter", "before\xc3\n", "before�", false},
		{"CRLF", "before\xc3\r\n", "before�", false},
		{"Ctrl-D", "before\xc3\x04", "before�", true},
		{"left arrow", "ab\xc3\x1b[DX\n", "abX�", false},
		{"backspace", "before\xc3\x7f\n", "before", false},
		{"clear", "before\xc3\x15ok\n", "ok", false},
		{"longer prefix", "before\xe2\x82\n", "before��", false},
		{"following Unicode", "a\xc3é\n", "a�é", false},
		{"valid Unicode", "é界😀\n", "é界😀", false},
	} {
		for _, readSize := range []int{1, 4096} {
			t.Run(fmt.Sprintf("%s/read=%d", tt.name, readSize), func(t *testing.T) {
				owner, pty, _ := terminalOwner(t)
				if _, err := owner.enableEditing(io.Discard, nil); err != nil {
					t.Fatal(err)
				}
				owner.term.(*terminalReader).buf = make([]byte, readSize)
				typeKeys(t, pty, tt.keys+"next\n")
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				line, err := owner.readLineUnder(ctx, 100)
				if err != nil || line.text != tt.want || line.eof != tt.eof {
					t.Fatalf("line=%+v err=%v; want text=%q eof=%v", line, err, tt.want, tt.eof)
				}
				next, err := owner.readLineUnder(ctx, 100)
				if err != nil || next.text != "next" || next.eof {
					t.Fatalf("following submission=%+v err=%v", next, err)
				}
			})
		}
	}
}

func TestTerminalUTF8LookaheadEOFAndCancellation(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%v", cancelled), func(t *testing.T) {
			owner, _, _ := terminalOwner(t)
			if _, err := owner.enableEditing(io.Discard, nil); err != nil {
				t.Fatal(err)
			}
			reader := owner.term.(*terminalReader)
			reader.held = []byte("\xe2\x82")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if cancelled {
				key, err := reader.editKey(ctx, cancel)
				if key != "" || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled lookahead=%q err=%v", key, err)
				}
				return
			}
			// A closed pipe gives the reader deterministic EOF on every Unix;
			// a PTY hangup is not consistently reported by select on macOS.
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			fd := reader.fd
			reader.fd = int(r.Fd())
			defer func() { reader.fd = fd }()
			line, err := owner.readLineUnder(ctx, 100)
			if err != nil || !line.eof || line.text != "��" {
				t.Fatalf("incomplete UTF-8 at EOF=%+v err=%v", line, err)
			}
		})
	}
}

func TestTerminalPasteLimitCountsNormalizedBytes(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct {
		name, prefix, paste, want string
		tooLong, search           bool
	}{
		{name: "LF", paste: "1\n2\n3\n4\n5\n", want: "1\n2\n3\n4\n5\n"},
		{name: "CRLF", paste: "1\r\n2\r\n3\r\n4\r\n5\r\n", want: "1\n2\n3\n4\n5\n"},
		{name: "lone CR", paste: "a\rb\r", want: "a\nb\n"},
		{name: "mixed", paste: "\r\n\n\r\ra\r\nb\r", want: "\n\n\n\na\nb\n"},
		{name: "Unicode", paste: "é\r\n界\r\n123", want: "é\n界\n123"},
		{name: "existing draft", prefix: "ab", paste: "1\r\n2\r\n3\r\n4\r\n", want: "ab1\n2\n3\n4\n"},
		{name: "search", paste: "1\r\n2\r\n3\r\n4\r\n5\r\n", want: "1\n2\n3\n4\n5\n", search: true},
		{name: "oversized", paste: "1\r\n2\r\n3\r\n4\r\n5\r\nX", tooLong: true},
		{name: "oversized with draft", prefix: "abc", paste: "1\r\n2\r\n3\r\n4\r\n", tooLong: true},
	} {
		for _, readSize := range []int{1, 4096} {
			t.Run(fmt.Sprintf("%s/read=%d", tt.name, readSize), func(t *testing.T) {
				owner, pty, _ := terminalOwner(t)
				history := &replHistory{}
				history.add(tt.want)
				history.add("unrelated")
				if _, err := owner.enableEditing(io.Discard, history); err != nil {
					t.Fatal(err)
				}
				owner.term.(*terminalReader).buf = make([]byte, readSize)
				before, after := tt.prefix, "\n"
				if tt.search {
					before, after = "\x12", "\n\n"
				}
				typeKeys(t, pty, before+"\x1b[200~"+tt.paste+"\x1b[201~"+after+"next\n")
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				line, err := owner.readLineUnder(ctx, 10)
				if err != nil || line.text != tt.want || line.tooLong != tt.tooLong || line.eof {
					t.Fatalf("paste=%+v err=%v; want text=%q tooLong=%v", line, err, tt.want, tt.tooLong)
				}
				next, err := owner.readLineUnder(ctx, 10)
				if err != nil || next.text != "next" || next.eof {
					t.Fatalf("following submission=%+v err=%v", next, err)
				}
			})
		}
	}
}
