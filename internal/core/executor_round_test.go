package core

import (
	"context"
	"fmt"
	"lmtools/internal/errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// roundApprover answers from a script and records the target each question
// carried.
type roundApprover struct {
	DeclineNonCommandApprovals
	mu        sync.Mutex
	answers   []Approval
	asked     []string
	questions int
	// cancel, when set, is called in place of the answer once cancelAfter
	// questions have been answered.
	cancel      context.CancelFunc
	cancelAfter int
}

func (a *roundApprover) Approve(ctx context.Context, args UniversalCommandArgs) (Approval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.questions++
	if a.cancel != nil && len(a.asked) >= a.cancelAfter {
		a.cancel()
		return Approval{}, ctx.Err()
	}
	target, ok := ApprovalTargetFrom(ctx)
	label := strings.Join(args.Command, " ")
	if ok {
		label = fmt.Sprintf("[%d/%d] %s rest=%d", target.Index+1, target.Total, args.Command[0], target.Remaining)
	}
	a.asked = append(a.asked, label)
	answer := a.answers[0]
	a.answers = a.answers[1:]
	return answer, nil
}

type observingToolUI struct {
	recordingToolUI
	mu       sync.Mutex
	started  []int
	finished []int
	ready    []int
}

func (ui *observingToolUI) CallStarted(index int, _ ToolCall) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.started = append(ui.started, index)
}

func (ui *observingToolUI) CallFinished(index int) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.finished = append(ui.finished, index)
}

func (ui *observingToolUI) ResultReady(index, total int, _ ToolCall, result ToolResult) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.ready = append(ui.ready, index)
	ui.recorder.add(fmt.Sprintf("ready:%d/%d:%s", index+1, total, result.ID))
}

func roundCall(id string, argv ...string) ToolCall {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = fmt.Sprintf("%q", arg)
	}
	return ToolCall{ID: id, Name: UniversalCommandToolName, Args: []byte(`{"command":[` + strings.Join(quoted, ",") + `]}`)}
}

func TestExecuteParallelApprovesTheRestOfTheRound(t *testing.T) {
	approver := &roundApprover{answers: []Approval{{Approved: true, Rest: true}}}
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatal(err)
	}
	calls := []ToolCall{roundCall("one", "echo", "one"), roundCall("two", "echo", "two"), roundCall("three", "echo", "three")}
	results := executor.ExecuteParallel(context.Background(), calls, &recordingToolUI{recorder: &lifecycleRecorder{}})
	for _, result := range results {
		if result.Error != "" || result.NotRun {
			t.Fatalf("result %s = %#v", result.ID, result)
		}
	}
	// One question named the call and how many more it would approve.
	if want := []string{"[1/3] echo rest=2"}; !slices.Equal(approver.asked, want) {
		t.Fatalf("asked = %#v, want %#v", approver.asked, want)
	}
}

// Every question names its call, a single call included, because the review
// above it may have scrolled away by the time the question is answered.
func TestExecuteParallelNamesTheCallOfEveryQuestion(t *testing.T) {
	approver := &roundApprover{answers: []Approval{{Approved: true}}}
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatal(err)
	}
	executor.ExecuteParallel(context.Background(), []ToolCall{roundCall("one", "echo", "one")}, TestToolUI{})
	if want := []string{"[1/1] echo rest=0"}; !slices.Equal(approver.asked, want) {
		t.Fatalf("asked = %#v, want %#v", approver.asked, want)
	}
}

// noteRecordingUI records the notes shown under calls, as "index: note".
type noteRecordingUI struct {
	TestToolUI
	notes []string
}

func (ui *noteRecordingUI) ShowCallNote(index int, note string) {
	ui.notes = append(ui.notes, fmt.Sprintf("%d: %s", index, note))
}

