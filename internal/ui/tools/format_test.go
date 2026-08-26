package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	cliui "lmtools/internal/ui"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// formatTestNotifier captures what the real CLI notifier would print, so these
// golden transcripts stay pinned to StdNotifier's own formatting instead of to
// a second copy of it.
type formatTestNotifier struct {
	*cliui.StdNotifier
	out bytes.Buffer
}

func newFormatTestNotifier() *formatTestNotifier {
	notifier := new(formatTestNotifier)
	notifier.StdNotifier = cliui.NewNotifierWithWriter(&notifier.out)
	return notifier
}

func TestCLIToolUIFormatsReviewedParallelBatch(t *testing.T) {
	notifier := newFormatTestNotifier()
	ui := NewCLIToolUI(notifier)
	calls := []core.ToolCall{
		commandCall(t, "call-status", []string{"git", "status", "--short", "--branch"}, core.UniversalCommandArgs{}),
		commandCall(t, "call-show", []string{"git", "show", "--stat", "--oneline", "--summary", "HEAD"}, core.UniversalCommandArgs{Timeout: 120}),
		commandCall(t, "call-diff", []string{"git", "diff", "HEAD^", "HEAD", "--check"}, core.UniversalCommandArgs{
			Workdir: "/workspace/lmtools",
			Environ: map[string]string{"GOCACHE": "/tmp/lmtools-cache"},
		}),
	}
	results := []core.ToolResult{
		{ID: "call-status", Output: "## main...origin/main [ahead 1]\n?? docs/\n", Elapsed: 58},
		{ID: "call-show", Output: "failing output\n", Error: "exit status 1", Code: errors.ErrCodeExecError, Elapsed: 44},
		{ID: "call-diff", Error: "Command was not executed: user denied permission.", Reason: "user denied permission", Code: errors.ErrCodeNotApproved, NotRun: true},
	}

	for i, call := range calls {
		ui.ShowCall(i, len(calls), call, nil)
	}
	ui.BeforeRun(len(calls), 2, 2)
	ui.AfterExecute(calls, results)

	want := `
>>> Tools requested: 3
[1/3] Command: ["git","status","--short","--branch"]

[2/3] Command: ["git","show","--stat","--oneline","--summary","HEAD"]
      Timeout: 120s

[3/3] Command: ["git","diff","HEAD^","HEAD","--check"]
      Workdir: "/workspace/lmtools"
      Environ: {"GOCACHE":"/tmp/lmtools-cache"}

>>> Running 2 of 3 commands in parallel...

>>> Results:
[1/3] Completed in 58ms
      Output:
## main...origin/main [ahead 1]
?? docs/

[2/3] Failed in 44ms: exit status 1
      Output:
failing output

[3/3] Not run: user denied permission

`
	if got := notifier.out.String(); got != want {
		t.Fatalf("tool transcript mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	for _, unwanted := range []string{"Note:", "universal_command", "call-status", ">>> Executing"} {
		if strings.Contains(notifier.out.String(), unwanted) {
			t.Fatalf("tool transcript contains %q: %s", unwanted, notifier.out.String())
		}
	}
}

func TestCLIToolUIShowCallUsesCompactJSONArgv(t *testing.T) {
	notifier := newFormatTestNotifier()
	call := commandCall(t, "call-escape", []string{
		"printf",
		"a b",
		"line\nnext",
		`quote"`,
		`back\slash`,
	}, core.UniversalCommandArgs{})

	NewCLIToolUI(notifier).ShowCall(0, 1, call, nil)

	want := `
>>> Tools requested: 1
[1/1] Command: ["printf","a b","line\nnext","quote\"","back\\slash"]
`
	if got := notifier.out.String(); got != want {
		t.Fatalf("ShowCall() = %q, want %q", got, want)
	}
}

func TestCLIToolUIShowCallDisplaysStdioSettings(t *testing.T) {
	notifier := newFormatTestNotifier()
	call := commandCall(t, "call-stdio", []string{"sort"}, core.UniversalCommandArgs{
		Stdin:      toolStringPointer("beta\nalpha\n"),
		StdoutFile: "sorted.txt",
		StderrFile: "errors.txt",
	})

	NewCLIToolUI(notifier).ShowCall(0, 1, call, nil)

	want := `
>>> Tools requested: 1
[1/1] Command: ["sort"]
      Stdin: "beta\nalpha\n"
      Stdout file: "sorted.txt"
      Stderr file: "errors.txt"
`
	if got := notifier.out.String(); got != want {
		t.Fatalf("ShowCall() = %q, want %q", got, want)
	}
}

func TestCLIToolUIShowCallDisplaysStdinFile(t *testing.T) {
	notifier := newFormatTestNotifier()
	call := commandCall(t, "call-stdin-file", []string{"sort"}, core.UniversalCommandArgs{
		Workdir:   "/workspace",
		StdinFile: "inputs/data.txt",
	})

	NewCLIToolUI(notifier).ShowCall(0, 1, call, nil)

	want := `
>>> Tools requested: 1
[1/1] Command: ["sort"]
      Workdir: "/workspace"
      Stdin file: "inputs/data.txt"
`
	if got := notifier.out.String(); got != want {
		t.Fatalf("ShowCall() = %q, want %q", got, want)
	}
}

func TestCLIToolUIAfterExecuteHandlesNoOutputTimeoutAndTruncation(t *testing.T) {
	notifier := newFormatTestNotifier()
	calls := []core.ToolCall{{ID: "one"}, {ID: "two"}, {ID: "three"}}
	results := []core.ToolResult{
		{ID: "one", Elapsed: 3},
		{ID: "two", Output: "partial", Error: "command timed out", Code: errors.ErrCodeTimeout, Elapsed: 25},
		{ID: "three", Output: "captured", Elapsed: 7, Truncated: true, TruncatedTo: 1024},
	}

	NewCLIToolUI(notifier).AfterExecute(calls, results)

	got := notifier.out.String()
	for _, want := range []string{
		"[1/3] Completed in 3ms (no captured output)",
		"[2/3] Timed out in 25ms\n      Partial output:\npartial",
		"[3/3] Completed in 7ms (output truncated at 1KiB)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool results missing %q:\n%s", want, got)
		}
	}
}

