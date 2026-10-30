package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/core"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

// mockNotifier records approval prompts for assertions.
type mockNotifier struct {
	messages []string
}

func (m *mockNotifier) Infof(format string, args ...interface{}) {
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}

func (m *mockNotifier) Warnf(format string, args ...interface{}) {
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}

func (m *mockNotifier) Errorf(format string, args ...interface{}) {
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}

func (m *mockNotifier) Promptf(format string, args ...interface{}) {
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}

func TestApprover_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "approve with y",
			input:    "y\n",
			expected: true,
		},
		{
			name:     "approve with yes",
			input:    "yes\n",
			expected: true,
		},
		{
			name:     "approve with y and spaces",
			input:    "  y  \n",
			expected: true,
		},
		{
			name:     "approve with yes and spaces",
			input:    "  yes  \n",
			expected: true,
		},
		{
			name:     "deny with n",
			input:    "n\n",
			expected: false,
		},
		{
			name:     "deny with no",
			input:    "no\n",
			expected: false,
		},
		{
			name:     "approve with Enter",
			input:    "\n",
			expected: true,
		},
		{
			name:     "deny with random input",
			input:    "maybe\n",
			expected: false,
		},
		{
			name:     "approve with uppercase Y",
			input:    "Y\n",
			expected: true,
		},
		{
			name:     "approve with uppercase YES",
			input:    "YES\n",
			expected: true,
		},
		{
			name:     "EOF results in deny",
			input:    "", // EOF
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &mockNotifier{}
			approver := newCLIApproverWithReader(notifier, strings.NewReader(tt.input))

			result, err := approver.Approve(context.Background(), core.UniversalCommandArgs{Command: []string{"ls"}})
			if err != nil {
				t.Fatalf("Approve() error = %v", err)
			}

			if result.Approved != tt.expected {
				t.Errorf("Approve() = %v, expected %v", result, tt.expected)
			}
			if len(notifier.messages) == 0 || notifier.messages[0] != "  Allow ls? [Y/n or reason; /ed to edit]: " {
				t.Fatalf("approval prompt = %#v", notifier.messages)
			}
		})
	}
}

// approverEntryPoint adapts each approval conversation to a common test
// result, including the round-limit question that only returns a boolean.
type approverEntryPoint struct {
	name      string
	invoke    func(*cliApprover, context.Context) (core.Approval, error)
	prompt    string
	eofNotice string
	// enterApproves says an empty answer approves.
	enterApproves bool
}

func approverEntryPoints() []approverEntryPoint {
	return []approverEntryPoint{
		{
			name: "Approve",
			invoke: func(a *cliApprover, ctx context.Context) (core.Approval, error) {
				return a.Approve(ctx, core.UniversalCommandArgs{Command: []string{"ls"}})
			},
			prompt:        "  Allow ls? [Y/n or reason; /ed to edit]: ",
			eofNotice:     "\n  No interactive input available; denying by default.\n",
			enterApproves: true,
		},
		{
			name: "ApproveImage",
			invoke: func(a *cliApprover, ctx context.Context) (core.Approval, error) {
				// The prompt describes the bytes that were read, so it names
				// the sniffed type and decoded size rather than the path alone.
				return a.ApproveImage(ctx, core.ViewImageArgs{Path: "plots/shot.png"},
					core.ImageBlock{URL: "data:image/png;base64,iVBORw0KGgo=", Name: "shot.png"})
			},
			prompt:        "  Send shot.png (image/png, 8 bytes) to the model? [Y/n or reason; /ed to edit]: ",
			eofNotice:     "\n  No interactive input available; denying by default.\n",
			enterApproves: true,
		},
		{
			name: "ApproveMCP",
			invoke: func(a *cliApprover, ctx context.Context) (core.Approval, error) {
				// The review line above showed the arguments; the question
				// names the server and tool the way a rule would.
				return a.ApproveMCP(ctx, core.MCPCall{Server: "github", Tool: "list_issues", Arguments: json.RawMessage(`{"repo":"a/b"}`)})
			},
			prompt:        "  Call github/list_issues? [Y/n or reason; /ed to edit]: ",
			eofNotice:     "\n  No interactive input available; denying by default.\n",
			enterApproves: true,
		},
		{
			name: "ApproveRerun",
			invoke: func(a *cliApprover, ctx context.Context) (core.Approval, error) {
				// The review line above named the call and why its outcome
				// is unknown; the question names the tool and the risk.
				return a.ApproveRerun(ctx, core.ToolCall{ID: "call-1", Name: "universal_command"})
			},
			prompt:    "  Run universal_command again? It may already have run. [y/N or reason; /ed to edit]: ",
			eofNotice: "\n  No interactive input available; not running it again.\n",
		},
		{
			name: "ApproveToolRoundLimitReset",
			invoke: func(a *cliApprover, ctx context.Context) (core.Approval, error) {
				approved, err := a.ApproveToolRoundLimitReset(ctx, 4)
				return core.Approval{Approved: approved}, err
			},
			prompt:    "\nTool-call round limit reached (4). Reset it and continue? [y/N]: ",
			eofNotice: "\nNo interactive input available; keeping the tool-call round limit.\n",
		},
	}
}

