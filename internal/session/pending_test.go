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
	journalOwnerEnv         = "LMC_TEST_JOURNAL_OWNER"
	journalOwnerSessionsEnv = "LMC_TEST_JOURNAL_SESSIONS"
)

// runJournalOwner claims a call, records its start, starts a command that
// outlives it, reports the command's process ID, and waits to be killed.
func runJournalOwner(invocation string) int {
	call := core.ToolCall{ID: "call_owned", Name: "universal_command", InvocationID: invocation}
	claim, err := Open(os.Getenv(journalOwnerSessionsEnv)).Journal().Claim([]core.ToolCall{call})
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
		ID: id, Name: "universal_command", InvocationID: invocation,
		Args: json.RawMessage(fmt.Sprintf(`{"command":["touch",%q]}`, marker)),
	}
}

// countingCall appends a line to counter each time it runs and prints "ran".
func countingCall(id, invocation, counter string) core.ToolCall {
	return core.ToolCall{
		ID: id, Name: "universal_command", InvocationID: invocation,
		Args: json.RawMessage(fmt.Sprintf(`{"command":["sh","-c","echo run >> \"$1\"; echo ran","sh",%q]}`, counter)),
	}
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func pendingToolTestConfig() core.RequestOptions {
	cfg := core.NewTestRequestConfig()
	cfg.ToolEnabled = true
	cfg.ToolTimeout = 5 * time.Second
	return cfg
}

// callEntry is a reply that makes calls.
func callEntry(calls ...core.ToolCall) Entry {
	return Entry{Role: core.RoleAssistant, Model: "test-model", Content: Content{
		Blocks: core.ReplyBlocks(core.Response{Text: "running", ToolCalls: calls}),
		Tools:  &core.ToolInteraction{Calls: calls},
	}}
}

// pendingCallSession saves a question and a reply whose calls are pending,
// after the entries given, and returns the session at the reply.
func pendingCallSession(t *testing.T, s *Store, calls []core.ToolCall, before ...Entry) *Session {
	t.Helper()
	info := create(t, s)
	sess := &Session{Store: s, Number: info.Number, Identity: info.Identity}
	entries := append(append([]Entry(nil), before...), text("run it"), callEntry(calls...))
	if _, err := sess.Append(context.Background(), entries...); err != nil {
		t.Fatal(err)
	}
	return sess
}

func claimedNeverStarted(t *testing.T, s *Store, calls []core.ToolCall) {
	t.Helper()
	claim, err := s.Journal().Claim(calls)
	if err != nil {
		t.Fatal(err)
	}
	claim.Release()
}

func lastToolResults(t *testing.T, sess *Session) []core.ToolResult {
	t.Helper()
	h, err := sess.History()
	if err != nil {
		t.Fatal(err)
	}
	tools := h.Contents[len(h.Contents)-1].Tools
	if tools == nil {
		t.Fatalf("message %s carries no tool results", sess.Head)
	}
	return tools.Results
}

func resolveForTest(t *testing.T, ctx context.Context, sess *Session, cfg core.RequestOptions, notifier core.Notifier, approver core.Approver) PendingResolution {
	t.Helper()
	resolution, err := sess.ResolvePending(ctx, cfg, core.NewTestLogger(false), notifier, core.TestToolUI{}, approver, nil)
	if err != nil {
		t.Fatalf("ResolvePending() error = %v", err)
	}
	return resolution
}

func TestJournalRecordUpdatesKeepTheLock(t *testing.T) {
	s := newStore(t)
	call := touchCall("call_1", core.NewInvocationID(), filepath.Join(t.TempDir(), "x"))
	claim, err := s.Journal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Release()
	if err := claim.Started(call); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := claim.Finished(call, core.ToolResult{ID: "call_1", Output: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	file, ok, err := lockEntry(openJournal(s).entryDir(call.InvocationID))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		_ = file.Close()
		t.Fatal("a second open took the ownership lock after the records were replaced")
	}
}

func TestJournalOutcomeRecoversImages(t *testing.T) {
	s := newStore(t)
	call := core.ToolCall{ID: "call_img", Name: core.ViewImageToolName, InvocationID: core.NewInvocationID()}
	claim, err := s.Journal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	picture := core.ImageBlock{URL: "data:image/png;base64,iVBORw0KGgo=", Name: "shot.png"}
	if err := claim.Finished(call, core.ToolResult{ID: "call_img", Output: "image", Images: []core.ImageBlock{picture}}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	state, result, err := openJournal(s).state(call.InvocationID)
	if err != nil || state != invocationKnown || len(result.Images) != 1 || result.Images[0] != picture {
		t.Fatalf("state %v, images %+v: %v", state, result.Images, err)
	}
}

// Two runs resolve one pending call on two branches: the first runs it, and
// the second, writing an alternative after the same reply, sends the
// recorded outcome and runs nothing.
func TestResolvePendingRunsACallOnceAcrossBranches(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	counter := filepath.Join(t.TempDir(), "runs")
	call := countingCall("call_1", core.NewInvocationID(), counter)
	claimedNeverStarted(t, s, []core.ToolCall{call})
	first := pendingCallSession(t, s, []core.ToolCall{call})
	second := *first
	cfg := pendingToolTestConfig()
	if resolution := resolveForTest(t, ctx, first, cfg, core.NewTestNotifier(), core.NewTestApprover(true)); !resolution.Committed {
		t.Fatalf("resolution %+v", resolution)
	}
	notifier := &recordingNotifier{}
	if resolution := resolveForTest(t, ctx, &second, cfg, notifier, core.NewTestApprover(true)); !resolution.Committed {
		t.Fatalf("resolution %+v", resolution)
	}
	if !notifier.has("already ran") {
		t.Fatalf("notes %q", notifier.messages)
	}
	if first.Head == second.Head {
		t.Fatal("the two runs saved one message")
	}
	if results := lastToolResults(t, &second); len(results) != 1 || !strings.Contains(results[0].Output, "ran") {
		t.Fatalf("results %+v", results)
	}
	if got := lineCount(t, counter); got != 1 {
		t.Fatalf("the call ran %d times, want once", got)
	}
}

// A live owner holds a call: a recovery waits, then uses the outcome the
// owner recorded without running the call.
func TestResolvePendingWaitsForALiveOwner(t *testing.T) {
	s := newStore(t)
	marker := filepath.Join(t.TempDir(), "ran")
	call := touchCall("call_1", core.NewInvocationID(), marker)
	owner, err := s.Journal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	sess := pendingCallSession(t, s, []core.ToolCall{call})
	notifier := &recordingNotifier{}
	done := make(chan error, 1)
	go func() {
		_, err := sess.ResolvePending(context.Background(), pendingToolTestConfig(), core.NewTestLogger(false), notifier, core.TestToolUI{}, core.NewTestApprover(true), nil)
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !notifier.has(fmt.Sprintf("Waiting for process %d", os.Getpid())) {
		if time.Now().After(deadline) {
			t.Fatalf("no waiting note; notes %q", notifier.messages)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := owner.Started(call); err != nil {
		t.Fatal(err)
	}
	if err := owner.Finished(call, core.ToolResult{ID: "call_1", Output: "the owner ran it"}); err != nil {
		t.Fatal(err)
	}
	owner.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not finish after the owner released the call")
	}
	if exists(marker) {
		t.Fatal("recovery ran a call the owner had already run")
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Output != "the owner ran it" {
		t.Fatalf("results %+v", results)
	}
}

func TestResolvePendingWaitIsCancellable(t *testing.T) {
	s := newStore(t)
	call := touchCall("call_1", core.NewInvocationID(), filepath.Join(t.TempDir(), "ran"))
	owner, err := s.Journal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	sess := pendingCallSession(t, s, []core.ToolCall{call})
	head := sess.Head
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	resolution, err := sess.ResolvePending(ctx, pendingToolTestConfig(), core.NewTestLogger(false), &recordingNotifier{}, core.TestToolUI{}, core.NewTestApprover(true), nil)
	if !errors.Is(err, context.DeadlineExceeded) || resolution.Committed || sess.Head != head {
		t.Fatalf("resolution %+v: %v", resolution, err)
	}
}

// A call started without an outcome may already have run. Only an operator
// can decide to run it again.
func TestResolvePendingAsksBeforeRunningAStartedCallAgain(t *testing.T) {
	for _, tt := range []struct {
		name                                  string
		answer, nonInteractive, asked, wanted bool
	}{
		{name: "operator declines", asked: true},
		{name: "operator approves", answer: true, asked: true, wanted: true},
		{name: "cannot prompt", answer: true, nonInteractive: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			marker := filepath.Join(t.TempDir(), "ran")
			call := touchCall("call_1", core.NewInvocationID(), marker)
			claim, err := s.Journal().Claim([]core.ToolCall{call})
			if err != nil {
				t.Fatal(err)
			}
			if err := claim.Started(call); err != nil {
				t.Fatal(err)
			}
			claim.Release()
			sess := pendingCallSession(t, s, []core.ToolCall{call})
			cfg := pendingToolTestConfig()
			cfg.ToolAutoApprove = true
			cfg.ToolNonInteractive = tt.nonInteractive
			approver := core.NewTestApprover(true)
			approver.RerunResponses = []bool{tt.answer}
			resolveForTest(t, context.Background(), sess, cfg, core.NewTestNotifier(), approver)
			if asked := len(approver.RerunCalls) == 1; asked != tt.asked {
				t.Fatalf("asked %v, want %v", asked, tt.asked)
			}
			if exists(marker) != tt.wanted {
				t.Fatalf("ran %v, want %v", exists(marker), tt.wanted)
			}
			if results := lastToolResults(t, sess); !tt.wanted && (len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown) {
				t.Fatalf("results %+v", results)
			}
		})
	}
}

// An owner died partway through a batch: the call with a recorded outcome is
// not run again, and the call that never started runs.
func TestResolvePendingRunsOnlyTheUnstartedCallsOfABatch(t *testing.T) {
	s := newStore(t)
	dir := t.TempDir()
	finished := touchCall("call_a", core.NewInvocationID(), filepath.Join(dir, "a"))
	unstarted := touchCall("call_b", core.NewInvocationID(), filepath.Join(dir, "b"))
	claim, err := s.Journal().Claim([]core.ToolCall{finished, unstarted})
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Started(finished); err != nil {
		t.Fatal(err)
	}
	if err := claim.Finished(finished, core.ToolResult{ID: "call_a", Output: "a finished"}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	sess := pendingCallSession(t, s, []core.ToolCall{finished, unstarted})
	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), core.NewTestApprover(true))
	if exists(filepath.Join(dir, "a")) || !exists(filepath.Join(dir, "b")) {
		t.Fatal("the batch ran the wrong calls")
	}
	if results := lastToolResults(t, sess); len(results) != 2 || results[0].Output != "a finished" {
		t.Fatalf("results %+v", results)
	}
}

func TestResolvePendingTreatsAMissingEntryAsUncertain(t *testing.T) {
	s := newStore(t)
	marker := filepath.Join(t.TempDir(), "ran")
	sess := pendingCallSession(t, s, []core.ToolCall{touchCall("call_1", core.NewInvocationID(), marker)})
	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), core.NewTestApprover(true))
	if exists(marker) {
		t.Fatal("a call with no journal entry ran without a decision")
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown {
		t.Fatalf("results %+v", results)
	}
}

// view_image reads a file and has no effects, so a started view_image call
// runs again without the question an uncertain command raises.
func TestResolvePendingViewImageRunsAgainAfterAStart(t *testing.T) {
	s := newStore(t)
	picture := filepath.Join(t.TempDir(), "shot.png")
	file, err := os.Create(picture)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	call := core.ToolCall{ID: "call_img", Name: core.ViewImageToolName, Args: json.RawMessage(fmt.Sprintf(`{"path":%q}`, picture)), InvocationID: core.NewInvocationID()}
	claim, err := s.Journal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Started(call); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	sess := pendingCallSession(t, s, []core.ToolCall{call})
	approver := core.NewTestApprover(true)
	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), approver)
	if len(approver.RerunCalls) != 0 || len(approver.ImageApprovalCalls) != 1 {
		t.Fatalf("reruns asked %d, images approved %d", len(approver.RerunCalls), len(approver.ImageApprovalCalls))
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Error != "" {
		t.Fatalf("results %+v", results)
	}
}

// A pending notebook call keeps the notebook it was made for. Under another
// notebook it stays pending and the error names its notebook, unless the
// journal knows its outcome, which is sent in any case.
func TestPendingNotebookCallKeepsItsNotebook(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	call := core.ToolCall{ID: "call_put", Name: "mcp__notebook__put", MCPServer: "notebook", MCPTool: "put", Args: json.RawMessage(`{}`), InvocationID: core.NewInvocationID()}
	claimedNeverStarted(t, s, []core.ToolCall{call})
	settings := Settings{Notebook: "/notes/a"}
	sess := pendingCallSession(t, s, []core.ToolCall{call}, Entry{Role: core.RoleSystem, Settings: &settings})
	head := sess.Head
	cfg := pendingToolTestConfig()
	cfg.Context.Notebook = "/notes/b"
	_, err := sess.ResolvePending(ctx, cfg, core.NewTestLogger(false), core.NewTestNotifier(), core.TestToolUI{}, core.NewTestApprover(true), nil)
	if err == nil || !strings.Contains(err.Error(), `"/notes/a"`) || sess.Head != head {
		t.Fatalf("resolving under another notebook: %v", err)
	}
	detached := pendingToolTestConfig()
	detached.ToolEnabled = false
	if _, err := sess.ResolvePending(ctx, detached, core.NewTestLogger(false), core.NewTestNotifier(), core.TestToolUI{}, core.NewTestApprover(true), nil); err == nil || !strings.Contains(err.Error(), `"/notes/a"`) || sess.Head != head {
		t.Fatalf("resolving with the notebook detached: %v", err)
	}
	claim, err := s.Journal().Claim([]core.ToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.Finished(call, core.ToolResult{ID: "call_put", Output: "saved"}); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	resolveForTest(t, ctx, sess, cfg, core.NewTestNotifier(), core.NewTestApprover(true))
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Output != "saved" {
		t.Fatalf("results %+v", results)
	}
}

// Killing the owner ends its ownership, even though a command it started is
// still running. The call started and recorded no outcome, so recovery finds
// it uncertain and does not run it without a decision.
func TestJournalOwnershipEndsWithTheOwnerAndNotItsCommands(t *testing.T) {
	s := newStore(t)
	invocation := core.NewInvocationID()
	owner := exec.Command(os.Args[0], "-test.run=^$")
	owner.Env = append(os.Environ(), journalOwnerEnv+"="+invocation, journalOwnerSessionsEnv+"="+s.Dir())
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		_ = owner.Wait()
	})
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	var commandPID int
	if _, err := fmt.Sscanf(line, "ready %d", &commandPID); err != nil {
		t.Fatalf("owner said %q", line)
	}
	t.Cleanup(func() { _ = syscall.Kill(commandPID, syscall.SIGKILL) })

	dir := openJournal(s).entryDir(invocation)
	if file, ok, err := lockEntry(dir); err != nil || ok {
		if ok {
			_ = file.Close()
		}
		t.Fatalf("lock while the owner lives: %v, %v", ok, err)
	}
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	if err := syscall.Kill(commandPID, 0); err != nil {
		t.Fatalf("the owner's command is gone (%v); the test needs it running", err)
	}
	file, ok, err := lockEntry(dir)
	if err != nil || !ok {
		t.Fatalf("lock after the owner died: %v, %v", ok, err)
	}
	_ = file.Close()

	marker := filepath.Join(t.TempDir(), "ran")
	sess := pendingCallSession(t, s, []core.ToolCall{touchCall("call_owned", invocation, marker)})
	resolveForTest(t, context.Background(), sess, pendingToolTestConfig(), core.NewTestNotifier(), core.NewTestApprover(true))
	if exists(marker) {
		t.Fatal("recovery ran a call that started without an outcome")
	}
	if results := lastToolResults(t, sess); len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown {
		t.Fatalf("results %+v", results)
	}
}
