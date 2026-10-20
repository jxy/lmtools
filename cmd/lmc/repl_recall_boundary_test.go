//go:build darwin || freebsd || linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/mockserver"
	"lmtools/internal/ui"
	"testing"
	"time"
)

func TestTerminalRecallCountsPayloadBytes(t *testing.T) {
	t.Setenv("TERM", "xterm")
	defer func(limit int) { maxTurnBytes = limit }(maxTurnBytes)
	maxTurnBytes = 10
	for _, tt := range []struct {
		name, prompt, prefix, edits, want string
		tooLong                           bool
	}{
		{name: "plain", prompt: "1234567890", want: "1234567890"},
		{name: "slash", prompt: "/123456789", want: "/123456789"},
		{name: "two slashes", prompt: "//12345678", want: "//12345678"},
		{name: "UTF-8 and newline", prompt: "/界é\nabc", want: "/界é\nabc"},
		{name: "literal command", prompt: "/exit     ", want: "/exit     "},
		{name: "continuation", prompt: "/1234567", prefix: "p\\\n", want: "p\n/1234567"},
		{name: "plain overflow", prompt: "1234567890", edits: "x", tooLong: true},
		{name: "slash overflow", prompt: "/123456789", edits: "x", tooLong: true},
		{name: "UTF-8 overflow", prompt: "/界é\nabc", edits: "é", tooLong: true},
		{name: "continuation overflow", prompt: "/12345678", prefix: "p\\\n", tooLong: true},
		{name: "inserted prefix", prompt: "/12345678", edits: "\x01X", tooLong: true},
	} {
		for _, recall := range []string{"\x1b[A", "\x12\n"} {
			for _, approval := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/recall=%q/approval=%v", tt.name, recall, approval), func(t *testing.T) {
					owner, _, _ := terminalOwner(t)
					history := &replHistory{}
					history.add(tt.prompt)
					if _, err := owner.enableEditing(io.Discard, history); err != nil {
						t.Fatal(err)
					}
					owner.term.(*terminalReader).held = []byte(tt.prefix + recall + tt.edits + "\n")
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					var text string
					var tooLong bool
					if approval {
						initial, err := owner.readLineUnder(ctx, maxTurnBytes+2)
						if err != nil {
							t.Fatal(err)
						}
						a := newApprover(ui.NewREPLNotifier(io.Discard), owner)
						line, err := a.composeAnswer(ctx, initial)
						if err != nil || line.eof {
							t.Fatalf("approval input=%+v err=%v", line, err)
						}
						text, tooLong = line.text, line.tooLong
					} else {
						turn, how, err := readTextTurn(ctx, owner.readLineUnder, true, func(string) {}, func() {}, false)
						if err != nil || (how != readTurnText && how != readTooLong) {
							t.Fatalf("turn=%q how=%v err=%v", turn, how, err)
						}
						tooLong = how == readTooLong
						if !tooLong {
							action := (&replLoop{}).interpretInput(turn)
							if !action.send || action.exit {
								t.Fatal("recalled literal became a command")
							}
							text = action.text
						}
					}
					if tooLong != tt.tooLong || text != tt.want {
						t.Fatalf("recalled text=%q tooLong=%v; want %q, %v", text, tooLong, tt.want, tt.tooLong)
					}
				})
			}
		}
	}
}

func TestREPLFullSizeDraftCanBeRecalledUnchanged(t *testing.T) {
	t.Setenv("TERM", "xterm")
	defer func(limit int) { maxTurnBytes = limit }(maxTurnBytes)
	maxTurnBytes = 10
	for _, recall := range []string{"\x1b[A\n", "\x12\n\n"} {
		t.Run(fmt.Sprintf("recall=%q", recall), func(t *testing.T) {
			server := mockserver.NewMockServer(numberedAnswers())
			defer server.Close()
			loop, _, notes := newTestLoop(t, newTurnTestEnv(t, server))
			owner, _, _ := terminalOwner(t)
			loop.input, loop.terminal = owner, true
			const prompt = "/exit     "
			loop.draft = prompt // A successful /ed may return exactly the limit.
			if _, err := owner.enableEditing(io.Discard, &loop.history); err != nil {
				t.Fatal(err)
			}
			owner.term.(*terminalReader).held = []byte("\n" + recall + "/exit\n")
			timer := time.AfterFunc(3*time.Second, func() { loop.signals.endProcess(context.DeadlineExceeded) })
			defer timer.Stop()
			if err := loop.run(false); err != nil {
				t.Fatal(err)
			}
			requests := server.GetRequests()
			if len(requests) != 2 {
				t.Fatalf("requests=%d; want original and recalled draft; diagnostics=%q", len(requests), notes.all())
			}
			for _, request := range requests {
				var body struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
					t.Fatal(err)
				}
				if len(body.Messages) == 0 {
					t.Fatal("request has no messages")
				}
				last := body.Messages[len(body.Messages)-1]
				if last.Role != "user" || last.Content != prompt {
					t.Fatalf("recall changed the submitted prompt: %+v", last)
				}
			}
		})
	}
}
