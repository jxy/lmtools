package session

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"slices"
	"strings"
)

// PendingToolMode is what preparing a request does with tool calls pending
// at its start point: skip them, refuse until they are resolved, or preview
// them with placeholder results for -print-curl.
type PendingToolMode int

const (
	PendingToolSkip PendingToolMode = iota
	PendingToolExecute
	PendingToolPreview
)

// PendingResolution reports what ResolvePending did.
type PendingResolution struct {
	// Found is true when the head held pending tool calls.
	Found bool
	// Committed is true when their results were saved, which moved the
	// head to them.
	Committed bool
	// ResultsAwaitReply is true when the head is tool results the model has
	// not answered: just saved, or saved by an earlier run whose request
	// failed. A run with no new prompt still has a request to send.
	ResultsAwaitReply bool
}

// pendingCalls returns the tool calls pending at the end of h and the index
// of the reply that holds them.
func pendingCalls(h History) ([]core.ToolCall, int) {
	resolved := map[string]bool{}
	for i := len(h.Messages) - 1; i >= 0; i-- {
		m, tools := h.Messages[i], h.Contents[i].Tools
		switch {
		case m.Role == core.RoleAssistant && tools != nil && len(tools.Calls) > 0:
			var pending []core.ToolCall
			for _, call := range tools.Calls {
				if call.ID == "" || !resolved[call.ID] {
					pending = append(pending, call)
				}
			}
			return pending, i
		case m.Role == core.RoleSystem || m.Role == core.RoleDeveloper || m.Pin != nil:
			continue
		case m.Role == core.RoleUser && tools != nil && len(tools.Results) > 0:
			for _, result := range tools.Results {
				if result.ID != "" {
					resolved[result.ID] = true
				}
			}
		default:
			return nil, -1
		}
	}
	return nil, -1
}

// endsWithToolResults reports whether h ends with tool results, which a
// model answers with its next reply.
func endsWithToolResults(h History) bool {
	n := len(h.Messages)
	if n == 0 || h.Messages[n-1].Role != core.RoleUser {
		return false
	}
	tools := h.Contents[n-1].Tools
	return tools != nil && len(tools.Results) > 0
}

// validateDeveloperHistory refuses a history that holds developer messages
// when the run's wire cannot carry the developer role.
func validateDeveloperHistory(cfg core.RequestOptions, h History) error {
	for _, m := range h.Messages {
		if m.Role == core.RoleDeveloper {
			return core.ValidateDeveloperRole(cfg)
		}
	}
	return nil
}

// notebookTools names the MCP server that holds the notebook tools.
const notebookTools = "notebook"

