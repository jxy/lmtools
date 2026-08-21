package main

import (
	"context"
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
			name:     "deny with empty input",
			input:    "\n",
			expected: false,
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

			result, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
			if err != nil {
				t.Fatalf("Approve() error = %v", err)
			}

			if result != tt.expected {
				t.Errorf("Approve() = %v, expected %v", result, tt.expected)
			}
			if len(notifier.messages) == 0 || notifier.messages[0] != "      Allow execution? [y/N]: " {
				t.Fatalf("approval prompt = %#v", notifier.messages)
			}
		})
	}
}

// approverEntryPoint is one of the two yes/no conversations an approver
// holds. Both delegate to ask and readYesNo, so shared-behavior tests run
// against each entry point rather than trusting the delegation.
type approverEntryPoint struct {
	name      string
	invoke    func(*cliApprover, context.Context) (bool, error)
	prompt    string
	eofNotice string
}

func approverEntryPoints() []approverEntryPoint {
	return []approverEntryPoint{
		{
			name: "Approve",
			invoke: func(a *cliApprover, ctx context.Context) (bool, error) {
				return a.Approve(ctx, core.UniversalCommandArgs{})
			},
			prompt:    "      Allow execution? [y/N]: ",
			eofNotice: "\n      No interactive input available; denying by default.\n",
		},
		{
			name: "ApproveToolRoundLimitReset",
			invoke: func(a *cliApprover, ctx context.Context) (bool, error) {
				return a.ApproveToolRoundLimitReset(ctx, 4)
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
				if approved != tt.expected {
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

var errTestReadFailure = errors.New("stdin exploded")

// Only an end of input is an answer. A stream that failed for any other reason
// did not report what the operator typed, so it is an error rather than a
// silent denial.
func TestApproverReportsReadFailure(t *testing.T) {
	notifier := &mockNotifier{}
	approver := newCLIApproverWithReader(notifier, iotest.ErrReader(errTestReadFailure))

	approved, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
	if approved {
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
	if denied {
		t.Fatal("Approve() first call approved despite an explicit n")
	}

	approved, err := approver.Approve(context.Background(), core.UniversalCommandArgs{})
	if err != nil {
		t.Fatalf("Approve() second error = %v", err)
	}
	if !approved {
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
				if !approved {
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
	reviewNotifier, approver := newOperatorToolSurface(base)
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
