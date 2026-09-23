//go:build !windows

package session

import (
	"context"
	"errors"
	"fmt"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventLog records, in order, what a tool UI and an approver were asked.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

type rerunReviewUI struct {
	core.TestToolUI
	log *eventLog
}

func (ui rerunReviewUI) ShowRerun(call core.ToolCall, reason string) {
	ui.log.add("review " + call.ID + " " + string(call.Args) + ": " + reason)
}

type rerunApprover struct {
	core.DeclineNonCommandApprovals
	log *eventLog
}

func (a rerunApprover) Approve(context.Context, core.UniversalCommandArgs) (bool, error) {
	return true, nil
}

func (a rerunApprover) ApproveRerun(_ context.Context, call core.ToolCall) (bool, error) {
	a.log.add("question " + call.ID)
	return false, nil
}

// The call an operator is asked about is shown on the tool UI, the surface
// the question uses, right before the question; the ordinary notifier, which
// may be a redirected stderr, never carries its arguments.
func TestRerunReviewIsShownWhereTheQuestionIsAsked(t *testing.T) {
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

	log := &eventLog{}
	notifier := &recordingNotifier{}
	_, err = ResolvePendingToolCalls(context.Background(), sess, pendingToolTestConfig(), core.NewTestLogger(false), notifier, rerunReviewUI{log: log}, rerunApprover{log: log})
	if err != nil {
		t.Fatalf("ResolvePendingToolCalls() error = %v", err)
	}

	if len(log.events) != 2 || !strings.HasPrefix(log.events[0], "review call_1 ") || !strings.Contains(log.events[0], marker) || log.events[1] != "question call_1" {
		t.Fatalf("events = %q, want the call and its arguments reviewed, then the question", log.events)
	}
	if notifier.has(marker) {
		t.Fatalf("notes = %q, want the arguments kept off the ordinary notifier", notifier.messages)
	}
}

// failOutcomeWriteApprover approves a call and, doing so, makes the outcome
// record of its invocation unwritable: a directory where the file goes.
type failOutcomeWriteApprover struct {
	core.DeclineNonCommandApprovals
	entryDir string
}

func (a failOutcomeWriteApprover) Approve(context.Context, core.UniversalCommandArgs) (bool, error) {
	return true, os.MkdirAll(filepath.Join(a.entryDir, journalOutcomeName), 0o700)
}

// An outcome the journal could not record does not stop the results from
// being committed, and the failure reaches the caller. A copy of the
// transcript made while the call was pending shows why it has to: that copy
// can only find the call uncertain.
func TestRecoveryReportsAnOutcomeItCouldNotRecord(t *testing.T) {
	UseTestSessionDir(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "ran")
	call := touchCall("call_1", core.NewInvocationID(), marker)
	claimedNeverStarted(t, []core.ToolCall{call})
	sess := pendingCallSession(t, []core.ToolCall{call})
	head, _, err := LastMessageRefWithManager(DefaultManager(), sess.Path)
	if err != nil {
		t.Fatalf("LastMessageRefWithManager() error = %v", err)
	}
	copied, err := ForkThroughHead(ctx, DefaultManager(), sess.Path, head)
	if err != nil {
		t.Fatalf("ForkThroughHead() error = %v", err)
	}

	entryDir := openJournal(nil).entryDir(call.InvocationID)
	resolution, err := ResolvePendingToolCalls(ctx, sess, pendingToolTestConfig(), core.NewTestLogger(false), core.NewTestNotifier(), core.TestToolUI{}, failOutcomeWriteApprover{entryDir: entryDir})
	if err == nil || !strings.Contains(err.Error(), "record") {
		t.Fatalf("ResolvePendingToolCalls() error = %v, want the unrecorded outcome reported", err)
	}
	if !resolution.Committed || !exists(marker) {
		t.Fatalf("resolution = %+v, ran = %v; want the call run and its results committed anyway", resolution, exists(marker))
	}

	if err := os.Remove(filepath.Join(entryDir, journalOutcomeName)); err != nil {
		t.Fatalf("clear the obstruction: %v", err)
	}
	resolveForTest(t, ctx, copied, pendingToolTestConfig(), core.NewTestNotifier(), &MockApprover{shouldApprove: true})
	if results := lastToolResults(t, copied); len(results) != 1 || results[0].Code != lmerrors.ErrCodeOutcomeUnknown {
		t.Fatalf("copy results = %+v, want the unknown outcome the missing record leaves", results)
	}
}

// A batch that fails part way leaves its committed prefix in the lineage, and
// the pinned head follows it: the next write neither forks nor drops it.
func TestAppendMessagesAdvancesThePinnedHeadPastAPartialBatch(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	sess, err := OpenSession(ctx, GetSessionID(sess.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}

	beforeBatchEntryCommitForTest = func(index int) error {
		if index == 1 {
			return errors.New("disk full")
		}
		return nil
	}
	t.Cleanup(func() { beforeBatchEntryCommitForTest = nil })
	_, err = AppendMessagesWithBlocks(ctx, sess, []MessageEntry{
		{Message: Message{Role: core.RoleAssistant, Content: "first", Timestamp: time.Now()}},
		{Message: Message{Role: core.RoleUser, Content: "second", Timestamp: time.Now()}},
	})
	beforeBatchEntryCommitForTest = nil
	if err == nil {
		t.Fatal("AppendMessagesWithBlocks() error = nil, want the second entry's failure")
	}

	appendPlanMessage(t, ctx, sess, core.RoleUser, "third")
	if len(sess.ConflictForks) != 0 {
		t.Fatalf("ConflictForks = %+v, want the head to have followed the committed entry", sess.ConflictForks)
	}
	texts := lineageTexts(t, ctx, sess.Path)
	if strings.Join(texts, "|") != "user:question|assistant:first|user:third" {
		t.Fatalf("lineage = %q, want the committed prefix kept", texts)
	}
}

// planCase is a plan whose commit creates a directory before it saves the
// user message: a new session, a branch, or a fork for a changed system
// prompt, alone or together.
type planCase struct {
	name   string
	config func(t *testing.T, ctx context.Context) core.RequestOptions
}

var creatingPlans = []planCase{
	{name: "new session", config: func(*testing.T, context.Context) core.RequestOptions {
		return newTestCoordinatorConfig()
	}},
	{name: "branch", config: func(t *testing.T, ctx context.Context) core.RequestOptions {
		sess, _, questionID := planHistory(t, ctx, "")
		cfg := newTestCoordinatorConfig()
		cfg.Branch = GetSessionID(sess.Path) + "/" + questionID
		return cfg
	}},
	{name: "message resume", config: func(t *testing.T, ctx context.Context) core.RequestOptions {
		sess, answerID, _ := planHistory(t, ctx, "")
		cfg := newTestCoordinatorConfig()
		cfg.Resume = GetSessionID(sess.Path) + "/" + answerID
		return cfg
	}},
	{name: "message resume with a system fork", config: func(t *testing.T, ctx context.Context) core.RequestOptions {
		sess, answerID, _ := planHistory(t, ctx, "")
		cfg := newTestCoordinatorConfig()
		cfg.Resume = GetSessionID(sess.Path) + "/" + answerID
		cfg.System = "new system"
		cfg.SystemExplicitlySet = true
		return cfg
	}},
	{name: "system fork", config: func(t *testing.T, ctx context.Context) core.RequestOptions {
		sess, _, _ := planHistory(t, ctx, "old system")
		cfg := resumePlanConfig(sess)
		cfg.System = "new system"
		cfg.SystemExplicitlySet = true
		return cfg
	}},
}

// planHistory creates a session holding a question, an answer, and a second
// question, and returns it with the answer's and the second question's IDs.
func planHistory(t *testing.T, ctx context.Context, system string) (sess *Session, answerID, questionID string) {
	t.Helper()
	sess = createPlanSession(t, system)
	appendPlanMessage(t, ctx, sess, core.RoleUser, "first question")
	answerID = appendPlanMessage(t, ctx, sess, core.RoleAssistant, "first answer")
	questionID = appendPlanMessage(t, ctx, sess, core.RoleUser, "second question")
	return sess, answerID, questionID
}

// A plan commit whose user message fails keeps the session, branch, or fork
// it created and returns it beside the error, which names it: another run may
// already have adopted it, and nothing on disk can show that it has not.
func TestPlanCommitKeepsWhatItCreatedWhenTheUserMessageFails(t *testing.T) {
	for _, tt := range creatingPlans {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			plan, err := PrepareRequest(ctx, tt.config(t, ctx), core.NewTestNotifier(), core.TestToolUI{}, "question", false, PendingToolSkip)
			if err != nil {
				t.Fatalf("PrepareRequest() error = %v", err)
			}
			var created string
			beforeUserMessageSaveForTest = func(sess *Session) error {
				created = sess.Path
				return errors.New("disk full")
			}
			t.Cleanup(func() { beforeUserMessageSaveForTest = nil })

			committed, err := plan.Commit(ctx)
			if err == nil || !strings.Contains(err.Error(), "kept the partial commit in ") || !strings.Contains(err.Error(), GetSessionID(created)) {
				t.Fatalf("Commit() error = %v, want the failure with %s reported kept", err, GetSessionID(created))
			}
			if committed == nil || committed.Path != created || !exists(created) {
				t.Fatalf("Commit() session = %+v, want %s, on disk", committed, created)
			}
		})
	}
}

