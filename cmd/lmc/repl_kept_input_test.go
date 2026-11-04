//go:build darwin || freebsd || linux

package main

import (
	"context"
	"fmt"
	"io"
	"lmtools/internal/ui"
	"strings"
	"testing"
	"time"
)

// keptInputTurn feeds keys to the editor and reads one turn, at the prompt
// or as a composed approval answer, with history holding "earlier".
func keptInputTurn(t *testing.T, keys string, approval bool) (string, turnRead) {
	t.Helper()
	owner, _, _ := terminalOwner(t)
	history := &replHistory{}
	history.add("earlier")
	if _, err := owner.enableEditing(io.Discard, history); err != nil {
		t.Fatal(err)
	}
	owner.term.(*terminalReader).held = []byte(keys)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if approval {
		initial, err := owner.readLineUnder(ctx, maxTurnBytes+2)
		if err != nil {
			t.Fatal(err)
		}
		a := newApprover(ui.NewREPLNotifier(io.Discard), owner)
		line, err := a.composeAnswer(ctx, initial, func() {}, question{compose: true, enterApproves: true})
		if err != nil || line.tooLong || line.eof {
			t.Fatalf("approval=%+v err=%v", line, err)
		}
		return line.text, readTurnText
	}
	text, how, err := readTurn(ctx, owner, func(string) {}, func(string, ...interface{}) {}, func() {}, false)
	if err != nil {
		t.Fatal(err)
	}
	return text, how
}

// Text pasted or recalled is sent as it came, and the keys typed around it
// work as usual: a typed backslash at the end continues the turn, a pasted
// one stays in the text, and a typed one after a pasted one continues the
// turn and keeps the pasted one.
func TestTerminalTypedKeysWorkAfterPasteOrRecall(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct{ name, keys, want string }{
		{"paste, typed backslash", "\x1b[200~foo\x1b[201~\\\nbar\n", "foo\nbar"},
		{"multiline paste, typed backslash", "\x1b[200~one\ntwo\x1b[201~\\\nbar\n", "one\ntwo\nbar"},
		{"recall, typed backslash", "\x1b[A\\\nbar\n", "earlier\nbar"},
		{"search, typed backslash", "\x12earl\n\\\nbar\n", "earlier\nbar"},
		{"pasted backslash", "\x1b[200~foo\\\x1b[201~\n", "foo\\"},
		{"pasted doubled backslash", "\x1b[200~a & b \\\\\x1b[201~\n", "a & b \\\\"},
		{"typed backslash after a pasted one", "\x1b[200~foo\\\x1b[201~\\\nbar\n", "foo\\\nbar"},
		{"typed doubled backslash after a paste", "\x1b[200~foo\x1b[201~\\\\\n", "foo\\"},
		{"words typed after a pasted backslash", "\x1b[200~foo\\\x1b[201~ bar\\\nbaz\n", "foo\\ bar\nbaz"},
		{"paste inside a typed continuation", "foo\\\x01\x1b[200~x\x1b[201~\nbar\n", "xfoo\nbar"},
		{"paste on a continuation line", "Review:\\\n\x1b[200~one\ntwo\x1b[201~\\\nWhy?\n", "Review:\none\ntwo\nWhy?"},
		{"cleared paste", "\x1b[200~foo\x1b[201~\x15bar\\\nbaz\n", "bar\nbaz"},
	} {
		for _, approval := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/approval=%v", tt.name, approval), func(t *testing.T) {
				if text, how := keptInputTurn(t, tt.keys, approval); text != tt.want || how != readTurnText {
					t.Fatalf("submitted %q (%v), want %q", text, how, tt.want)
				}
			})
		}
	}
}

