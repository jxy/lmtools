package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAssignInvocationIDsKeepsExistingIdentities(t *testing.T) {
	calls := []ToolCall{{ID: "a"}, {ID: "b", InvocationID: "kept"}}
	AssignInvocationIDs(calls)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(calls[0].InvocationID) {
		t.Fatalf("assigned identity = %q, want 32 hexadecimal digits", calls[0].InvocationID)
	}
	if calls[1].InvocationID != "kept" {
		t.Fatalf("existing identity = %q, want it kept", calls[1].InvocationID)
	}
	first, second := NewInvocationID(), NewInvocationID()
	if first == second {
		t.Fatal("two fresh identities are equal")
	}
}

// recordingClaim records what the executor tells it, in order.
type recordingClaim struct {
	mu        sync.Mutex
	events    []string
	failStart bool
	onFinish  func()
	released  bool
}

func (c *recordingClaim) Started(call ToolCall) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, "started "+call.ID)
	if c.failStart {
		return errors.New("disk full")
	}
	return nil
}

func (c *recordingClaim) Finished(call ToolCall, result ToolResult) error {
	c.mu.Lock()
	c.events = append(c.events, fmt.Sprintf("finished %s notrun=%v", call.ID, result.NotRun))
	onFinish := c.onFinish
	c.mu.Unlock()
	if onFinish != nil {
		onFinish()
	}
	return nil
}

func (c *recordingClaim) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.released = true
}

func autoApproveConfig() RequestOptions {
	cfg := NewTestRequestConfig()
	cfg.ToolEnabled = true
	cfg.ToolAutoApprove = true
	cfg.ToolTimeout = 5 * time.Second
	return cfg
}

func commandCall(id string, argv ...string) ToolCall {
	args, _ := json.Marshal(UniversalCommandArgs{Command: argv})
	return ToolCall{ID: id, Name: "universal_command", Args: args, InvocationID: "inv-" + id}
}

func TestExecutorRecordsStartsAndOutcomes(t *testing.T) {
	executor, err := NewExecutor(autoApproveConfig(), NewTestLogger(false), nil)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	claim := &recordingClaim{}
	executor.SetRecorder(claim)

	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		commandCall("run", "echo", "hi"),
		{ID: "refused", Name: "no_such_tool", Args: json.RawMessage(`{}`), InvocationID: "inv-refused"},
	}, TestToolUI{})

	if results[0].Error != "" || !strings.Contains(results[0].Output, "hi") {
		t.Fatalf("run result = %+v, want the command's output", results[0])
	}
	if !results[1].NotRun {
		t.Fatalf("refused result = %+v, want not run", results[1])
	}
	want := []string{"finished refused notrun=true", "started run", "finished run notrun=false"}
	if strings.Join(claim.events, "|") != strings.Join(want, "|") {
		t.Fatalf("recorded = %q, want %q", claim.events, want)
	}
}

func TestExecutorRefusesToStartACallWhoseStartIsNotRecorded(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	executor, err := NewExecutor(autoApproveConfig(), NewTestLogger(false), nil)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	executor.SetRecorder(&recordingClaim{failStart: true})

	results := executor.ExecuteParallel(context.Background(), []ToolCall{commandCall("run", "touch", marker)}, TestToolUI{})
	if !results[0].NotRun || !strings.Contains(results[0].Error, "recording the call's start failed") {
		t.Fatalf("result = %+v, want the call refused because its start was not recorded", results[0])
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the call ran without a start record, marker stat err = %v", err)
	}
}

// cancelAwareStore records the state of the context each save ran under.
type cancelAwareStore struct {
	mu            sync.Mutex
	savedCalls    [][]ToolCall
	resultSaveErr []error
}

func (s *cancelAwareStore) GetPath() string { return "memory" }

func (s *cancelAwareStore) SaveAssistant(_ context.Context, _ string, calls []ToolCall, _ string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.savedCalls = append(s.savedCalls, append([]ToolCall(nil), calls...))
	return "memory", "0001", nil
}

func (s *cancelAwareStore) SaveToolResults(ctx context.Context, _ []ToolResult, _ string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resultSaveErr = append(s.resultSaveErr, ctx.Err())
	return "memory", "0002", nil
}

type recordingJournal struct {
	claim   *recordingClaim
	claimed []ToolCall
}

func (j *recordingJournal) Claim(calls []ToolCall) (ToolClaim, error) {
	j.claimed = append([]ToolCall(nil), calls...)
	return j.claim, nil
}