func writeAsAnotherRun(t *testing.T, sess *Session) string {
	t.Helper()
	other, err := LoadSession(sess.Path)
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	return appendPlanMessage(t, context.Background(), other, core.RoleUser, "another run's question")
}

// Before a plan commit fails, another run can change the session the commit
// created in ways that keep its path and its message names: write to it,
// delete its system message and write its own as 0000, or delete the session
// and create its own at the same path. The failed commit leaves each of those
// in place.
func TestPlanCommitLeavesWhatAnotherRunDidToItsSession(t *testing.T) {
	tests := []struct {
		name string
		// change changes the session as another run and returns a message
		// of that run which must survive.
		change func(t *testing.T, sess *Session) string
	}{
		{name: "wrote to it", change: func(t *testing.T, sess *Session) string {
			writeAsAnotherRun(t, sess)
			return "user:another run's question"
		}},
		{name: "replaced its message", change: func(t *testing.T, sess *Session) string {
			if err := DeleteNode(filepath.Join(sess.Path, "0000")); err != nil {
				t.Fatalf("DeleteNode() error = %v", err)
			}
			if id := writeAsAnotherRun(t, sess); id != "0000" {
				t.Fatalf("the other run's message is %s, want it at the deleted 0000", id)
			}
			return "user:another run's question"
		}},
		{name: "recycled its path", change: func(t *testing.T, sess *Session) string {
			if err := DeleteNode(sess.Path); err != nil {
				t.Fatalf("DeleteNode() error = %v", err)
			}
			if other := createPlanSession(t, "another run's system"); other.Path != sess.Path {
				t.Fatalf("the other run's session is %s, want it at the recycled %s", other.Path, sess.Path)
			}
			return "system:another run's system"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			plan, err := PrepareRequest(ctx, newTestCoordinatorConfig(), core.NewTestNotifier(), core.TestToolUI{}, "question", false, PendingToolSkip)
			if err != nil {
				t.Fatalf("PrepareRequest() error = %v", err)
			}
			var created, survivor string
			beforeUserMessageSaveForTest = func(sess *Session) error {
				created = sess.Path
				survivor = tt.change(t, sess)
				return errors.New("disk full")
			}
			t.Cleanup(func() { beforeUserMessageSaveForTest = nil })

			committed, err := plan.Commit(ctx)
			if err == nil || committed == nil || committed.Path != created {
				t.Fatalf("Commit() = %+v, %v; want the failure and %s reported", committed, err, created)
			}
			if !containsText(lineageTexts(t, ctx, created), survivor) {
				t.Fatalf("lineage of %s = %q, want the other run's %q kept", created, lineageTexts(t, ctx, created), survivor)
			}
		})
	}
}