func TestCLIToolUIAfterExecuteReportsStdioDestinations(t *testing.T) {
	notifier := newFormatTestNotifier()
	calls := []core.ToolCall{
		commandCall(t, "stdout", []string{"sort"}, core.UniversalCommandArgs{
			Stdin:      toolStringPointer("beta\nalpha\n"),
			StdoutFile: "sorted.txt",
		}),
		commandCall(t, "combined", []string{"build"}, core.UniversalCommandArgs{
			StdoutFile: "build.log",
			StderrFile: "build.log",
		}),
		commandCall(t, "separate", []string{"check"}, core.UniversalCommandArgs{
			StdoutFile: "result.txt",
			StderrFile: "errors.txt",
		}),
		commandCall(t, "stdin-only", []string{"consume"}, core.UniversalCommandArgs{
			Stdin: toolStringPointer("input"),
		}),
		commandCall(t, "stdin-file", []string{"consume"}, core.UniversalCommandArgs{
			StdinFile: "input.txt",
		}),
		commandCall(t, "empty-stdin", []string{"consume"}, core.UniversalCommandArgs{
			Stdin: toolStringPointer(""),
		}),
	}
	results := []core.ToolResult{
		{ID: "stdout", Output: "stderr warning", Elapsed: 11},
		{ID: "combined", Elapsed: 12},
		{ID: "separate", Elapsed: 13},
		{ID: "stdin-only", Elapsed: 14},
		{ID: "stdin-file", Elapsed: 15},
		{ID: "empty-stdin", Elapsed: 16},
	}

	NewCLIToolUI(notifier).AfterExecute(calls, results)

	got := notifier.out.String()
	for _, want := range []string{
		`[1/6] Completed in 11ms (stdin provided; stdout file "sorted.txt")`,
		`[2/6] Completed in 12ms (stdout file "build.log"; stderr file "build.log")`,
		`[3/6] Completed in 13ms (stdout file "result.txt"; stderr file "errors.txt")`,
		`[4/6] Completed in 14ms (stdin provided; no captured output)`,
		`[5/6] Completed in 15ms (stdin file "input.txt"; no captured output)`,
		`[6/6] Completed in 16ms (stdin provided; no captured output)`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool results missing %q:\n%s", want, got)
		}
	}
}

