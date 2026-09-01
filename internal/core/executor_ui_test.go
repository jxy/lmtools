package core

import (
	"context"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type lifecycleRecorder struct {
	events []string
}

func (r *lifecycleRecorder) add(event string) {
	r.events = append(r.events, event)
}

type recordingToolUI struct {
	recorder *lifecycleRecorder
}

func (ui *recordingToolUI) ShowCall(index, total int, call ToolCall, _ *UniversalCommandArgs) {
	ui.recorder.add(fmt.Sprintf("show:%d/%d:%s", index+1, total, call.ID))
}

// BeforeRun records its three counts by name. Positional recording hid which
// argument was which, and the executor passes them in an order the UI renders
// as "Running <runnable> of <total> commands".
func (ui *recordingToolUI) BeforeRun(total, runnable, parallel int) {
	ui.recorder.add(fmt.Sprintf("run:total=%d,runnable=%d,parallel=%d", total, runnable, parallel))
}

func (ui *recordingToolUI) AfterExecute(_ []ToolCall, _ []ToolResult) {
	ui.recorder.add("results")
}

type recordingLifecycleApprover struct {
	DeclineToolRoundLimitReset
	recorder *lifecycleRecorder
	approve  bool
	err      error
}

func (a *recordingLifecycleApprover) Approve(_ context.Context, args UniversalCommandArgs) (bool, error) {
	a.recorder.add("approve:" + args.Command[len(args.Command)-1])
	if a.err != nil {
		return false, a.err
	}
	return a.approve, nil
}

// The batch is built so that BeforeRun's three counts are three different
// numbers: three calls arrive, one is denied by the blacklist before it can be
// prepared, and the parallel limit is below what is left. A batch where total,
// runnable, and parallel all happen to be equal cannot tell an argument swap at
// the call site from the correct order, and the swap is user-visible — the CLI
// renders these as "Running <runnable> of <total> commands".
func TestExecuteParallelDisplaysAndApprovesBeforeRun(t *testing.T) {
	recorder := &lifecycleRecorder{}
	approver := &recordingLifecycleApprover{recorder: recorder, approve: true}
	blacklistPath := filepath.Join(t.TempDir(), "blacklist.txt")
	if err := os.WriteFile(blacklistPath, []byte(`["echo","denied"]`+"\n"), constants.FilePerm); err != nil {
		t.Fatal(err)
	}
	cfg := RequestOptions{
		ToolTimeout:     5 * time.Second,
		MaxToolParallel: 1,
		ToolBlacklist:   blacklistPath,
	}
	executor, err := NewExecutor(cfg, NewTestLogger(false), approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	calls := []ToolCall{
		{ID: "one", Name: "universal_command", Args: []byte(`{"command":["echo","one"]}`)},
		{ID: "two", Name: "universal_command", Args: []byte(`{"command":["echo","two"]}`)},
		{ID: "denied", Name: "universal_command", Args: []byte(`{"command":["echo","denied"]}`)},
	}
	ui := &recordingToolUI{recorder: recorder}
	results := executor.ExecuteParallel(context.Background(), calls, ui)
	for _, result := range results[:2] {
		if result.Error != "" {
			t.Fatalf("result %s failed: %s", result.ID, result.Error)
		}
	}
	if results[2].Code != errors.ErrCodeDeniedBlacklist {
		t.Fatalf("denied result = %#v, want code %s", results[2], errors.ErrCodeDeniedBlacklist)
	}

	want := []string{
		"show:1/3:one",
		"approve:one",
		"show:2/3:two",
		"approve:two",
		"show:3/3:denied",
		"run:total=3,runnable=2,parallel=1",
		"results",
	}
	if !slices.Equal(recorder.events, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", recorder.events, want)
	}
}

func TestExecuteParallelDoesNotPromptWithoutReviewedUI(t *testing.T) {
	recorder := &lifecycleRecorder{}
	approver := &recordingLifecycleApprover{recorder: recorder, approve: true}
	executor, err := NewExecutor(RequestOptions{}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "unreviewed-call",
		Name: "universal_command",
		Args: []byte(`{"command":["echo","no"],"stdout_file":"out.txt"}`),
	}}, nil)[0]
	if result.Code != errors.ErrCodeApprovalError {
		t.Fatalf("unreviewed result = %#v, want %s", result, errors.ErrCodeApprovalError)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("approver was called without review: %v", recorder.events)
	}
}