// failAppendsOutside fails every append that is not into path. A fork's
// copies do not go through the append hook, so the fork completes and the
// write that follows it fails.
func failAppendsOutside(t *testing.T, path string) {
	t.Helper()
	beforeAppendCommitForTest = func(target string) error {
		if filepath.Clean(target) != filepath.Clean(path) {
			return fmt.Errorf("disk full in %s", target)
		}
		return nil
	}
	t.Cleanup(func() { beforeAppendCommitForTest = nil })
}

// A write that finds the head moved forks, and when the write then fails in
// the fork, the fork is kept: it holds the history the write expected. The
// value points at it and records it, so the caller can report it.
func TestConflictForkIsKeptWhenItsWriteFails(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	sess, err := OpenSession(ctx, GetSessionID(sess.Path))
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	original := sess.Path
	intrude(t, ctx, sess.Path)
	failAppendsOutside(t, original)

	_, err = AppendMessageWithToolInteraction(ctx, sess, Message{Role: core.RoleAssistant, Content: "answer", Timestamp: time.Now()}, nil, nil)
	if err == nil {
		t.Fatal("append error = nil, want the failure in the fork")
	}
	if sess.Path == original || len(sess.ConflictForks) != 1 {
		t.Fatalf("session after the failed write = %+v, want it at the recorded fork", sess)
	}
	if texts := lineageTexts(t, ctx, sess.Path); strings.Join(texts, "|") != "user:question" {
		t.Fatalf("fork lineage = %q, want the history through the expected head", texts)
	}
}