// ResolvePending resolves the tool calls pending at the head. It is a step
// of its own, run before a request is prepared, so that retrying a provider
// request never runs a tool. setup runs once the pending calls are known.
//
// It takes ownership of every pending call first, waiting, cancellably,
// while another live run owns one, and then reads what the journal knows:
//
//   - A call with a recorded outcome is known. The outcome is sent and the
//     call does not run, on whichever branch this is.
//   - A call claimed and never started runs, subject to approval as always.
//   - Any other call is uncertain: it started without recording an outcome,
//     or its entry is missing. An uncertain call never runs again unasked.
//     The operator is asked, and a denial, or no one to ask, sends the model
//     a result saying the outcome is unknown. view_image reads a file and
//     has no effects, so it is never uncertain.
//
// A call keeps the notebook it was made for: a notebook call that still has
// to run runs only when this run's notebook is the one in force where the
// call was made. Otherwise the calls stay pending and the error names that
// notebook.
//
// The results are saved after the head, even when the context was cancelled
// while calls ran, so no pending state is left.
func (s *Session) ResolvePending(ctx context.Context, cfg core.RequestOptions, log core.Logger, notifier core.Notifier, ui core.ToolUI, approver core.Approver, setup func([]core.ToolCall) error) (PendingResolution, error) {
	var resolution PendingResolution
	h, err := s.History()
	if err != nil {
		return resolution, lmerrors.WrapError("read session history", err)
	}
	if err := validateDeveloperHistory(cfg, h); err != nil {
		return resolution, err
	}
	calls, holder := pendingCalls(h)
	if len(calls) == 0 {
		resolution.ResultsAwaitReply = endsWithToolResults(h)
		return resolution, nil
	}
	resolution.Found = true
	madeFor := SettingsAt(h.Messages[:holder+1]).Notebook
	if !cfg.ToolEnabled {
		// A notebook call under another notebook, none included, names the
		// notebook it was made for, which a run with tools would need too.
		for _, call := range calls {
			if call.MCPServer == notebookTools && madeFor != cfg.Context.Notebook {
				return resolution, notebookCallError(call, madeFor)
			}
		}
		return resolution, lmerrors.WrapError("execute pending tools", fmt.Errorf("pending tool calls require -tool to continue"))
	}
	if cfg.ImplicitNotebookTools {
		tools := core.AdvertisedTools(cfg)
		for _, call := range calls {
			if !slices.ContainsFunc(tools, func(t core.ToolDefinition) bool {
				return t.Name == call.Name && (call.MCPServer == "" || call.MCPServer == t.MCPServer) && (call.MCPTool == "" || call.MCPTool == t.MCPTool)
			}) {
				return resolution, fmt.Errorf("pending tool call %q requires -tool or its explicit tool selection to continue", call.Name)
			}
		}
	}
	for _, call := range calls {
		if call.InvocationID == "" {
			return resolution, fmt.Errorf("pending tool call %s has no invocation identity", describePendingCall(call))
		}
	}
	if setup != nil {
		if err := setup(calls); err != nil {
			return resolution, err
		}
	}
	work := append([]core.ToolCall(nil), calls...)
	keys := make([]string, len(work))
	for i := range work {
		keys[i] = work[i].InvocationID
	}
	journal := openJournal(s.Store)
	claim, err := journal.acquire(ctx, keys, func(owner journalEntry, known bool) {
		notifier.Infof("Waiting for %s, which is running the pending tool calls", describeJournalOwner(owner, known))
	})
	if err != nil {
		return resolution, lmerrors.WrapError("take ownership of pending tool calls", err)
	}
	defer claim.Release()

	results := make([]core.ToolResult, len(work))
	states := make([]invocationState, len(work))
	for i, call := range work {
		state, outcome, err := journal.state(call.InvocationID)
		if err != nil {
			return resolution, err
		}
		states[i] = state
		if state == invocationKnown {
			results[i] = outcome
			continue
		}
		if call.MCPServer == notebookTools && madeFor != cfg.Context.Notebook {
			return resolution, notebookCallError(call, madeFor)
		}
	}

	var toRun []int
	// recordErrs are journal writes that failed. They do not stop the
	// results from being saved, and are reported once the save was tried.
	var recordErrs []error
	for i, call := range work {
		state := states[i]
		switch {
		case state == invocationKnown:
			notifier.Infof("Tool call %s already ran; sending its recorded outcome instead of running it again", describePendingCall(call))
		case call.Name == core.ViewImageToolName:
			toRun = append(toRun, i)
		case state == invocationNeverStarted:
			toRun = append(toRun, i)
		default:
			reason := uncertainCallReason(state)
			asked, approval, err := askRerun(ctx, cfg, approver, ui, call, reason)
			if err != nil {
				return resolution, err
			}
			if approval.Approved {
				toRun = append(toRun, i)
				continue
			}
			if !asked {
				notifier.Infof("Tool call %s was not run again: %s", describePendingCall(call), reason)
			}
			results[i] = outcomeUnknownResult(call, reason)
			if asked && strings.TrimSpace(approval.Reason) != "" {
				results[i].Reason = approval.DenialReason()
				results[i].Error += "; " + results[i].Reason
			}
			if err := claim.Finished(call, results[i]); err != nil {
				recordErrs = append(recordErrs, fmt.Errorf("record the outcome of tool call %s: %w", call.ID, err))
			}
		}
	}

	if len(toRun) > 0 {
		executor, err := core.NewExecutor(cfg, log, approver)
		if err != nil {
			return resolution, lmerrors.WrapError("create executor for pending tools", err)
		}
		executor.SetRecorder(claim)
		runCalls := make([]core.ToolCall, len(toRun))
		for j, i := range toRun {
			runCalls[j] = work[i]
		}
		runResults := executor.ExecuteParallel(ctx, runCalls, ui)
		for j, i := range toRun {
			results[i] = runResults[j]
		}
		if err := executor.TakeRecordingError(); err != nil {
			recordErrs = append(recordErrs, err)
		}
	}

	persistCtx, cancel := core.PersistenceContext(ctx)
	defer cancel()
	_, saveErr := s.SaveToolResults(persistCtx, results, core.BuildTruncationNotes(results, work))
	if saveErr == nil {
		resolution.Committed = true
		resolution.ResultsAwaitReply = true
	} else {
		saveErr = lmerrors.WrapError("save pending tool results", saveErr)
	}
	// A branch that still shows these calls pending reads the journal, so an
	// outcome the journal lacks is one the operator has to hear about now.
	var recordErr error
	if len(recordErrs) > 0 {
		recordErr = lmerrors.WrapError("record pending tool call outcomes", stdErrors.Join(recordErrs...))
	}
	return resolution, stdErrors.Join(saveErr, recordErr)
}