// A command whose program or working directory is missing when the round is
// shown carries a warning under its review, and is still asked about and
// run: an earlier call of the round may create what it needs. The run then
// reports a command that still cannot start as not run.
func TestExecuteParallelWarnsAboutACommandThatCannotStartYet(t *testing.T) {
	approver := &roundApprover{answers: []Approval{{Approved: true}, {Approved: true}, {Approved: true}}}
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatal(err)
	}
	calls := []ToolCall{
		roundCall("missing", "lmc-test-no-such-program-0f3a"),
		{ID: "nodir", Name: UniversalCommandToolName, Args: []byte(`{"command":["echo","x"],"workdir":"/no/such/dir/0f3a"}`)},
		roundCall("fine", "echo", "fine"),
	}
	ui := &noteRecordingUI{}
	results := executor.ExecuteParallel(context.Background(), calls, ui)
	if len(approver.asked) != 3 {
		t.Fatalf("asked = %#v, want every call", approver.asked)
	}
	if len(ui.notes) != 2 || !strings.HasPrefix(ui.notes[0], "0: Cannot start now: ") || !strings.Contains(ui.notes[0], "executable file not found") ||
		!strings.HasPrefix(ui.notes[1], "1: Cannot start now: ") || !strings.Contains(ui.notes[1], "/no/such/dir/0f3a") {
		t.Fatalf("notes = %q", ui.notes)
	}
	if !results[0].NotRun || !strings.Contains(results[0].Error, "executable file not found") {
		t.Fatalf("missing program = %#v", results[0])
	}
	if !results[1].NotRun || !strings.Contains(results[1].Error, "/no/such/dir/0f3a") {
		t.Fatalf("missing workdir = %#v", results[1])
	}
	if results[2].Error != "" || results[2].NotRun {
		t.Fatalf("runnable call = %#v", results[2])
	}
}

// A round that asks nothing has no question to warn ahead of; the results
// report what could not start.
func TestExecuteParallelWarnsOnlyWhenItAsks(t *testing.T) {
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second, ToolAutoApprove: true}, NewTestLogger(false), nil)
	if err != nil {
		t.Fatal(err)
	}
	ui := &noteRecordingUI{}
	results := executor.ExecuteParallel(context.Background(), []ToolCall{roundCall("missing", "lmc-test-no-such-program-0f3a")}, ui)
	if len(ui.notes) != 0 || !results[0].NotRun {
		t.Fatalf("notes %q; result %#v", ui.notes, results[0])
	}
}

// One call of a round can create what a later one needs to start.
func TestExecuteParallelRunsACallAnEarlierCallMadeStartable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "made")
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second, ToolAutoApprove: true, MaxToolParallel: 1}, NewTestLogger(false), nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := []ToolCall{
		roundCall("mkdir", "mkdir", dir),
		{ID: "inside", Name: UniversalCommandToolName, Args: []byte(fmt.Sprintf(`{"command":["pwd"],"workdir":%q}`, dir))},
	}
	results := executor.ExecuteParallel(context.Background(), calls, TestToolUI{})
	for _, result := range results {
		if result.Error != "" || result.NotRun {
			t.Fatalf("result %s = %#v", result.ID, result)
		}
	}
	if !strings.Contains(results[1].Output, "made") {
		t.Fatalf("pwd printed %q", results[1].Output)
	}
}

// Results reach an observer in request order, each as soon as it and every
// earlier result are known: a fast second call waits for a slow first one.
func TestExecuteParallelReportsResultsInOrderAsTheyFinish(t *testing.T) {
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second, ToolAutoApprove: true}, NewTestLogger(false), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &lifecycleRecorder{}
	ui := &observingToolUI{recordingToolUI: recordingToolUI{recorder: recorder}}
	calls := []ToolCall{
		roundCall("slow", "sleep", "0.3"),
		roundCall("fast", "echo", "fast"),
		{ID: "unsupported", Name: "no_such_tool", Args: []byte(`{}`)},
	}
	executor.ExecuteParallel(context.Background(), calls, ui)
	if !slices.Equal(ui.ready, []int{0, 1, 2}) {
		t.Fatalf("ready order = %v", ui.ready)
	}
	started := slices.Clone(ui.started)
	slices.Sort(started)
	if !slices.Equal(started, []int{0, 1}) {
		t.Fatalf("started = %v", ui.started)
	}
	if got := recorder.events[len(recorder.events)-1]; got != "results" {
		t.Fatalf("AfterExecute not last: %v", recorder.events)
	}
	// Each started call is reported finished when it ends, ahead of the
	// results that wait for earlier ones.
	if !slices.Equal(ui.finished, []int{1, 0}) {
		t.Fatalf("finished order = %v, want the fast call first", ui.finished)
	}
}