// During an ordinary resume the commit creates no directory of its own, but
// its user message write can fork and fail. The kept fork is reported as the
// partial session rather than lost behind an empty list of creations.
func TestPlanCommitReportsTheConflictForkItsWriteKept(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess := createPlanSession(t, "")
	appendPlanMessage(t, ctx, sess, core.RoleUser, "question")
	appendPlanMessage(t, ctx, sess, core.RoleAssistant, "answer")
	plan, err := PrepareRequest(ctx, resumePlanConfig(sess), core.NewTestNotifier(), core.TestToolUI{}, "next question", false, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	intrude(t, ctx, sess.Path)
	failAppendsOutside(t, sess.Path)

	committed, err := plan.Commit(ctx)
	if err == nil {
		t.Fatal("Commit() error = nil, want the failure in the fork")
	}
	if committed == nil || GetRootSession(committed.Path) == GetRootSession(sess.Path) || len(committed.ConflictForks) != 1 {
		t.Fatalf("Commit() session = %+v, want the kept fork reported", committed)
	}
	if !exists(committed.Path) || !strings.Contains(err.Error(), "kept the partial commit in "+GetSessionID(committed.Path)) {
		t.Fatalf("Commit() = %s, %v; want the fork on disk and named", committed.Path, err)
	}
}

// When another run writes to the directory a commit just created, the
// commit's user message write forks, and it can fail in the fork. The commit
// keeps both: the directory it created, which the other run adopted, and the
// fork, which the session value now names and which comes back as the
// partial session.
func TestPlanCommitKeepsTheConflictForkOfADirectoryItCreated(t *testing.T) {
	for _, tt := range creatingPlans {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			plan, err := PrepareRequest(ctx, tt.config(t, ctx), core.NewTestNotifier(), core.TestToolUI{}, "question", false, PendingToolSkip)
			if err != nil {
				t.Fatalf("PrepareRequest() error = %v", err)
			}
			var created string
			beforeUserMessageSaveForTest = func(sess *Session) error {
				created = sess.Path
				writeAsAnotherRun(t, sess)
				failAppendsOutside(t, sess.Path)
				return nil
			}
			t.Cleanup(func() { beforeUserMessageSaveForTest = nil })

			committed, err := plan.Commit(ctx)
			if err == nil {
				t.Fatal("Commit() error = nil, want the failure in the fork")
			}
			if committed == nil || committed.Path == created || len(committed.ConflictForks) != 1 {
				t.Fatalf("Commit() session = %+v, want the conflict fork reported", committed)
			}
			if !exists(committed.Path) {
				t.Fatalf("reported fork %s is not on disk", committed.Path)
			}
			for _, kept := range []string{GetSessionID(created), GetSessionID(committed.Path)} {
				if !strings.Contains(err.Error(), kept) {
					t.Fatalf("Commit() error = %v, want it to name %s as kept", err, kept)
				}
			}
			if !containsText(lineageTexts(t, ctx, created), "user:another run's question") {
				t.Fatal("the other run's message was lost")
			}
		})
	}
}

