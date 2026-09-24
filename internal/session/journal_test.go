//go:build !windows

package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	journalOwnerHelperEnv   = "LMC_TEST_JOURNAL_OWNER"
	journalOwnerSessionsEnv = "LMC_TEST_JOURNAL_SESSIONS"
	sessionLockOwnerEnv     = "LMC_TEST_SESSION_LOCK_OWNER"
)

// TestMain lets the test binary play a run that owns a call or holds a
// session's lock, so a test can kill that run the way a crash or a closed
// terminal would.
func TestMain(m *testing.M) {
	if invocation := os.Getenv(journalOwnerHelperEnv); invocation != "" {
		os.Exit(runJournalOwner(invocation))
	}
	if sessionPath := os.Getenv(sessionLockOwnerEnv); sessionPath != "" {
		os.Exit(runSessionLockOwner(sessionPath))
	}
	os.Exit(m.Run())
}

// startOwner starts the test binary as an owner run with env and returns the
// process ID of the command the owner reports it started. Both are killed
// when the test ends.
func startOwner(t *testing.T, env ...string) (*exec.Cmd, int) {
	t.Helper()
	owner := exec.Command(os.Args[0], "-test.run=^$")
	owner.Env = append(os.Environ(), env...)
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	if err := owner.Start(); err != nil {
		t.Fatalf("start owner: %v", err)
	}
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		_ = owner.Wait()
	})

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var commandPID int
	select {
	case line := <-lines:
		if _, err := fmt.Sscanf(line, "ready %d", &commandPID); err != nil {
			t.Fatalf("owner said %q, want its command's process ID", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("owner did not report ready")
	}
	t.Cleanup(func() { _ = syscall.Kill(commandPID, syscall.SIGKILL) })
	return owner, commandPID
}

// killOwner kills an owner run and checks that the command it started still
// runs, which the tests that use it need.
func killOwner(t *testing.T, owner *exec.Cmd, commandPID int) {
	t.Helper()
	if err := owner.Process.Kill(); err != nil {
		t.Fatalf("kill owner: %v", err)
	}
	_ = owner.Wait()
	if err := syscall.Kill(commandPID, 0); err != nil {
		t.Fatalf("the owner's command is gone (%v); the test needs it running", err)
	}
}

// runJournalOwner claims a call, records its start, starts a command that
// outlives it, reports the command's process ID, and waits to be killed.
func runJournalOwner(invocation string) int {
	SetSessionsDir(os.Getenv(journalOwnerSessionsEnv))
	call := core.ToolCall{ID: "call_owned", Name: "universal_command", InvocationID: invocation}
	claim, err := NewToolJournal().Claim([]core.ToolCall{call})
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	if err := claim.Started(call); err != nil {
		fmt.Println("error", err)
		return 1
	}
	command := exec.Command("sleep", "60")
	if err := command.Start(); err != nil {
		fmt.Println("error", err)
		return 1
	}
	fmt.Printf("ready %d\n", command.Process.Pid)
	time.Sleep(time.Hour)
	return 0
}

// recordingNotifier keeps formatted messages and is safe to read while
// another goroutine writes.
type recordingNotifier struct {
	mu       sync.Mutex
	messages []string
}

func (n *recordingNotifier) record(format string, args ...interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, fmt.Sprintf(format, args...))
}

func (n *recordingNotifier) Infof(format string, args ...interface{}) { n.record(format, args...) }

func (n *recordingNotifier) Warnf(format string, args ...interface{}) { n.record(format, args...) }

func (n *recordingNotifier) Errorf(format string, args ...interface{}) { n.record(format, args...) }

func (n *recordingNotifier) Promptf(format string, args ...interface{}) { n.record(format, args...) }

func (n *recordingNotifier) has(substring string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, message := range n.messages {
		if strings.Contains(message, substring) {
			return true
		}
	}
	return false
}

func touchCall(id, invocation, marker string) core.ToolCall {
	return core.ToolCall{
		ID:           id,
		Name:         "universal_command",
		Args:         json.RawMessage(fmt.Sprintf(`{"command":["touch",%q]}`, marker)),
		InvocationID: invocation,
	}
}

