package main

import (
	"context"
	"lmtools/internal/core"
	"strings"
	"testing"
)

// Any other command at a question gets a note and the question again, and
// the call stays undecided. A reason that starts with / is given with //.
func TestApproverNeverTakesACommandAsAnAnswer(t *testing.T) {
	for _, tt := range []struct {
		name, input, reason, note string
		approved                  bool
	}{
		{name: "unknown command", input: "/exit\ny\n", approved: true, note: "  At a question, /help and /ed work.\n  To give a reason that starts with /, begin it with //.\n"},
		{name: "help", input: "/help\nn\n", note: "n, no, or Enter denies it; other text denies it and is your reason."},
		{name: "show without output", input: "/show last\nn\n", note: "At a question, /help and /ed work"},
		{name: "escaped reason", input: "//etc is the wrong place\n", reason: "/etc is the wrong place"},
		{name: "command then end of input", input: "/show output 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &mockNotifier{}
			a := newCLIApproverWithReader(notifier, strings.NewReader(tt.input))
			approval, err := a.Approve(context.Background(), core.UniversalCommandArgs{Command: []string{"ls"}})
			if err != nil || approval.Approved != tt.approved || approval.Reason != tt.reason {
				t.Fatalf("approval = %+v, err = %v", approval, err)
			}
			if said := strings.Join(notifier.messages, ""); !strings.Contains(said, tt.note) {
				t.Fatalf("missing %q in:\n%s", tt.note, said)
			}
		})
	}
}

// The round limit question runs commands too, and asks again.
func TestRoundLimitQuestionRunsCommands(t *testing.T) {
	notifier := &mockNotifier{}
	a := newCLIApproverWithReader(notifier, strings.NewReader("/help\n/exit\ny\n"))
	reset, err := a.ApproveToolRoundLimitReset(context.Background(), 8)
	if err != nil || !reset {
		t.Fatalf("reset = %v, err = %v", reset, err)
	}
	said := strings.Join(notifier.messages, "")
	if strings.Count(said, "Reset it and continue?") != 3 || !strings.Contains(said, "y or yes continues") || !strings.Contains(said, "At this question, /help works.") {
		t.Fatalf("transcript:\n%s", said)
	}
}

// The guide and the note at a question fit a terminal 80 columns wide as
// they are.
func TestQuestionHelpFitsEightyColumns(t *testing.T) {
	a := newCLIApproverWithReader(&mockNotifier{}, strings.NewReader(""))
	for _, compose := range []bool{true, false} {
		for _, line := range strings.Split(a.questionGuide(compose)+a.questionCommandsNote(compose), "\n") {
			if len(line) > 79 {
				t.Errorf("%d bytes: %q", len(line), line)
			}
		}
	}
}