// Ctrl-C at the first question cancels the round. Nothing ran, so the round
// prints nothing more; the turn reports what happened once.
func TestExecuteParallelStaysQuietWhenCancelledBeforeAnythingRan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	approver := &roundApprover{cancel: cancel}
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &lifecycleRecorder{}
	results := executor.ExecuteParallel(ctx, []ToolCall{roundCall("one", "echo", "one"), roundCall("two", "echo", "two")}, &recordingToolUI{recorder: recorder})
	for _, result := range results {
		if !result.NotRun || result.Code != errors.ErrCodeCancelled {
			t.Fatalf("result = %#v", result)
		}
	}
	if want := []string{"show:1/2:one", "show:2/2:two"}; !slices.Equal(recorder.events, want) {
		t.Fatalf("events = %#v, want %#v", recorder.events, want)
	}
}

// Ctrl-C at a later question cancels the round, the calls already approved
// included: nothing runs, no question follows, and nothing more is printed.
func TestExecuteParallelRunsNothingWhenCancelledAtALaterQuestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	approver := &roundApprover{answers: []Approval{{Approved: true}}, cancel: cancel, cancelAfter: 1}
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &lifecycleRecorder{}
	ui := &observingToolUI{recordingToolUI: recordingToolUI{recorder: recorder}}
	calls := []ToolCall{roundCall("one", "echo", "one"), roundCall("two", "echo", "two"), roundCall("three", "echo", "three")}
	results := executor.ExecuteParallel(ctx, calls, ui)
	for _, result := range results {
		if !result.NotRun || result.Code != errors.ErrCodeCancelled {
			t.Fatalf("result = %#v", result)
		}
	}
	if approver.questions != 2 {
		t.Fatalf("%d questions, want none after the cancelled one", approver.questions)
	}
	if want := []string{"show:1/3:one", "show:2/3:two", "show:3/3:three"}; !slices.Equal(recorder.events, want) {
		t.Fatalf("events = %#v, want %#v", recorder.events, want)
	}
	if len(ui.started) != 0 || len(ui.ready) != 0 {
		t.Fatalf("observer heard started %v, ready %v", ui.started, ui.ready)
	}
}

// A round that asks questions notes the calls that will not run under their
// review; a round that asks nothing reports them once, with the results.
func TestExecuteParallelNotesRefusalsOnlyWhenItAsks(t *testing.T) {
	approver := &roundApprover{answers: []Approval{{Approved: false}}}
	blacklist := writeRuleFile(t, "blacklist.txt", `["rm"]`)
	executor, err := NewExecutor(RequestOptions{ToolTimeout: 5 * time.Second, ToolBlacklist: blacklist}, NewTestLogger(false), approver)
	if err != nil {
		t.Fatal(err)
	}
	ui := &noteRecordingUI{}
	executor.ExecuteParallel(context.Background(), []ToolCall{roundCall("denied", "rm", "x"), roundCall("asked", "echo", "x")}, ui)
	if want := []string{"0: Will not run: blacklisted"}; !slices.Equal(ui.notes, want) {
		t.Fatalf("notes = %q, want %q", ui.notes, want)
	}
	ui = &noteRecordingUI{}
	executor.ExecuteParallel(context.Background(), []ToolCall{roundCall("denied", "rm", "x")}, ui)
	if len(ui.notes) != 0 {
		t.Fatalf("a round without questions noted refusals: %q", ui.notes)
	}
}