func TestCLIToolUIAfterExecuteReportsStdioSettingsOnFailure(t *testing.T) {
	notifier := newFormatTestNotifier()
	calls := []core.ToolCall{commandCall(t, "failed", []string{"sort"}, core.UniversalCommandArgs{
		StdinFile:  "input.txt",
		StdoutFile: "output.txt",
		StderrFile: "errors.txt",
	})}
	results := []core.ToolResult{{
		ID: "failed", Error: "exit status 1", Code: errors.ErrCodeExecError, Elapsed: 9,
	}}

	NewCLIToolUI(notifier).AfterExecute(calls, results)

	want := `[1/1] Failed in 9ms: exit status 1 (stdin file "input.txt"; stdout file "output.txt"; stderr file "errors.txt")`
	if got := notifier.out.String(); !strings.Contains(got, want) {
		t.Fatalf("tool result missing %q:\n%s", want, got)
	}
}

func TestWriteToolOutputUsesBoundedWritesWithoutIndenting(t *testing.T) {
	notifier := &toolOutputWriteRecorder{}
	output := strings.Repeat("x\n", toolOutputChunkBytes+1) + "tail"

	writeToolOutput(notifier, output)

	if notifier.maxWrite > toolOutputChunkBytes {
		t.Fatalf("largest output write = %d bytes, want at most %d", notifier.maxWrite, toolOutputChunkBytes)
	}
	if notifier.outputWrites < 2 {
		t.Fatalf("output writes = %d, want more than one", notifier.outputWrites)
	}
	if got, want := notifier.out.String(), output+"\n"; got != want {
		t.Fatalf("written output differs from source\ngot:  %q\nwant: %q", got, want)
	}
}

func TestCLIToolUIBeforeRunWithNoApprovedCommands(t *testing.T) {
	notifier := newFormatTestNotifier()
	NewCLIToolUI(notifier).BeforeRun(2, 0, 0)
	if got, want := notifier.out.String(), "\n>>> No commands will be run.\n"; got != want {
		t.Fatalf("BeforeRun() = %q, want %q", got, want)
	}
}

func TestToolResultStatusDistinguishesApprovalFailureFromUserDenial(t *testing.T) {
	status, _ := toolResultStatus(core.ToolResult{
		Error:  "approval failed: read response: input/output error",
		Code:   errors.ErrCodeApprovalError,
		NotRun: true,
	})
	if want := "Not run: approval failed: read response: input/output error"; status != want {
		t.Fatalf("toolResultStatus() = %q, want %q", status, want)
	}
}

func TestToolResultStatusDistinguishesCancellationBeforeAndDuringExecution(t *testing.T) {
	beforeRun, _ := toolResultStatus(core.ToolResult{
		Error:  "execution cancelled",
		Reason: "execution cancelled",
		Code:   errors.ErrCodeCancelled,
		NotRun: true,
	})
	if want := "Not run: execution cancelled"; beforeRun != want {
		t.Fatalf("pre-run cancellation status = %q, want %q", beforeRun, want)
	}

	inFlight, _ := toolResultStatus(core.ToolResult{
		Error:   "context canceled",
		Code:    errors.ErrCodeCancelled,
		Elapsed: 25,
	})
	if want := "Cancelled in 25ms"; inFlight != want {
		t.Fatalf("in-flight cancellation status = %q, want %q", inFlight, want)
	}
}

// executorTestApprover answers every approval the same way, so a scenario is
// described entirely by the policy it was built with.
type executorTestApprover struct {
	core.DeclineToolRoundLimitReset
	approve bool
	err     error
}

func (a executorTestApprover) Approve(context.Context, core.UniversalCommandArgs) (bool, error) {
	if a.err != nil {
		return false, a.err
	}
	return a.approve, nil
}

var elapsedMilliseconds = regexp.MustCompile(`in \d+ms`)

type executorScenario struct {
	name     string
	cfg      core.RequestOptions
	approver core.Approver
	cancel   bool
	calls    []core.ToolCall
	want     string
}