// askRerun asks whether to run an uncertain call again, and reports whether
// anyone was asked. Only a person can answer: with no approver, with
// -tool-non-interactive, or with no reviewed tool UI to show the call on,
// nobody is asked and the answer is no.
// notebookCallError refuses a pending notebook call under another notebook
// than the one it was made for.
func notebookCallError(call core.ToolCall, madeFor string) error {
	return fmt.Errorf("pending tool call %s was made for the notebook %q; resume with -notebook %q to run it", describePendingCall(call), madeFor, madeFor)
}

func askRerun(ctx context.Context, cfg core.RequestOptions, approver core.Approver, ui core.ToolUI, call core.ToolCall, reason string) (asked bool, approval core.Approval, err error) {
	if approver == nil || cfg.ToolNonInteractive || ui == nil {
		return false, core.Approval{}, nil
	}
	ui.ShowRerun(call, reason)
	approval, err = approver.ApproveRerun(ctx, call)
	if err != nil {
		if ctx.Err() != nil {
			return true, core.Approval{}, ctx.Err()
		}
		return true, core.Approval{}, lmerrors.WrapError("ask whether to run a tool call again", err)
	}
	return true, approval, nil
}

func uncertainCallReason(state invocationState) string {
	if state == invocationStartedUnfinished {
		return "an earlier run started it and recorded no outcome"
	}
	return "its journal entry is missing, so whether it ran is unknown"
}

// outcomeUnknownResult is the result sent for an uncertain call that was not
// run again. NotRun stays false: the call may well have run.
func outcomeUnknownResult(call core.ToolCall, reason string) core.ToolResult {
	return core.ToolResult{
		ID:    call.ID,
		Error: "outcome unknown: " + reason + "; it was not run again",
		Code:  lmerrors.ErrCodeOutcomeUnknown,
	}
}

func describePendingCall(call core.ToolCall) string {
	name := call.Name
	if call.MCPServer != "" {
		name = call.MCPServer + "/" + call.MCPTool
	}
	if call.ID == "" {
		return name
	}
	return name + " (" + call.ID + ")"
}

func describeJournalOwner(owner journalEntry, known bool) string {
	if !known || owner.PID == 0 {
		return "another run"
	}
	if owner.Host == "" {
		return fmt.Sprintf("process %d", owner.PID)
	}
	return fmt.Sprintf("process %d on %s", owner.PID, owner.Host)
}

func placeholderToolResults(calls []core.ToolCall) []core.ToolResult {
	results := make([]core.ToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, core.ToolResult{
			ID:     call.ID,
			Output: fmt.Sprintf("[print-curl placeholder] Tool %q (call %s) was not executed.", call.Name, call.ID),
		})
	}
	return results
}