// Without an approver the policy denies up front rather than routing through
// the generic approval-error backstop, so the caller gets advice it can act on
// and the reason names the environment instead of a flag nobody passed.
func TestExecuteParallelDeniesWhenNoApproverCanBeAsked(t *testing.T) {
	executor, err := NewExecutor(RequestOptions{}, NewTestLogger(false), nil)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "unapprovable-call",
		Name: "universal_command",
		Args: []byte(`{"command":["echo","no"]}`),
	}}, TestToolUI{})[0]
	if result.Code != errors.ErrCodeDeniedNonInteractive {
		t.Fatalf("result code = %q, want %q", result.Code, errors.ErrCodeDeniedNonInteractive)
	}
	for _, want := range []string{
		"no terminal available for approval",
		"Run from a terminal so commands can be approved",
		"-tool-auto-approve",
	} {
		if !strings.Contains(result.Error, want) {
			t.Fatalf("result error = %q, want it to contain %q", result.Error, want)
		}
	}
	if strings.Contains(result.Error, "-tool-non-interactive") {
		t.Fatalf("result error = %q, want no mention of a flag that was not passed", result.Error)
	}
	// The same parts reach the CLI as structured fields, so the UI renders the
	// executor's reason instead of parsing it back out of Error.
	if result.Reason != "no terminal available for approval" {
		t.Fatalf("result reason = %q, want the executor's own wording", result.Reason)
	}
	if len(result.Hints) != 1 || !strings.Contains(result.Hints[0], "Run from a terminal so commands can be approved") {
		t.Fatalf("result hints = %#v, want the restore guidance", result.Hints)
	}
}

// The backstop is unreachable through NewExecutor, so reach it the only way
// left: a policy that claims it can prompt with nothing to prompt with.
func TestExecuteParallelReportsMissingApprover(t *testing.T) {
	executor, err := NewExecutor(RequestOptions{}, NewTestLogger(false), nil)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	executor.policy.canPrompt = true

	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "unapprovable-call",
		Name: "universal_command",
		Args: []byte(`{"command":["echo","no"]}`),
	}}, TestToolUI{})[0]
	if result.Code != errors.ErrCodeApprovalError {
		t.Fatalf("result code = %q, want %q", result.Code, errors.ErrCodeApprovalError)
	}
	// The backstop composes its message from the same helpers as the policy
	// denial, so the advice cannot drift between the two paths. It now composes
	// it through denyResult as well, which spells the prefix the way the other
	// two policy denials already did: "command denied" and "denied" were the
	// same sentence written twice.
	for _, want := range []string{
		"denied: no terminal available for approval",
		"Run from a terminal so commands can be approved",
		"-tool-auto-approve",
	} {
		if !strings.Contains(result.Error, want) {
			t.Fatalf("result error = %q, want it to contain %q", result.Error, want)
		}
	}
}

func TestDeniedApprovalBecomesExplicitModelVisibleToolError(t *testing.T) {
	approver := &recordingLifecycleApprover{recorder: &lifecycleRecorder{}, approve: false}
	executor, err := NewExecutor(RequestOptions{}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "denied-call",
		Name: "universal_command",
		Args: []byte(`{"command":["echo","no"],"stdout_file":"out.txt"}`),
	}}, TestToolUI{})[0]
	const denial = "user denied permission"
	if result.Code != errors.ErrCodeNotApproved || result.Error != denial {
		t.Fatalf("denied result = %#v, want code %s and error %q", result, errors.ErrCodeNotApproved, denial)
	}
	// The refusal reaches the model as a refusal, and the sentence saying so is
	// notRunStatement's rather than this arm's: it used to be composed here, and
	// was the only one of the executor's refusals that said it at all.
	block := ToolResultBlockFromResult(result, "universal_command")
	if !block.IsError || block.Content != "Command was not executed.\n"+denial {
		t.Fatalf("model-visible block = %#v, want the not-run statement above %q", block, denial)
	}
}