// forkCase is a way to fork a source session, with the system prompt the
// fork gets and whether it pins its head.
type forkCase struct {
	name   string
	system string
	pinned bool
	fork   func(ctx context.Context, source *Session) (*Session, error)
}

func lastRef(source *Session) (MessageRef, error) {
	head, _, err := LastMessageRefWithManager(DefaultManager(), source.Path)
	return head, err
}

var forkCases = []forkCase{
	{name: "through the head", system: "source system", pinned: true, fork: func(ctx context.Context, source *Session) (*Session, error) {
		head, err := lastRef(source)
		if err != nil {
			return nil, err
		}
		return ForkThroughHead(ctx, DefaultManager(), source.Path, head)
	}},
	{name: "through a message", system: "fork system", fork: func(ctx context.Context, source *Session) (*Session, error) {
		head, err := lastRef(source)
		if err != nil {
			return nil, err
		}
		system := "fork system"
		return ForkSessionThroughMessageWithManager(ctx, DefaultManager(), source.Path, head.Path, head.ID, &system)
	}},
	{name: "whole session", system: "fork system", fork: func(ctx context.Context, source *Session) (*Session, error) {
		system := "fork system"
		return ForkSessionWithManager(ctx, DefaultManager(), source.Path, &system)
	}},
	{name: "system prompt change", system: "fork system", pinned: true, fork: func(ctx context.Context, source *Session) (*Session, error) {
		pinned, err := OpenSession(ctx, GetSessionID(source.Path))
		if err != nil {
			return nil, err
		}
		fork, _, err := MaybeForkForSystem(ctx, pinned, "fork system")
		return fork, err
	}},
}

func messageTexts(messages []core.TypedMessage) []string {
	var texts []string
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if text, ok := block.(core.TextBlock); ok {
				texts = append(texts, msg.Role+":"+text.Text)
			}
		}
	}
	return texts
}