// countingCall appends a line to counter each time it runs and prints "ran".
func countingCall(id, invocation, counter string) core.ToolCall {
	return core.ToolCall{
		ID:           id,
		Name:         "universal_command",
		Args:         json.RawMessage(fmt.Sprintf(`{"command":["sh","-c","echo run >> \"$1\"; echo ran","sh",%q]}`, counter)),
		InvocationID: invocation,
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return strings.Count(string(data), "\n")
}

func resolveForTest(t *testing.T, ctx context.Context, sess *Session, cfg core.RequestOptions, notifier core.Notifier, approver core.Approver) PendingResolution {
	t.Helper()
	resolution, err := ResolvePendingToolCalls(ctx, sess, cfg, core.NewTestLogger(false), notifier, core.TestToolUI{}, approver)
	if err != nil {
		t.Fatalf("ResolvePendingToolCalls() error = %v", err)
	}
	return resolution
}

func TestJournalRecordUpdatesKeepTheLock(t *testing.T) {
	UseTestSessionDir(t)
	call := echoCall("call_1", core.NewInvocationID(), "hi")
	claim, err := NewToolJournal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	defer claim.Release()
	if err := claim.Started(call); err != nil {
		t.Fatalf("Started() error = %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := claim.Finished(call, core.ToolResult{ID: "call_1", Output: "hi"}); err != nil {
			t.Fatalf("Finished() error = %v", err)
		}
	}

	file, ok, err := lockEntry(openJournal(nil).entryDir(call.InvocationID))
	if err != nil {
		t.Fatalf("lockEntry() error = %v", err)
	}
	if ok {
		_ = file.Close()
		t.Fatal("a second open took the ownership lock after the records were replaced")
	}
}

func TestJournalOutcomeRecoversImages(t *testing.T) {
	UseTestSessionDir(t)
	call := core.ToolCall{ID: "call_img", Name: core.ViewImageToolName, InvocationID: core.NewInvocationID()}
	claim, err := NewToolJournal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	picture := core.ImageBlock{URL: "data:image/png;base64,iVBORw0KGgo=", Name: "shot.png"}
	if err := claim.Finished(call, core.ToolResult{ID: "call_img", Output: "image", Images: []core.ImageBlock{picture}}); err != nil {
		t.Fatalf("Finished() error = %v", err)
	}
	claim.Release()

	state, result, err := openJournal(nil).state(call.InvocationID)
	if err != nil {
		t.Fatalf("state() error = %v", err)
	}
	if state != invocationKnown || len(result.Images) != 1 || result.Images[0] != picture {
		t.Fatalf("state = %v, images = %+v, want the recorded outcome with its image", state, result.Images)
	}
}

// A call claimed while pending is copied into a fork, and a stale session
// value still expects the old head. Resolving the original runs the call;
// every later recovery, in the fork or through the stale value, reads the
// recorded outcome and runs nothing.
func TestResolvePendingToolCallsRunsACallOnceAcrossCopies(t *testing.T) {
	UseTestSessionDir(t)
	ctx := context.Background()
	counter := filepath.Join(t.TempDir(), "runs")
	call := countingCall("call_1", core.NewInvocationID(), counter)
	claimedNeverStarted(t, []core.ToolCall{call})
	original := pendingCallSession(t, []core.ToolCall{call})

	head, _, err := LastMessageRefWithManager(DefaultManager(), original.Path)
	if err != nil {
		t.Fatalf("LastMessageRefWithManager() error = %v", err)
	}
	fork, err := ForkThroughHead(ctx, DefaultManager(), original.Path, head)
	if err != nil {
		t.Fatalf("ForkThroughHead() error = %v", err)
	}
	stale := &Session{Path: original.Path, Head: &head}

	cfg := pendingToolTestConfig()
	for name, sess := range map[string]*Session{"original": original} {
		if resolution := resolveForTest(t, ctx, sess, cfg, core.NewTestNotifier(), &MockApprover{shouldApprove: true}); !resolution.Committed {
			t.Fatalf("%s: resolution = %+v, want committed", name, resolution)
		}
	}
	for name, sess := range map[string]*Session{"fork": fork, "stale": stale} {
		notifier := &recordingNotifier{}
		if resolution := resolveForTest(t, ctx, sess, cfg, notifier, &MockApprover{shouldApprove: true}); !resolution.Committed {
			t.Fatalf("%s: resolution = %+v, want committed", name, resolution)
		}
		if !notifier.has("already ran") {
			t.Fatalf("%s: notes = %q, want the recorded outcome reported", name, notifier.messages)
		}
		if results := lastToolResults(t, sess); len(results) != 1 || !strings.Contains(results[0].Output, "ran") {
			t.Fatalf("%s: results = %+v, want the recorded outcome", name, results)
		}
	}
	if got := lineCount(t, counter); got != 1 {
		t.Fatalf("the call ran %d times, want once", got)
	}
}

// A live owner holds a call: a recovery waits, then uses the outcome the
// owner recorded, on its own head, without running the call.
func TestResolvePendingToolCallsWaitsForALiveOwner(t *testing.T) {
	UseTestSessionDir(t)
	marker := filepath.Join(t.TempDir(), "ran")
	call := touchCall("call_1", core.NewInvocationID(), marker)
	owner, err := NewToolJournal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	sess := pendingCallSession(t, []core.ToolCall{call})

	notifier := &recordingNotifier{}
	done := make(chan error, 1)
	go func() {
		_, err := ResolvePendingToolCalls(context.Background(), sess, pendingToolTestConfig(), core.NewTestLogger(false), notifier, core.TestToolUI{}, &MockApprover{shouldApprove: true})
		done <- err
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !notifier.has(fmt.Sprintf("Waiting for process %d", os.Getpid())) {
		if time.Now().After(deadline) {
			t.Fatalf("no waiting note; notes = %q", notifier.messages)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := owner.Started(call); err != nil {
		t.Fatalf("Started() error = %v", err)
	}
	if err := owner.Finished(call, core.ToolResult{ID: "call_1", Output: "the owner ran it"}); err != nil {
		t.Fatalf("Finished() error = %v", err)
	}
	owner.Release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ResolvePendingToolCalls() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not finish after the owner released the call")
	}
	if exists(marker) {
		t.Fatal("recovery ran a call the owner had already run")
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Output != "the owner ran it" {
		t.Fatalf("results = %+v, want the owner's recorded outcome", results)
	}
}

func TestResolvePendingToolCallsWaitIsCancellable(t *testing.T) {
	UseTestSessionDir(t)
	call := touchCall("call_1", core.NewInvocationID(), filepath.Join(t.TempDir(), "ran"))
	owner, err := NewToolJournal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	defer owner.Release()
	sess := pendingCallSession(t, []core.ToolCall{call})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	resolution, err := ResolvePendingToolCalls(ctx, sess, pendingToolTestConfig(), core.NewTestLogger(false), &recordingNotifier{}, core.TestToolUI{}, &MockApprover{shouldApprove: true})
	if !errors.Is(err, context.DeadlineExceeded) || resolution.Committed {
		t.Fatalf("ResolvePendingToolCalls() = %+v, %v; want the wait cancelled with nothing committed", resolution, err)
	}
	if pending, err := CheckForPendingToolCalls(context.Background(), sess.Path); err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v (err %v), want the call still pending", pending, err)
	}
}

// A call started without an outcome may already have run. Only an operator
// can decide to run it again; policy cannot, and neither can a run that
// cannot prompt.
func TestResolvePendingToolCallsAsksBeforeRunningAStartedCallAgain(t *testing.T) {
	tests := []struct {
		name           string
		answer         bool
		nonInteractive bool
		wantAsked      bool
		wantRun        bool
	}{
		{name: "operator declines", answer: false, wantAsked: true},
		{name: "operator approves", answer: true, wantAsked: true, wantRun: true},
		{name: "cannot prompt", answer: true, nonInteractive: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			UseTestSessionDir(t)
			marker := filepath.Join(t.TempDir(), "ran")
			call := touchCall("call_1", core.NewInvocationID(), marker)
			claim, err := NewToolJournal().Claim([]core.ToolCall{call})
			if err != nil {
				t.Fatalf("Claim() error = %v", err)
			}
			if err := claim.Started(call); err != nil {
				t.Fatalf("Started() error = %v", err)
			}
			claim.Release()
			sess := pendingCallSession(t, []core.ToolCall{call})

			cfg := pendingToolTestConfig()
			cfg.ToolAutoApprove = true
			cfg.ToolNonInteractive = tt.nonInteractive
			approver := core.NewTestApprover(true)
			approver.RerunResponses = []bool{tt.answer}
			resolveForTest(t, context.Background(), sess, cfg, core.NewTestNotifier(), approver)

			if asked := len(approver.RerunCalls) == 1; asked != tt.wantAsked {
				t.Fatalf("asked = %v, want %v", asked, tt.wantAsked)
			}
			if exists(marker) != tt.wantRun {
				t.Fatalf("ran = %v, want %v", exists(marker), tt.wantRun)
			}
			results := lastToolResults(t, sess)
			if !tt.wantRun && (len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown) {
				t.Fatalf("results = %+v, want an unknown outcome", results)
			}
		})
	}
}

// An owner died partway through a batch: the call with a recorded outcome
// is not run again, and the call that never started runs.
func TestResolvePendingToolCallsRunsOnlyTheUnstartedCallsOfABatch(t *testing.T) {
	UseTestSessionDir(t)
	dir := t.TempDir()
	finished := touchCall("call_a", core.NewInvocationID(), filepath.Join(dir, "a"))
	unstarted := touchCall("call_b", core.NewInvocationID(), filepath.Join(dir, "b"))
	claim, err := NewToolJournal().Claim([]core.ToolCall{finished, unstarted})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := claim.Started(finished); err != nil {
		t.Fatalf("Started() error = %v", err)
	}
	if err := claim.Finished(finished, core.ToolResult{ID: "call_a", Output: "a finished"}); err != nil {
		t.Fatalf("Finished() error = %v", err)
	}
	claim.Release()
	sess := pendingCallSession(t, []core.ToolCall{finished, unstarted})

	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), &MockApprover{shouldApprove: true})

	if exists(filepath.Join(dir, "a")) {
		t.Fatal("the call with a recorded outcome ran again")
	}
	if !exists(filepath.Join(dir, "b")) {
		t.Fatal("the call that never started did not run")
	}
	if results := lastToolResults(t, sess); len(results) != 2 || results[0].Output != "a finished" {
		t.Fatalf("results = %+v, want the recorded outcome first", results)
	}
}