// A terminal operator who types `y` and then Ctrl-D has answered the question.
// bufio.Reader.ReadString hands back that unterminated line together with
// io.EOF, so an approver that reads the error and not the line converts the
// approval into a denial. Only an EOF with nothing before it is the absence of
// an answer.
func TestApproverAnswerTerminatedByEOFIsTheAnswer(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		expected   bool
		wantNotice bool
	}{
		{name: "y then Ctrl-D approves", input: "y", expected: true},
		{name: "yes then Ctrl-D approves", input: "  YES  ", expected: true},
		{name: "n then Ctrl-D denies without the no-input notice", input: "n"},
		{name: "Ctrl-D alone keeps the fail-safe denial and its notice", input: "", wantNotice: true},
		// Spaces are no answer: only a submitted empty answer is Enter.
		{name: "spaces then Ctrl-D keeps the fail-safe denial", input: "   ", wantNotice: true},
		{name: "a tab then Ctrl-D keeps the fail-safe denial", input: "\t", wantNotice: true},
	}

	for _, entry := range approverEntryPoints() {
		for _, tt := range tests {
			t.Run(entry.name+"/"+tt.name, func(t *testing.T) {
				notifier := &mockNotifier{}
				approver := newCLIApproverWithReader(notifier, strings.NewReader(tt.input))

				approved, err := entry.invoke(approver, context.Background())
				if err != nil {
					t.Fatalf("%s() error = %v", entry.name, err)
				}
				if approved.Approved != tt.expected {
					t.Fatalf("%s(%q) = %v, want %v", entry.name, tt.input, approved, tt.expected)
				}
				// An answered prompt has its echo closed with a newline; only
				// an unanswered one gets the no-input notice.
				wantMessages := []string{entry.prompt, "\n"}
				if tt.wantNotice {
					wantMessages[1] = entry.eofNotice
				}
				if !reflect.DeepEqual(notifier.messages, wantMessages) {
					t.Fatalf("prompt messages = %#v, want %#v", notifier.messages, wantMessages)
				}
			})
		}
	}
}

func TestApproverReturnsDenialReason(t *testing.T) {
	for _, entry := range approverEntryPoints() {
		for _, test := range []struct {
			input  string
			reason string
		}{
			{"Only inspect files; do not modify them.\n", "Only inspect files; do not modify them."},
			{"  Use the cached result — no network.  \n", "Use the cached result — no network."},
			{"yes, but use a different command\n", "yes, but use a different command"},
			{"no, this is a read-only review\n", "no, this is a read-only review"},
			{"Do not send this image", "Do not send this image"},
			{"N\n", ""},
			{" NO \n", ""},
			{" \n", ""},
		} {
			t.Run(entry.name+"/"+test.input, func(t *testing.T) {
				approver := newCLIApproverWithReader(&mockNotifier{}, strings.NewReader(test.input))
				decision, err := entry.invoke(approver, context.Background())
				wantReason := test.reason
				if entry.name == "ApproveToolRoundLimitReset" {
					wantReason = "" // This confirmation does not produce a tool result.
				}
				if entry.enterApproves && strings.TrimSpace(test.input) == "" {
					// A blank answer is Enter, which approves here.
					if err != nil || !decision.Approved {
						t.Fatalf("decision=%+v error=%v, want approval", decision, err)
					}
					return
				}
				if err != nil || decision.Approved || decision.Reason != wantReason {
					t.Fatalf("decision=%+v error=%v, want denial reason %q", decision, err, wantReason)
				}
			})
		}
	}
}