// The operator reads Reason and Hints, the model reads Error, and they are the
// same refusal. The not-whitelisted denial told the operator which file to edit
// and told the model to "add the command to your whitelist file" without ever
// naming one — while the approval-unavailable denial, composed ten lines away,
// did name it. denyResult writes both from one list so a hint cannot reach one
// audience and not the other.
func TestPolicyDenialGivesTheModelEveryHintTheOperatorSees(t *testing.T) {
	whitelistPath := filepath.Join(t.TempDir(), "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["/bin/echo"]`+"\n"), constants.FilePerm); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(RequestOptions{
		ToolWhitelist:      whitelistPath,
		ToolNonInteractive: true,
		ToolAutoApprove:    true,
	}, NewTestLogger(false), nil)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "unlisted-call",
		Name: "universal_command",
		Args: []byte(`{"command":["/bin/ls","-l"]}`),
	}}, TestToolUI{})[0]

	if result.Code != errors.ErrCodeDeniedNotWhitelisted {
		t.Fatalf("result code = %q, want %q", result.Code, errors.ErrCodeDeniedNotWhitelisted)
	}
	if len(result.Hints) == 0 {
		t.Fatalf("denial recorded no hints: %#v", result)
	}
	for _, hint := range result.Hints {
		if !strings.Contains(result.Error, hint) {
			t.Fatalf("model-facing error omits the hint the operator was shown\nerror: %q\nhint:  %q", result.Error, hint)
		}
	}
	if !strings.Contains(result.Error, whitelistPath) {
		t.Fatalf("model-facing error = %q, want it to name the whitelist it says to edit (%s)", result.Error, whitelistPath)
	}
}