func TestResolvePendingToolCallsTreatsAMissingEntryAsUncertain(t *testing.T) {
	UseTestSessionDir(t)
	marker := filepath.Join(t.TempDir(), "ran")
	sess := pendingCallSession(t, []core.ToolCall{touchCall("call_1", core.NewInvocationID(), marker)})

	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), &MockApprover{shouldApprove: true})

	if exists(marker) {
		t.Fatal("a call with no journal entry ran without a decision")
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown {
		t.Fatalf("results = %+v, want an unknown outcome", results)
	}
}

// view_image reads a file and has no effects, so a started view_image call
// runs again without the question an uncertain command raises.
func TestResolvePendingViewImageRunsAgainAfterAStart(t *testing.T) {
	UseTestSessionDir(t)
	picture := filepath.Join(t.TempDir(), "shot.png")
	file, err := os.Create(picture)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	_ = file.Close()

	call := core.ToolCall{
		ID:           "call_img",
		Name:         core.ViewImageToolName,
		Args:         json.RawMessage(fmt.Sprintf(`{"path":%q}`, picture)),
		InvocationID: core.NewInvocationID(),
	}
	claim, err := NewToolJournal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := claim.Started(call); err != nil {
		t.Fatalf("Started() error = %v", err)
	}
	claim.Release()
	sess := pendingCallSession(t, []core.ToolCall{call})

	approver := core.NewTestApprover(true)
	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), approver)

	if len(approver.RerunCalls) != 0 {
		t.Fatalf("asked about rerunning view_image %d times, want never", len(approver.RerunCalls))
	}
	if len(approver.ImageApprovalCalls) != 1 {
		t.Fatalf("image approvals = %d, want the call run again", len(approver.ImageApprovalCalls))
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Error != "" {
		t.Fatalf("results = %+v, want the image read again", results)
	}
}