func TestApproverUsesNormalInputLimit(t *testing.T) {
	defer func(size int) { maxTurnBytes = size }(maxTurnBytes)
	maxTurnBytes = 128
	reason := strings.Repeat("x", maxTurnBytes)
	for _, test := range []struct {
		input, reason string
		approved      bool
	}{
		{reason + "\n", reason, false},
		{reason + "x\nn\n", "", false},
		{"yes" + strings.Repeat(" ", 80) + "\n", "", true},
	} {
		approver := newCLIApproverWithReader(&mockNotifier{}, strings.NewReader(test.input))
		decision, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
		if err != nil || decision.Approved != test.approved || decision.Reason != test.reason {
			t.Fatalf("decision=%+v error=%v", decision, err)
		}
	}
}

func TestApproverContinuedInputUsesFinalText(t *testing.T) {
	for _, test := range []struct {
		input, reason string
		approved      bool
	}{
		{"Only inspect files\\\nDo not modify anything.\n", "Only inspect files\nDo not modify anything.", false},
		{"yes\\\nplease\n", "yes\nplease", false},
		{"yes\\\n\n", "", true},
		{"no\\\nKeep the review read-only.\n", "no\nKeep the review read-only.", false},
		{"Keep a literal slash\\\\\n", "Keep a literal slash\\", false},
		{"//ed\n", "/ed", false},
		{"First line\\\n//ed\n", "First line\n/ed", false},
		{"Unfinished\\\n", "", false},
		{"Unfinished\\", "", false},
	} {
		t.Run(test.input, func(t *testing.T) {
			approver := newCLIApproverWithReader(&mockNotifier{}, strings.NewReader(test.input))
			decision, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
			if err != nil || decision.Approved != test.approved || decision.Reason != test.reason {
				t.Fatalf("decision=%+v error=%v; want approved=%v reason=%q", decision, err, test.approved, test.reason)
			}
		})
	}
}

func TestApproverOversizedSubmissionDoesNotConsumeNextAnswer(t *testing.T) {
	defer func(limit int) { maxTurnBytes = limit }(maxTurnBytes)
	maxTurnBytes = 10
	for _, input := range []string{
		"0123456789abcdef\n",
		"0123456789abcdef\\\nyes\n",
		"0123456789abcdef\\\\\n",
		"123456\\\n123456\\\nyes\n",
	} {
		t.Run(input, func(t *testing.T) {
			a := newCLIApproverWithReader(&mockNotifier{}, strings.NewReader(input+"yes\n"))
			decision, err := a.Approve(context.Background(), core.UniversalCommandArgs{})
			if err != nil || decision.Approved || decision.Reason != "" {
				t.Fatalf("oversized submission=%+v err=%v", decision, err)
			}
			next, err := a.Approve(context.Background(), core.UniversalCommandArgs{})
			if err != nil || !next.Approved {
				t.Fatalf("next independent answer=%+v err=%v", next, err)
			}
		})
	}
}