// renderExecutorBatch runs one real batch through the real executor and this
// package's real renderer, and returns the transcript with the two parts that
// cannot be pinned — the elapsed milliseconds and the test's temporary
// directory — replaced by placeholders.
func renderExecutorBatch(t *testing.T, scenario executorScenario, dir string) string {
	t.Helper()
	notifier := newFormatTestNotifier()
	cfg := scenario.cfg
	cfg.ToolTimeout = 5 * time.Second
	executor, err := core.NewExecutor(cfg, nil, scenario.approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	ctx := context.Background()
	if scenario.cancel {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cancelled
	}
	executor.ExecuteParallel(ctx, scenario.calls, NewCLIToolUI(notifier))
	transcript := strings.ReplaceAll(notifier.out.String(), dir, "<TMP>")
	return elapsedMilliseconds.ReplaceAllString(transcript, "in NNms")
}

func universalCall(id, args string) core.ToolCall {
	return core.ToolCall{ID: id, Name: core.UniversalCommandToolName, Args: json.RawMessage(args)}
}

// The executor decides what happened and the renderer only reports it, so the
// pairing of the two is what has to stay pinned: a golden transcript per
// outcome the executor can produce, driven through the real executor rather
// than through hand-built ToolResults that could drift from what it stamps.
//
// Every case here rendered exactly this way before the outcome field existed,
// which is what makes it a regression test for the switch to it. The one class
// deliberately not on this list is a command that never started, which used to
// be reported as a zero-millisecond failure; see
// TestCommandThatNeverStartedIsNotRenderedAsAFailure.
func TestExecutorOutcomesRenderTheSameThroughTheCLI(t *testing.T) {
	dir := t.TempDir()
	whitelist := filepath.Join(dir, "wl.txt")
	if err := os.WriteFile(whitelist, []byte(`["/bin/echo"]`+"\n"), 0o600); err != nil {
		t.Fatalf("write whitelist: %v", err)
	}
	blacklist := filepath.Join(dir, "bl.txt")
	if err := os.WriteFile(blacklist, []byte(`["/bin/rm"]`+"\n"), 0o600); err != nil {
		t.Fatalf("write blacklist: %v", err)
	}
	shared := filepath.Join(dir, "shared.log")

	scenarios := []executorScenario{
		{
			name:     "completed",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/echo","hi"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/echo","hi"]

>>> Running 1 command...

>>> Results:
[1/1] Completed in NNms
      Output:
hi

`,
		},
		{
			name:     "failed",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/sh","-c","exit 3"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/sh","-c","exit 3"]

>>> Running 1 command...

>>> Results:
[1/1] Failed in NNms: exit status 3

`,
		},
		{
			name:     "timed out",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/sleep","5"],"timeout":1}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/sleep","5"]
      Timeout: 1s

>>> Running 1 command...

>>> Results:
[1/1] Timed out in NNms

`,
		},
		{
			name:     "blacklisted",
			cfg:      core.RequestOptions{ToolAutoApprove: true, ToolBlacklist: blacklist},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/rm","-rf","/"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/rm","-rf","/"]

>>> No commands will be run.

>>> Results:
[1/1] Not run: blacklisted

`,
		},
		{
			name:     "not in whitelist",
			cfg:      core.RequestOptions{ToolWhitelist: whitelist, ToolNonInteractive: true, ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/cat"],"stdin":"x"}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/cat"]
      Stdin: "x"

>>> No commands will be run.

>>> Results:
[1/1] Not run: not in whitelist (stdin provided)
      Hint: Whitelist file: <TMP>/wl.txt
      Hint: To allow this command, either:
              1. Add {"command":["/bin/cat"],"stdin":true} to your whitelist file and use -tool-whitelist <file>
              2. Run interactively without -tool-non-interactive

`,
		},
		{
			name:     "no terminal for approval",
			cfg:      core.RequestOptions{},
			approver: nil,
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/echo","hi"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/echo","hi"]

>>> No commands will be run.

>>> Results:
[1/1] Not run: no terminal available for approval
      Hint: Allow via one of:
              - Run from a terminal so commands can be approved
              - Use -tool-auto-approve
              - Add the command to a whitelist

`,
		},
		{
			name:     "approval disabled by flag",
			cfg:      core.RequestOptions{ToolNonInteractive: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/echo","hi"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/echo","hi"]

>>> No commands will be run.

>>> Results:
[1/1] Not run: approval disabled by -tool-non-interactive
      Hint: Allow via one of:
              - Run interactively without -tool-non-interactive
              - Use -tool-auto-approve
              - Add the command to a whitelist

`,
		},
		{
			name:     "user denied",
			cfg:      core.RequestOptions{},
			approver: executorTestApprover{approve: false},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/echo","hi"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/echo","hi"]

>>> No commands will be run.

>>> Results:
[1/1] Not run: user denied permission

`,
		},
		{
			name:     "approval failed",
			cfg:      core.RequestOptions{},
			approver: executorTestApprover{err: fmt.Errorf("read response: input/output error")},
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/echo","hi"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/echo","hi"]

>>> No commands will be run.

>>> Results:
[1/1] Not run: approval failed: read response: input/output error

`,
		},
		{
			name:     "unsupported tool",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{{ID: "a", Name: "other_tool", Args: json.RawMessage(`{}`)}},
			want: `
>>> Tools requested: 1
[1/1] Tool: other_tool
      Arguments: {}

>>> No commands will be run.

>>> Results:
[1/1] Not run: unsupported tool: other_tool

`,
		},
		{
			name:     "invalid arguments",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls:    []core.ToolCall{universalCall("a", `{"command":[]}`)},
			want: `
>>> Tools requested: 1
[1/1] Tool: universal_command
      Arguments: {"command":[]}

>>> No commands will be run.

>>> Results:
[1/1] Not run: command array cannot be empty

`,
		},
		{
			name:     "shared output file",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			calls: []core.ToolCall{
				universalCall("a", fmt.Sprintf(`{"command":["/bin/echo","a"],"stdout_file":%q}`, shared)),
				universalCall("b", fmt.Sprintf(`{"command":["/bin/echo","b"],"stdout_file":%q}`, filepath.Join(dir, "./shared.log"))),
			},
			want: `
>>> Tools requested: 2
[1/2] Command: ["/bin/echo","a"]
      Stdout file: "<TMP>/shared.log"

[2/2] Command: ["/bin/echo","b"]
      Stdout file: "<TMP>/shared.log"

>>> No commands will be run.

>>> Results:
[1/2] Not run: another command in this round also writes to "<TMP>/shared.log"; give each command its own output file or run them in separate rounds (stdout file "<TMP>/shared.log")

[2/2] Not run: another command in this round also writes to "<TMP>/shared.log"; give each command its own output file or run them in separate rounds (stdout file "<TMP>/shared.log")

`,
		},
		{
			name:     "cancelled before it could start",
			cfg:      core.RequestOptions{ToolAutoApprove: true},
			approver: executorTestApprover{approve: true},
			cancel:   true,
			calls:    []core.ToolCall{universalCall("a", `{"command":["/bin/echo","hi"]}`)},
			want: `
>>> Tools requested: 1
[1/1] Command: ["/bin/echo","hi"]

>>> No commands will be run.

>>> Results:
[1/1] Not run: execution cancelled

`,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			got := renderExecutorBatch(t, scenario, dir)
			if got != scenario.want {
				t.Fatalf("transcript mismatch\n--- got ---\n%s--- want ---\n%s", got, scenario.want)
			}
		})
	}
}

// A command os/exec refused to start never ran, and reporting it as a failure
// that took zero milliseconds describes something that did not happen. The
// executor is the only layer that can tell the two apart — it holds the Cmd,
// and Cmd.Process is what says whether a process was ever created — so it says
// so on the result rather than leaving the renderer to guess from an error
// string.
func TestCommandThatNeverStartedIsNotRenderedAsAFailure(t *testing.T) {
	dir := t.TempDir()
	notARegularFile := filepath.Join(dir, "adirectory")
	if err := os.Mkdir(notARegularFile, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	scenarios := []executorScenario{
		{
			name:  "missing workdir with a redirection",
			calls: []core.ToolCall{universalCall("a", fmt.Sprintf(`{"command":["/bin/echo","hi"],"workdir":"/no/such/dir","stdout_file":%q}`, filepath.Join(dir, "out.log")))},
			want:  `[1/1] Not run: chdir /no/such/dir: no such file or directory (stdout file "<TMP>/out.log")`,
		},
		{
			name:  "missing workdir with no redirection",
			calls: []core.ToolCall{universalCall("a", `{"command":["/bin/echo","hi"],"workdir":"/no/such/dir"}`)},
			want:  `[1/1] Not run: chdir /no/such/dir: no such file or directory`,
		},
		{
			name:  "executable that does not exist",
			calls: []core.ToolCall{universalCall("a", fmt.Sprintf(`{"command":["./biuld.sh"],"stdout_file":%q}`, filepath.Join(dir, "out.log")))},
			want:  `[1/1] Not run: fork/exec ./biuld.sh: no such file or directory (stdout file "<TMP>/out.log")`,
		},
		{
			name:  "redirection that cannot be opened",
			calls: []core.ToolCall{universalCall("a", fmt.Sprintf(`{"command":["/bin/echo","hi"],"stdout_file":%q}`, notARegularFile))},
			want:  `[1/1] Not run: open stdout file "<TMP>/adirectory": "<TMP>/adirectory" is not a regular file (stdout file "<TMP>/adirectory")`,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			scenario.cfg = core.RequestOptions{ToolAutoApprove: true}
			scenario.approver = executorTestApprover{approve: true}
			got := renderExecutorBatch(t, scenario, dir)
			if !strings.Contains(got, scenario.want) {
				t.Fatalf("transcript missing %q:\n%s", scenario.want, got)
			}
			if strings.Contains(got, "Failed in") {
				t.Fatalf("a command that never started was reported as a failure:\n%s", got)
			}
		})
	}
}

// The renderer must not hold a second list of which error codes mean "never
// ran". It held one, nine untyped string constants long, with nothing keeping
// it in step with the executor; this changeset alone added two codes to that
// area. A code this package has never heard of has to render as the executor
// described it, hints and all, without an edit here.
func TestUnknownPreRunDenialCodeRendersFromTheResultAlone(t *testing.T) {
	status, hints := toolResultStatus(core.ToolResult{
		NotRun: true,
		Reason: "denied by a policy this renderer has never heard of",
		Hints:  []string{"Add a rule that admits it"},
		Code:   "DENIED_BY_SOMETHING_NEW",
		Error:  "denied: denied by a policy this renderer has never heard of\nAdd a rule that admits it",
	})
	if want := "Not run: denied by a policy this renderer has never heard of"; status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
	if want := []string{"Add a rule that admits it"}; !reflect.DeepEqual(hints, want) {
		t.Fatalf("hints = %#v, want %#v", hints, want)
	}
}

// AfterExecute decodes each call's arguments for itself. It used to read them
// from a cache ShowCall filled in, which is the same decode of the same bytes
// behind the same tool-name gate — so the two phases have to agree whether or
// not the review phase ran at all, and that equality is what made the cache
// removable.
func TestAfterExecuteRendersTheSameWithoutTheReviewPhase(t *testing.T) {
	calls := []core.ToolCall{
		commandCall(t, "redirected", []string{"sort"}, core.UniversalCommandArgs{
			Stdin:      toolStringPointer(strings.Repeat("payload\n", 4096)),
			StdoutFile: "sorted.txt",
			StderrFile: "errors.txt",
		}),
		{ID: "undecodable", Name: core.UniversalCommandToolName, Args: json.RawMessage(`{"command":`)},
		{ID: "other-tool", Name: "some_other_tool", Args: json.RawMessage(`{"a":1}`)},
		commandCall(t, "empty-argv", nil, core.UniversalCommandArgs{StdinFile: "in.txt"}),
	}
	results := []core.ToolResult{
		{ID: "redirected", Elapsed: 4},
		{ID: "undecodable", Error: "invalid command format", Code: errors.ErrCodeInvalidInput, NotRun: true},
		{ID: "other-tool", Error: "unsupported tool: some_other_tool", Code: errors.ErrCodeInvalidInput, NotRun: true},
		{ID: "empty-argv", Error: "command array cannot be empty", Code: errors.ErrCodeInvalidInput, NotRun: true},
	}

	reviewed := newFormatTestNotifier()
	reviewedUI := NewCLIToolUI(reviewed)
	for i, call := range calls {
		reviewedUI.ShowCall(i, len(calls), call, nil)
	}
	reviewed.out.Reset()
	reviewedUI.AfterExecute(calls, results)

	unreviewed := newFormatTestNotifier()
	NewCLIToolUI(unreviewed).AfterExecute(calls, results)

	if reviewed.out.String() != unreviewed.out.String() {
		t.Fatalf("results differ with and without the review phase\n--- reviewed ---\n%s--- unreviewed ---\n%s",
			reviewed.out.String(), unreviewed.out.String())
	}
}

func commandCall(t *testing.T, id string, command []string, extra core.UniversalCommandArgs) core.ToolCall {
	t.Helper()
	extra.Command = command
	args, err := json.Marshal(extra)
	if err != nil {
		t.Fatalf("marshal command args: %v", err)
	}
	return core.ToolCall{
		ID:   id,
		Name: core.UniversalCommandToolName,
		Args: args,
	}
}

func toolStringPointer(value string) *string {
	return &value
}

type toolOutputWriteRecorder struct {
	out          bytes.Buffer
	outputWrites int
	maxWrite     int
}

func (n *toolOutputWriteRecorder) Infof(string, ...interface{})  {}
func (n *toolOutputWriteRecorder) Warnf(string, ...interface{})  {}
func (n *toolOutputWriteRecorder) Errorf(string, ...interface{}) {}

func (n *toolOutputWriteRecorder) Promptf(format string, args ...interface{}) {
	if format == "%s" && len(args) == 1 {
		chunk, ok := args[0].(string)
		if ok {
			n.outputWrites++
			if len(chunk) > n.maxWrite {
				n.maxWrite = len(chunk)
			}
		}
	}
	fmt.Fprintf(&n.out, format, args...)
}

// The approval line is read by a person deciding whether to run the command it
// shows. encoding/json escapes &, <, and > for HTML by default, which turns
// `make build && ./run > out 2>&1` into something the reviewer has to decode
// before they can judge it.
func TestApprovalLineIsNotHTMLEscaped(t *testing.T) {
	notifier := newFormatTestNotifier()
	ui := NewCLIToolUI(notifier)

	call := commandCall(t, "shell", []string{"sh", "-c", "make build && ./run > out 2>&1"}, core.UniversalCommandArgs{
		Workdir: "/srv/a&b",
		Environ: map[string]string{"FLAGS": "-D<x>"},
		Stdin:   toolStringPointer("a < b && c > d"),
	})
	ui.ShowCall(0, 1, call, nil)
	rendered := notifier.out.String()

	for _, escaped := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(rendered, escaped) {
			t.Fatalf("rendered approval line contains %s:\n%s", escaped, rendered)
		}
	}
	for _, want := range []string{
		`["sh","-c","make build && ./run > out 2>&1"]`,
		`Workdir: "/srv/a&b"`,
		`{"FLAGS":"-D<x>"}`,
		`Stdin: "a < b && c > d"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered approval line missing %s:\n%s", want, rendered)
		}
	}
}