// Killing the owner ends its ownership, even though a command it started is
// still running: the lock descriptor is close-on-exec, so the command never
// held it. The call started and recorded no outcome, so recovery finds it
// uncertain and does not run it without a decision.
func TestJournalOwnershipEndsWithTheOwnerAndNotItsCommands(t *testing.T) {
	sessionsDir := UseTestSessionDir(t)
	invocation := core.NewInvocationID()
	owner, commandPID := startOwner(t, journalOwnerHelperEnv+"="+invocation, journalOwnerSessionsEnv+"="+sessionsDir)

	dir := openJournal(nil).entryDir(invocation)
	if file, ok, err := lockEntry(dir); err != nil || ok {
		if ok {
			_ = file.Close()
		}
		t.Fatalf("lockEntry() while the owner lives = %v, %v; want the lock held", ok, err)
	}

	killOwner(t, owner, commandPID)
	file, ok, err := lockEntry(dir)
	if err != nil || !ok {
		t.Fatalf("lockEntry() after the owner died = %v, %v; want the lock free while its command still runs", ok, err)
	}
	_ = file.Close()

	marker := filepath.Join(t.TempDir(), "ran")
	call := touchCall("call_owned", invocation, marker)
	sess := pendingCallSession(t, []core.ToolCall{call})
	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), &MockApprover{shouldApprove: true})
	if exists(marker) {
		t.Fatal("recovery ran a call that started without an outcome")
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown {
		t.Fatalf("results = %+v, want an unknown outcome", results)
	}
}