func TestApproverLiteralInputIsIndependentOfLineEnding(t *testing.T) {
	for _, entry := range approverEntryPoints() {
		if entry.name == "ApproveToolRoundLimitReset" {
			continue
		}
		for _, test := range []struct{ input, reason string }{
			{`//ed`, `/ed`},
			{`Use a literal backslash\\`, `Use a literal backslash\`},
			{`//ed\\`, `/ed\`},
		} {
			for _, ending := range []string{"", "\n", "\r\n"} {
				approver := newCLIApproverWithReader(&mockNotifier{}, strings.NewReader(test.input+ending))
				decision, err := entry.invoke(approver, context.Background())
				if err != nil || decision.Approved || decision.Reason != test.reason {
					t.Fatalf("%s(%q): decision=%+v error=%v, want reason %q", entry.name, test.input+ending, decision, err, test.reason)
				}
			}
		}
	}
}

var errTestReadFailure = errors.New("stdin exploded")

// Only an end of input is an answer. A stream that failed for any other reason
// did not report what the operator typed, so it is an error rather than a
// silent denial.
func TestApproverReportsReadFailure(t *testing.T) {
	notifier := &mockNotifier{}
	approver := newCLIApproverWithReader(notifier, iotest.ErrReader(errTestReadFailure))

	approved, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
	if approved.Approved {
		t.Fatal("Approve() approved despite a failed read")
	}
	if !errors.Is(err, errTestReadFailure) {
		t.Fatalf("Approve() error = %v, want it to wrap %v", err, errTestReadFailure)
	}
}

// The unterminated answer is the last one the shared reader can serve, so it
// has to survive the prompts that came before it.
func TestApproverReadsUnterminatedAnswerAfterBufferedPrompts(t *testing.T) {
	notifier := &mockNotifier{}
	approver := newCLIApproverWithReader(notifier, strings.NewReader("n\ny"))

	denied, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
	if err != nil {
		t.Fatalf("Approve() first error = %v", err)
	}
	if denied.Approved {
		t.Fatal("Approve() first call approved despite an explicit n")
	}

	approved, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
	if err != nil {
		t.Fatalf("Approve() second error = %v", err)
	}
	if !approved.Approved {
		t.Fatal("Approve() second call denied a y typed without a trailing newline")
	}
}

func TestApproverReusesBufferedReaderAcrossPrompts(t *testing.T) {
	for _, entry := range approverEntryPoints() {
		t.Run(entry.name, func(t *testing.T) {
			notifier := &mockNotifier{}
			approver := newCLIApproverWithReader(notifier, strings.NewReader("y\nyes\n"))

			for i := 0; i < 2; i++ {
				approved, err := entry.invoke(approver, context.Background())
				if err != nil {
					t.Fatalf("%s() call %d error = %v", entry.name, i+1, err)
				}
				if !approved.Approved {
					t.Fatalf("%s() call %d denied buffered approval", entry.name, i+1)
				}
			}
			if len(notifier.messages) != 2 {
				t.Fatalf("approval messages = %#v, want two prompts", notifier.messages)
			}
		})
	}
}

func TestApproverToolRoundLimitReset(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "approve", input: "yes\n", expected: true},
		{name: "deny", input: "n\n", expected: false},
		{name: "EOF", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &mockNotifier{}
			approver := newCLIApproverWithReader(notifier, strings.NewReader(tt.input))
			approved, err := approver.ApproveToolRoundLimitReset(context.Background(), 4)
			if err != nil {
				t.Fatalf("ApproveToolRoundLimitReset() error = %v", err)
			}
			if approved != tt.expected {
				t.Fatalf("ApproveToolRoundLimitReset() = %v, want %v", approved, tt.expected)
			}
			wantPrompt := "\nTool-call round limit reached (4). Reset it and continue? [y/N]: "
			if len(notifier.messages) == 0 || notifier.messages[0] != wantPrompt {
				t.Fatalf("round-limit prompt = %#v, want %q", notifier.messages, wantPrompt)
			}
		})
	}
}

func TestDevNullIsNotTerminal(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if isTerminalFile(file) {
		t.Fatal("os.DevNull was treated as a terminal")
	}
}