// A cancellation that arrives after the round's calls finished must not
// lose their results: they are committed on a persistence context, and the
// turn then stops with the cancellation.
func TestToolLoopCommitsTheResultsOfACancelledRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	claim := &recordingClaim{onFinish: cancel}
	journal := &recordingJournal{claim: claim}
	store := &cancelAwareStore{}

	call := commandCall("run", "echo", "hi")
	call.InvocationID = ""
	result := handleToolExecutionLoop(ToolContext{
		Ctx:             ctx,
		Cfg:             autoApproveConfig(),
		Logger:          NewTestLogger(false),
		Notifier:        NewTestNotifier(),
		ExecCfg:         ToolExecutionConfig{Store: store, Journal: journal},
		UI:              TestToolUI{},
		InitialResponse: Response{ToolCalls: []ToolCall{call}},
	})
	err := result.Error

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loop error = %v, want the cancellation", err)
	}
	if len(store.resultSaveErr) != 1 || store.resultSaveErr[0] != nil {
		t.Fatalf("result saves ran under context errors %v, want one save on a live context", store.resultSaveErr)
	}
	if len(store.savedCalls) != 1 || store.savedCalls[0][0].InvocationID == "" {
		t.Fatalf("saved calls = %+v, want the call saved with an identity", store.savedCalls)
	}
	if journal.claimed[0].InvocationID != store.savedCalls[0][0].InvocationID {
		t.Fatalf("claimed %q, saved %q, want the same identity", journal.claimed[0].InvocationID, store.savedCalls[0][0].InvocationID)
	}
	if !claim.released {
		t.Fatal("the claim was not released")
	}
}

// finishFailingClaim fails every outcome record.
type finishFailingClaim struct{ recordingClaim }

func (c *finishFailingClaim) Finished(call ToolCall, result ToolResult) error {
	_ = c.recordingClaim.Finished(call, result)
	return errors.New("disk full")
}

type failingClaimJournal struct{ claim ToolClaim }

func (j failingClaimJournal) Claim([]ToolCall) (ToolClaim, error) { return j.claim, nil }

// The results are still saved when the journal cannot record an outcome,
// and the failure reaches the caller rather than a debug log.
func TestToolLoopReportsAnOutcomeItCouldNotRecord(t *testing.T) {
	store := &cancelAwareStore{}
	result := handleToolExecutionLoop(ToolContext{
		Ctx:             context.Background(),
		Cfg:             autoApproveConfig(),
		Logger:          NewTestLogger(false),
		Notifier:        NewTestNotifier(),
		ExecCfg:         ToolExecutionConfig{Store: store, Journal: failingClaimJournal{claim: &finishFailingClaim{}}},
		UI:              TestToolUI{},
		InitialResponse: Response{ToolCalls: []ToolCall{commandCall("run", "echo", "hi")}},
	})
	if result.Error == nil || !strings.Contains(result.Error.Error(), "record tool call outcomes") {
		t.Fatalf("loop error = %v, want the unrecorded outcome reported", result.Error)
	}
	if len(store.resultSaveErr) != 1 {
		t.Fatalf("result saves = %d, want the results saved before the failure is reported", len(store.resultSaveErr))
	}
}

// assistantFailingStore fails every assistant save.
type assistantFailingStore struct{ cancelAwareStore }

func (s *assistantFailingStore) SaveAssistant(context.Context, string, []ToolCall, string) (string, string, error) {
	return "", "", errors.New("disk full")
}

// A response the loop presented and then could not save is reported as an
// unsaved answer, not only as the loop's error.
func TestToolLoopReportsAPresentedResponseItCouldNotSave(t *testing.T) {
	result := handleToolExecutionLoop(ToolContext{
		Ctx:             context.Background(),
		Cfg:             autoApproveConfig(),
		Logger:          NewTestLogger(false),
		Notifier:        NewTestNotifier(),
		ExecCfg:         ToolExecutionConfig{Store: &assistantFailingStore{}},
		UI:              TestToolUI{},
		InitialResponse: Response{Text: "running it", ToolCalls: []ToolCall{commandCall("run", "echo", "hi")}},
	})
	if result.UnsavedAnswer == nil || result.Error == nil {
		t.Fatalf("result = %+v, want the failed save as both the error and the unsaved answer", result)
	}
}

type refusingJournal struct{}

func (refusingJournal) Claim([]ToolCall) (ToolClaim, error) {
	return nil, errors.New("journal unavailable")
}

// Whatever stops the loop before the presented response is saved, the result
// reports the response as unsaved; a failing save is only one such cause.
func TestToolLoopReportsAPresentedResponseLeftUnsaved(t *testing.T) {
	missingWhitelist := autoApproveConfig()
	missingWhitelist.ToolWhitelist = filepath.Join(t.TempDir(), "missing-whitelist.json")

	tests := []struct {
		name    string
		cfg     RequestOptions
		journal ToolJournal
	}{
		{name: "claim fails", cfg: autoApproveConfig(), journal: refusingJournal{}},
		{name: "executor cannot start", cfg: missingWhitelist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &cancelAwareStore{}
			result := handleToolExecutionLoop(ToolContext{
				Ctx:             context.Background(),
				Cfg:             tt.cfg,
				Logger:          NewTestLogger(false),
				Notifier:        NewTestNotifier(),
				ExecCfg:         ToolExecutionConfig{Store: store, Journal: tt.journal},
				UI:              TestToolUI{},
				InitialResponse: Response{Text: "running it", ToolCalls: []ToolCall{commandCall("run", "echo", "hi")}},
			})
			if result.Error == nil || result.UnsavedAnswer == nil {
				t.Fatalf("result = %+v, want the failure reported as leaving the response unsaved", result)
			}
			if len(store.savedCalls) != 0 {
				t.Fatalf("saved %d assistant messages, want none before the failure", len(store.savedCalls))
			}
		})
	}
}