// Whether a command ran is the executor's own statement, because the executor
// is the only layer that knows. Every path that refuses a call before a process
// exists has to say so; a path that forgets renders in the CLI as a failure
// that took zero milliseconds, which is what the removed error-code list did
// for every code nobody remembered to add to it.
func TestEveryRefusedCallIsMarkedNotRun(t *testing.T) {
	dir := t.TempDir()
	whitelistPath := filepath.Join(dir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["/bin/echo"]`+"\n"), constants.FilePerm); err != nil {
		t.Fatal(err)
	}
	blacklistPath := filepath.Join(dir, "blacklist.txt")
	if err := os.WriteFile(blacklistPath, []byte(`["/bin/rm"]`+"\n"), constants.FilePerm); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(dir, "shared.log")

	overCap := make([]ToolCall, DefaultMaxToolCalls+1)
	for i := range overCap {
		overCap[i] = universalToolCall(fmt.Sprintf("over-%d", i), `{"command":["/bin/echo","hi"]}`)
	}

	tests := []struct {
		name     string
		cfg      RequestOptions
		approver Approver
		// withoutUI reaches the one refusal that needs a batch with no reviewed
		// UI, which is otherwise indistinguishable from leaving the field unset.
		withoutUI bool
		cancel    bool
		calls     []ToolCall
		index     int
		wantRan   bool
	}{
		{
			name:  "blacklisted",
			cfg:   RequestOptions{ToolAutoApprove: true, ToolBlacklist: blacklistPath},
			calls: []ToolCall{universalToolCall("a", `{"command":["/bin/rm","-rf","/"]}`)},
		},
		{
			name:  "not whitelisted",
			cfg:   RequestOptions{ToolWhitelist: whitelistPath, ToolNonInteractive: true, ToolAutoApprove: true},
			calls: []ToolCall{universalToolCall("a", `{"command":["/bin/ls"]}`)},
		},
		{
			name:  "approval unavailable",
			calls: []ToolCall{universalToolCall("a", `{"command":["/bin/echo","hi"]}`)},
		},
		{
			name:     "user denied",
			approver: &recordingLifecycleApprover{recorder: &lifecycleRecorder{}, approve: false},
			calls:    []ToolCall{universalToolCall("a", `{"command":["/bin/echo","hi"]}`)},
		},
		{
			name:     "approval failed",
			approver: &recordingLifecycleApprover{recorder: &lifecycleRecorder{}, err: fmt.Errorf("input/output error")},
			calls:    []ToolCall{universalToolCall("a", `{"command":["/bin/echo","hi"]}`)},
		},
		{
			name:      "no reviewed UI",
			approver:  &recordingLifecycleApprover{recorder: &lifecycleRecorder{}, approve: true},
			withoutUI: true,
			calls:     []ToolCall{universalToolCall("a", `{"command":["/bin/echo","hi"]}`)},
		},
		{
			name:   "cancelled before it could start",
			cfg:    RequestOptions{ToolAutoApprove: true},
			cancel: true,
			calls:  []ToolCall{universalToolCall("a", `{"command":["/bin/echo","hi"]}`)},
		},
		{
			name:  "unsupported tool",
			cfg:   RequestOptions{ToolAutoApprove: true},
			calls: []ToolCall{{ID: "a", Name: "other_tool", Args: []byte(`{}`)}},
		},
		{
			name:  "arguments that do not parse",
			cfg:   RequestOptions{ToolAutoApprove: true},
			calls: []ToolCall{universalToolCall("a", `{"command":[]}`)},
		},
		{
			name: "output file shared with another call",
			cfg:  RequestOptions{ToolAutoApprove: true},
			calls: []ToolCall{
				universalToolCall("a", fmt.Sprintf(`{"command":["/bin/echo","a"],"stdout_file":%q}`, shared)),
				universalToolCall("b", fmt.Sprintf(`{"command":["/bin/echo","b"],"stdout_file":%q}`, filepath.Join(dir, "./shared.log"))),
			},
		},
		{
			name:  "past the per-round call cap",
			cfg:   RequestOptions{ToolNonInteractive: true, ToolWhitelist: whitelistPath},
			calls: overCap,
			index: DefaultMaxToolCalls,
		},
		{
			name:  "os/exec refused to start it",
			cfg:   RequestOptions{ToolAutoApprove: true},
			calls: []ToolCall{universalToolCall("a", `{"command":["/bin/echo","hi"],"workdir":"/no/such/dir"}`)},
		},
		{
			name:    "ran and exited non-zero",
			cfg:     RequestOptions{ToolAutoApprove: true},
			calls:   []ToolCall{universalToolCall("a", `{"command":["/bin/sh","-c","exit 3"]}`)},
			wantRan: true,
		},
		{
			name:    "ran and succeeded",
			cfg:     RequestOptions{ToolAutoApprove: true},
			calls:   []ToolCall{universalToolCall("a", `{"command":["/bin/echo","hi"]}`)},
			wantRan: true,
		},
		{
			name:    "ran and timed out",
			cfg:     RequestOptions{ToolAutoApprove: true},
			calls:   []ToolCall{universalToolCall("a", `{"command":["/bin/sleep","5"],"timeout":1}`)},
			wantRan: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.ToolTimeout = 5 * time.Second
			executor, err := NewExecutor(cfg, NewTestLogger(false), tt.approver)
			if err != nil {
				t.Fatalf("NewExecutor() error = %v", err)
			}
			var ui ToolUI
			if !tt.withoutUI {
				ui = TestToolUI{}
			}
			ctx := context.Background()
			if tt.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}

			result := executor.ExecuteParallel(ctx, tt.calls, ui)[tt.index]
			if result.NotRun == tt.wantRan {
				t.Fatalf("NotRun = %v, want %v for %#v", result.NotRun, !tt.wantRan, result)
			}
			if !tt.wantRan && result.Error == "" {
				t.Fatalf("a refused call reported no error: %#v", result)
			}
		})
	}
}

func universalToolCall(id, args string) ToolCall {
	return ToolCall{ID: id, Name: UniversalCommandToolName, Args: []byte(args)}
}

func TestApprovalFailureIsNotReportedAsUserDenial(t *testing.T) {
	approver := &recordingLifecycleApprover{
		recorder: &lifecycleRecorder{},
		err:      fmt.Errorf("read response: input/output error"),
	}
	executor, err := NewExecutor(RequestOptions{}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "approval-error",
		Name: "universal_command",
		Args: []byte(`{"command":["echo","no"]}`),
	}}, TestToolUI{})[0]
	if result.Code != errors.ErrCodeApprovalError {
		t.Fatalf("approval failure code = %q, want %q", result.Code, errors.ErrCodeApprovalError)
	}
	if want := "approval failed: read response: input/output error"; result.Error != want {
		t.Fatalf("approval failure = %q, want %q", result.Error, want)
	}
}