func TestApproverToolRoundLimitResetCancelled(t *testing.T) {
	notifier := &mockNotifier{}
	approver := newCLIApproverWithReader(notifier, strings.NewReader("yes\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	approved, err := approver.ApproveToolRoundLimitReset(ctx, 4)
	if approved || !errors.Is(err, context.Canceled) {
		t.Fatalf("approved = %v, error = %v; want context.Canceled", approved, err)
	}
	if len(notifier.messages) != 0 {
		t.Fatalf("cancelled prompt messages = %#v, want none", notifier.messages)
	}
}

// Under `go test` stdin is not a terminal, so this pins the half of the gate
// that has no substitute: without somewhere to read the answer there is no
// approver, and the executor denies rather than prompting into the void.
func TestNewCLIApproverRequiresTerminalStdin(t *testing.T) {
	if isTerminalFile(os.Stdin) {
		t.Skip("test stdin is a terminal; this case needs a non-terminal stdin")
	}
	base := &mockNotifier{}
	reviewNotifier, approver := newOperatorToolSurface(base, newInputOwner(os.Stdin))
	if approver != nil {
		t.Fatalf("newOperatorToolSurface() approver = %#v, want nil without a terminal to read from", approver)
	}
	if reviewNotifier != core.Notifier(base) {
		t.Fatalf("newOperatorToolSurface() notifier = %#v, want the ordinary notifier when approval is off", reviewNotifier)
	}
}

// The other half does have a substitute. `lmc -tool 2>lmc.log` is a documented
// invocation, and redirecting a log must not silently disable approval, so a
// non-terminal stderr falls back to the controlling terminal rather than
// refusing outright.
func TestApprovalPromptFallsBackToControllingTerminal(t *testing.T) {
	if isTerminalFile(os.Stderr) {
		t.Skip("test stderr is a terminal; this case needs a redirected stderr")
	}

	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		// No controlling terminal, which is the CI case: refusing is correct.
		if notifier := approvalPromptNotifier(&mockNotifier{}); notifier != nil {
			t.Fatalf("approvalPromptNotifier() = %#v, want nil with no tty", notifier)
		}
		return
	}
	_ = tty.Close()

	if approvalPromptNotifier(&mockNotifier{}) == nil {
		t.Fatal("approvalPromptNotifier() refused despite an available controlling terminal")
	}
}

// A question names the program the model chose, with any control in it
// spelled out.
func TestApproverQuestionSpellsOutControlsInTheName(t *testing.T) {
	notifier := &mockNotifier{}
	approver := newCLIApproverWithReader(notifier, strings.NewReader("n\n"))
	if _, err := approver.Approve(context.Background(), core.UniversalCommandArgs{Command: []string{"\x1b[2Jrm"}}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.messages) == 0 || notifier.messages[0] != `  Allow "\u001b[2Jrm"? [Y/n or reason; /ed to edit]: ` {
		t.Fatalf("question = %#v", notifier.messages)
	}
}

func TestParseApprovalAnswer(t *testing.T) {
	for _, enterApproves := range []bool{false, true} {
		empty := core.Approval{}
		if enterApproves {
			empty = core.Approval{Approved: true, Rest: true}
		}
		for answer, want := range map[string]core.Approval{
			"y":      {Approved: true},
			" YES ":  {Approved: true},
			"a":      {Approved: true, Rest: true},
			"All":    {Approved: true, Rest: true},
			"":       empty,
			"  ":     empty,
			"n":      {},
			"No":     {},
			"use rg": {Reason: "use rg"},
		} {
			if got := parseApprovalAnswer(answer, enterApproves); got != want {
				t.Errorf("parseApprovalAnswer(%q, %v) = %+v, want %+v", answer, enterApproves, got, want)
			}
		}
	}
}

// Enter answers a command question with yes, and with a when later calls in
// the round would ask, and says so, since the answer line stays empty. A
// rerun keeps no as its default, and input that ends without an answer
// denies every question.
func TestApproverEnterTakesTheDefault(t *testing.T) {
	ls := core.UniversalCommandArgs{Command: []string{"ls"}}
	inRound := func(index, total, remaining int) context.Context {
		return core.WithApprovalTarget(context.Background(), core.ApprovalTarget{Index: index, Total: total, Remaining: remaining})
	}
	for _, tt := range []struct {
		name   string
		ctx    context.Context
		ask    func(*cliApprover, context.Context) (core.Approval, error)
		input  string
		prompt string
		want   core.Approval
		said   string
	}{
		{
			name:   "first of three",
			ctx:    inRound(0, 3, 2),
			ask:    func(a *cliApprover, ctx context.Context) (core.Approval, error) { return a.Approve(ctx, ls) },
			input:  "\n",
			prompt: "[1/3] Allow ls? [y/n/A = this and the rest, or reason; /ed to edit]: ",
			want:   core.Approval{Approved: true, Rest: true},
			said:   "  Approved this call and the remaining 2 calls.\n",
		},
		{
			name:   "last of three",
			ctx:    inRound(2, 3, 0),
			ask:    func(a *cliApprover, ctx context.Context) (core.Approval, error) { return a.Approve(ctx, ls) },
			input:  "\n",
			prompt: "[3/3] Allow ls? [Y/n or reason; /ed to edit]: ",
			want:   core.Approval{Approved: true, Rest: true},
			said:   "  Approved.\n",
		},
		{
			name:   "y needs no notice",
			ctx:    inRound(0, 3, 2),
			ask:    func(a *cliApprover, ctx context.Context) (core.Approval, error) { return a.Approve(ctx, ls) },
			input:  "y\n",
			prompt: "[1/3] Allow ls? [y/n/A = this and the rest, or reason; /ed to edit]: ",
			want:   core.Approval{Approved: true},
		},
		{
			name:   "n denies",
			ctx:    inRound(0, 3, 2),
			ask:    func(a *cliApprover, ctx context.Context) (core.Approval, error) { return a.Approve(ctx, ls) },
			input:  "n\n",
			prompt: "[1/3] Allow ls? [y/n/A = this and the rest, or reason; /ed to edit]: ",
		},
		{
			name:   "end of input",
			ctx:    inRound(0, 3, 2),
			ask:    func(a *cliApprover, ctx context.Context) (core.Approval, error) { return a.Approve(ctx, ls) },
			prompt: "[1/3] Allow ls? [y/n/A = this and the rest, or reason; /ed to edit]: ",
			said:   "No interactive input available; denying by default.",
		},
		{
			name: "rerun",
			ctx:  context.Background(),
			ask: func(a *cliApprover, ctx context.Context) (core.Approval, error) {
				return a.ApproveRerun(ctx, core.ToolCall{ID: "call-1", Name: "universal_command"})
			},
			input:  "\n",
			prompt: "  Run universal_command again? It may already have run. [y/N or reason; /ed to edit]: ",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &mockNotifier{}
			approval, err := tt.ask(newCLIApproverWithReader(notifier, strings.NewReader(tt.input)), tt.ctx)
			if err != nil || approval != tt.want {
				t.Fatalf("approval = %+v, err = %v, want %+v", approval, err, tt.want)
			}
			said := strings.Join(notifier.messages, "")
			if !strings.Contains(said, tt.prompt) || !strings.Contains(said, tt.said) || (tt.said == "" && strings.Contains(said, "Approved")) {
				t.Fatalf("said %q", said)
			}
		})
	}
}