// The executor decides whether the flag or the environment closed the door and
// hands the reason and guidance over as structured fields. This package renders
// them without wording of its own — restating a cause here would send half
// these readers after a flag they never passed.
func TestDenialStatusRendersTheExecutorReasonAndHints(t *testing.T) {
	tests := []struct {
		name   string
		result core.ToolResult
	}{
		{
			name: "non-interactive",
			result: core.ToolResult{
				NotRun: true,
				Reason: "no terminal available for approval",
				Hints:  []string{"Allow via one of:\n  - Run from a terminal so commands can be approved"},
				Code:   errors.ErrCodeDeniedNonInteractive,
				Error:  "denied: no terminal available for approval\nAllow via one of:\n  - Run from a terminal so commands can be approved",
			},
		},
		{
			name: "not whitelisted",
			result: core.ToolResult{
				NotRun: true,
				Reason: "not in whitelist",
				Hints:  []string{"Whitelist file: /tmp/wl.txt", "To allow this command, either:"},
				Code:   errors.ErrCodeDeniedNotWhitelisted,
				Error:  "denied: not in whitelist\nWhitelist file: /tmp/wl.txt\nTo allow this command, either:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, hints := toolResultStatus(tt.result)
			if want := "Not run: " + tt.result.Reason; status != want {
				t.Fatalf("status = %q, want %q", status, want)
			}
			if !reflect.DeepEqual(hints, tt.result.Hints) {
				t.Fatalf("hints = %#v, want the executor's own %#v", hints, tt.result.Hints)
			}
		})
	}
}