// A fork is built under its own lock, taken before its directory exists.
// Another run that finds the new directory cannot write to it until the
// copies and the head are in place, so the fork holds exactly the lineage it
// copies, and the other run's message lands after its head.
func TestForkIsCompleteBeforeAnotherRunCanWriteToIt(t *testing.T) {
	for _, tt := range forkCases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := setupCoordinatorTestEnv(t)
			source := createPlanSession(t, "source system")
			appendPlanMessage(t, ctx, source, core.RoleUser, "source question")
			appendPlanMessage(t, ctx, source, core.RoleAssistant, "source answer")

			built := false
			foreign := make(chan error, 1)
			afterLockedSessionCreatedForTest = func(path string) {
				built = true
				if err := WithSessionLock(path, 10*time.Millisecond, func() error { return nil }); !errors.Is(err, ErrLockTimeout) {
					t.Errorf("taking the lock of the fork being built: error = %v, want %v", err, ErrLockTimeout)
				}
				started := make(chan struct{})
				go func() {
					close(started)
					other, err := LoadSession(path)
					if err == nil {
						_, err = AppendMessageWithToolInteraction(context.Background(), other, Message{Role: core.RoleUser, Content: "foreign question", Timestamp: time.Now()}, nil, nil)
					}
					foreign <- err
				}()
				<-started
				// Give the other run time to reach the lock.
				time.Sleep(20 * time.Millisecond)
			}
			t.Cleanup(func() { afterLockedSessionCreatedForTest = nil })

			fork, err := tt.fork(ctx, source)
			afterLockedSessionCreatedForTest = nil
			if err != nil {
				t.Fatalf("fork error = %v", err)
			}
			if !built {
				t.Fatal("the fork was not built under its lock")
			}
			if err := <-foreign; err != nil {
				t.Fatalf("the other run's write error = %v", err)
			}
			want := "system:" + tt.system + "|user:source question|assistant:source answer"
			if got := strings.Join(lineageTexts(t, ctx, fork.Path), "|"); got != want+"|user:foreign question" {
				t.Fatalf("fork lineage = %q, want %q and then the other run's message", got, want)
			}
			if tt.pinned {
				through, err := BuildMessagesForSession(ctx, fork)
				if err != nil {
					t.Fatalf("BuildMessagesForSession() error = %v", err)
				}
				if got := strings.Join(messageTexts(through), "|"); got != want {
					t.Fatalf("fork through its head = %q, want %q", got, want)
				}
			}
		})
	}
}

// A fork whose copy fails is taken apart under its lock. Its own messages go;
// a file another run staged in the new directory while it waits for the lock
// stays, and with it the directory, which the error reports.
func TestFailedForkLeavesWhatAnotherRunStagedInIt(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	source := createPlanSession(t, "source system")
	appendPlanMessage(t, ctx, source, core.RoleUser, "run it")
	callID := appendPlanAssistantToolCall(t, ctx, source, "call_1", "universal_command")
	if err := os.WriteFile(buildMessageFilePaths(source.Path, callID).ToolsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt tool interaction: %v", err)
	}
	var forkPath string
	afterLockedSessionCreatedForTest = func(path string) {
		forkPath = path
		if err := os.WriteFile(filepath.Join(path, ".tmp-foreign.json"), []byte("{}"), 0o600); err != nil {
			t.Errorf("stage the other run's write: %v", err)
		}
	}
	t.Cleanup(func() { afterLockedSessionCreatedForTest = nil })

	head, err := lastRef(source)
	if err != nil {
		t.Fatalf("LastMessageRefWithManager() error = %v", err)
	}
	if _, err := ForkThroughHead(ctx, DefaultManager(), source.Path, head); err == nil || !strings.Contains(err.Error(), "kept the incomplete session") {
		t.Fatalf("ForkThroughHead() error = %v, want the failure and the kept directory reported", err)
	}
	entries, err := os.ReadDir(forkPath)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != ".tmp-foreign.json" {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("fork directory holds %v, want only the other run's staged file", names)
	}
}

// Two runs forking at once can pick the same next session ID. The one that
// finds the ID's lock held moves on to the following ID instead of failing.
func TestForkMovesPastAnIDAnotherRunHoldsTheLockOf(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	source := createPlanSession(t, "source system")
	appendPlanMessage(t, ctx, source, core.RoleUser, "source question")
	_, candidates, err := DefaultManager().sessionCandidates(nil)
	if err != nil {
		t.Fatalf("sessionCandidates() error = %v", err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithSessionLock(candidates[0], 0, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	fork, err := ForkThroughHead(ctx, DefaultManager(), source.Path, MessageRef{})
	close(release)
	if lockErr := <-done; lockErr != nil {
		t.Fatalf("holding the lock: %v", lockErr)
	}
	if err != nil {
		t.Fatalf("ForkThroughHead() error = %v", err)
	}
	if fork.Path != candidates[1] {
		t.Fatalf("fork at %s, want %s, past the ID whose lock was held", fork.Path, candidates[1])
	}
}