// A paste at the prompt is never a command or an escape, its first line
// continued or not. A slash typed in front of pasted text still makes a
// command, and deleting the escape a recall shows makes one, as it would in
// typed text.
func TestTerminalPasteAtThePromptIsNeverACommand(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct {
		name, keys, want string
		command          bool
	}{
		{name: "pasted path", keys: "\x1b[200~/home/me/x.c:12: error: oops\x1b[201~\n", want: "/home/me/x.c:12: error: oops"},
		{name: "pasted comment", keys: "\x1b[200~// Package x\npackage x\x1b[201~\n", want: "// Package x\npackage x"},
		{name: "pasted command", keys: "\x1b[200~/exit\x1b[201~\n", want: "/exit"},
		{name: "continued paste", keys: "\x1b[200~/etc/hosts\x1b[201~\\\nWhat is this?\n", want: "/etc/hosts\nWhat is this?"},
		{name: "typed escape before a paste", keys: "//\x1b[200~exit\x1b[201~\n", want: "/exit"},
		{name: "typed slash before a paste", keys: "/\x1b[200~exit\x1b[201~\n", command: true},
		{name: "deleted recall escape", keys: "\x1b[A\x01\x1b[3~\n", command: true},
	} {
		for _, approval := range []bool{false, true} {
			if approval && tt.command {
				continue // A command at a question runs and asks again.
			}
			t.Run(fmt.Sprintf("%s/approval=%v", tt.name, approval), func(t *testing.T) {
				owner, _, _ := terminalOwner(t)
				history := &replHistory{}
				history.add("/exit")
				if _, err := owner.enableEditing(io.Discard, history); err != nil {
					t.Fatal(err)
				}
				owner.term.(*terminalReader).held = []byte(tt.keys)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if approval {
					initial, err := owner.readLineUnder(ctx, maxTurnBytes+2)
					if err != nil {
						t.Fatal(err)
					}
					a := newApprover(ui.NewREPLNotifier(io.Discard), owner)
					line, err := a.composeAnswer(ctx, initial, func() {}, question{compose: true, enterApproves: true})
					if err != nil || line.text != tt.want {
						t.Fatalf("approval=%+v err=%v, want %q", line, err, tt.want)
					}
					return
				}
				turn, how, err := readTurn(ctx, owner, func(string) {}, func(string, ...interface{}) {}, func() {}, false)
				if err != nil || how != readTurnText {
					t.Fatalf("turn=%q how=%v err=%v", turn, how, err)
				}
				if isREPLCommand(turn) != tt.command {
					t.Fatalf("%q: command=%v, want %v", turn, isREPLCommand(turn), tt.command)
				}
				action := (&replLoop{}).interpretInput(turn)
				if tt.command {
					if action.send || !action.exit {
						t.Fatalf("%q did not run as a command: %+v", turn, action)
					}
					return
				}
				if !action.send || action.exit || action.text != tt.want {
					t.Fatalf("%q became %+v, want text %q", turn, action, tt.want)
				}
			})
		}
	}
}

// A recalled command runs again as it ran: its own trailing backslash does
// not continue the turn, and a backslash typed after it does.
func TestTerminalRecalledCommandRunsAsItRan(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct{ name, keys, want string }{
		{"recalled", "\x1b[A\n", "/developer Use C:\\"},
		{"typed backslash after it", "\x1b[A\\\nthen this\n", "/developer Use C:\\\nthen this"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner, _, _ := terminalOwner(t)
			history := &replHistory{}
			history.addCommand("/developer Use C:\\")
			if _, err := owner.enableEditing(io.Discard, history); err != nil {
				t.Fatal(err)
			}
			owner.term.(*terminalReader).held = []byte(tt.keys)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, how, err := readTurn(ctx, owner, func(string) {}, func(string, ...interface{}) {}, func() {}, false)
			if err != nil || how != readTurnText || turn != tt.want || !isREPLCommand(turn) {
				t.Fatalf("turn=%q how=%v err=%v, want the command %q", turn, how, err, tt.want)
			}
		})
	}
}

// /ed during a continuation hands over the unfinished text without the
// escape the reader put in front of a kept first line.
func TestTerminalEditAfterKeptFirstLineDropsReaderEscape(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct{ name, keys, want string }{
		{"recalled", "\x1b[A\\\n/ed\n", "/exit"},
		{"pasted", "\x1b[200~/etc/hosts\x1b[201~\\\n/ed\n", "/etc/hosts"},
		{"typed escape", "//etc\\\n/ed\n", "//etc"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owner, _, _ := terminalOwner(t)
			history := &replHistory{}
			history.add("/exit")
			if _, err := owner.enableEditing(io.Discard, history); err != nil {
				t.Fatal(err)
			}
			owner.term.(*terminalReader).held = []byte(tt.keys)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			text, how, err := readTextTurn(ctx, owner.readLineUnder, true, func(string) {}, func() {}, false)
			if err != nil || how != readEdit || text != tt.want {
				t.Fatalf("draft=%q how=%v err=%v, want %q to edit", text, how, err, tt.want)
			}
		})
	}
}

// A question answered on one line takes a pasted answer as an answer, even
// one that starts with /; a typed command still runs and asks again.
func TestTerminalPastedAnswerIsNeverACommand(t *testing.T) {
	t.Setenv("TERM", "xterm")
	for _, tt := range []struct {
		name, keys string
		asked      int
		reset      bool
	}{
		{"pasted", "\x1b[200~/help\x1b[201~\n", 1, false},
		{"typed", "/help\n", 2, false},
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
				reset bool
				err   error
			}
			done := make(chan result, 1)
			go func() { reset, err := a.ApproveToolRoundLimitReset(ctx, 8); done <- result{reset, err} }()
			if err := screen.Expect("[y/N]:", time.Second); err != nil {
				t.Fatal(err)
			}
			if err := screen.Expect("  > ", time.Second); err != nil {
				t.Fatal(err)
			}
			typeKeys(t, pty, tt.keys)
			if tt.asked > 1 {
				if err := screen.Expect("y or yes continues", time.Second); err != nil {
					t.Fatal(err)
				}
				if err := screen.Expect("  > ", time.Second); err != nil {
					t.Fatal(err)
				}
				typeKeys(t, pty, "n\n")
			}
			r := <-done
			if r.err != nil || r.reset != tt.reset {
				t.Fatalf("reset=%v err=%v", r.reset, r.err)
			}
			if got := strings.Count(screen.String(), "Reset it and continue?"); got != tt.asked {
				t.Fatalf("asked %d times, want %d:\n%s", got, tt.asked, screen.String())
			}
		})
	}
}