// a approves at every question. Where later calls would ask, it approves
// them too and says how many; at the last or only question it approves this
// call alone, without a count.
func TestApproverAcceptsAOnEveryQuestion(t *testing.T) {
	for _, tt := range []struct {
		remaining int
		count     string
	}{
		{remaining: 0},
		{remaining: 2, count: "Approved this call and the remaining 2 calls."},
	} {
		notifier := &mockNotifier{}
		approver := newCLIApproverWithReader(notifier, strings.NewReader("a\n"))
		ctx := core.WithApprovalTarget(context.Background(), core.ApprovalTarget{Index: 0, Total: tt.remaining + 1, Remaining: tt.remaining})
		approval, err := approver.Approve(ctx, core.UniversalCommandArgs{Command: []string{"ls"}})
		if err != nil || !approval.Approved || !approval.Rest {
			t.Fatalf("remaining %d: approval = %+v, err = %v", tt.remaining, approval, err)
		}
		said := strings.Join(notifier.messages, "")
		if tt.count == "" && strings.Contains(said, "remaining") || tt.count != "" && !strings.Contains(said, tt.count) {
			t.Fatalf("remaining %d: said %q", tt.remaining, said)
		}
	}
}

// With the REPL's bell on, the terminal rings as a question appears.
func TestApproverRingsWithTheREPLBell(t *testing.T) {
	notifier := &mockNotifier{}
	approver := newCLIApproverWithReader(notifier, strings.NewReader("n\n"))
	on := true
	approver.bell = &on
	if _, err := approver.Approve(context.Background(), core.UniversalCommandArgs{Command: []string{"ls"}}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.messages) < 2 || notifier.messages[0] != "\a" {
		t.Fatalf("messages = %#v, want the bell before the question", notifier.messages)
	}
}
