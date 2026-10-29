package main

import (
	"bytes"
	"context"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"strings"
	"testing"
)

// Outputs are numbered from 1 and never renumbered; past the limit the
// oldest go, the newest always stays, and a missing number says why.
func TestKeptOutputsNumberAndDropTheOldest(t *testing.T) {
	var k keptOutputs
	if _, missing := k.get(0); missing != "No tool output has been shortened yet." {
		t.Fatalf("empty store: %q", missing)
	}
	k.limit = 10
	for i, text := range []string{"aaaa", "bbbb", "cccc"} {
		if command := k.keep("call", text); command != "/show output "+string(rune('1'+i)) {
			t.Fatalf("command %d = %q", i, command)
		}
	}
	if got, _ := k.get(0); got.number != 3 || got.text != "cccc" {
		t.Fatalf("latest = %+v", got)
	}
	if got, _ := k.get(2); got.text != "bbbb" {
		t.Fatalf("output 2 = %+v", got)
	}
	k.keep("call", "dddd") // 16 bytes in all: output 1 goes
	if _, missing := k.get(1); !strings.Contains(missing, "Output 1 is no longer kept") {
		t.Fatalf("dropped output: %q", missing)
	}
	if _, missing := k.get(9); missing != "There is no output 9; the latest is 4." {
		t.Fatalf("unknown output: %q", missing)
	}
	k.keep("call", strings.Repeat("e", 50))
	if got, _ := k.get(5); got.number != 5 || len(k.outputs) != 1 {
		t.Fatalf("a large newest output was not kept alone: %+v, %d kept", got.number, len(k.outputs))
	}
}

// /show output at the prompt prints a kept output whole under its title.
func TestREPLShowOutputPrintsAKeptOutput(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, _, notes := newTestLoop(t, te)
	loop.kept.keep("[1/2] seq (stdout+stderr, 3 lines)", "one\ntwo\nthree\n")
	loop.interpretInput("/show output 1")
	want := "Output 1, from [1/2] seq (stdout+stderr, 3 lines):\n  | one\n  | two\n  | three\n"
	if got := loop.diag.(*bytes.Buffer).String(); got != want {
		t.Fatalf("shown %q, want %q", got, want)
	}
	loop.interpretInput("/show output x")
	if notes.count("Use /show output [N], with N from the note under an output.") != 1 {
		t.Fatalf("notes = %q", notes.all())
	}
}

// A question runs /show output and asks again, so the hint under a
// shortened output works while the model waits for the next approval.
func TestApproverShowsAKeptOutputAndAsksAgain(t *testing.T) {
	notifier := &mockNotifier{}
	a := newCLIApproverWithReader(notifier, strings.NewReader("/show output 2\ny \n"))
	var asked []string
	a.showOutput = func(_ context.Context, argument string) { asked = append(asked, argument) }
	ctx := core.WithApprovalTarget(context.Background(), core.ApprovalTarget{Index: 0, Total: 1})
	approval, err := a.Approve(ctx, core.UniversalCommandArgs{Command: []string{"rm", "-r", "build"}})
	if err != nil || !approval.Approved {
		t.Fatalf("approval = %+v, err = %v", approval, err)
	}
	if len(asked) != 1 || asked[0] != "2" {
		t.Fatalf("/show output ran with %q", asked)
	}
	said := strings.Join(notifier.messages, "")
	if strings.Count(said, "[1/1] Allow rm?") != 2 {
		t.Fatalf("question not asked again:\n%s", said)
	}
}
