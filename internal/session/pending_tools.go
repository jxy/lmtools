package session

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
)

type PendingToolMode int

const (
	PendingToolSkip PendingToolMode = iota
	PendingToolExecute
	PendingToolPreview
)

// pendingToolResolution is what preparing a request makes of pending calls:
// for -print-curl, placeholder results that stand in for running them.
type pendingToolResolution struct {
	HasPending     bool
	PreviewCalls   []core.ToolCall
	PreviewResults []core.ToolResult
	AdditionalText string
}

// PendingResolution reports what ResolvePendingToolCalls did.
type PendingResolution struct {
	// Found is true when the session's head held pending tool calls.
	Found bool
	// Committed is true when their results were committed, which advanced
	// the session and its pinned head.
	Committed bool
	// ResultsAwaitReply is true when the head is tool results the model has
	// not answered: just committed, or committed by an earlier attempt whose
	// request failed. A turn with no new input still has a request to send.
	ResultsAwaitReply bool
}

// ResolvePendingToolCalls resolves the tool calls pending at the session's
// pinned head, which a nil head pins at the lineage's current end. It is its
// own step, run before a request is prepared, so that retrying a provider
// request never runs a tool.
//
// It takes ownership of every pending call first, waiting, cancellably,
// while another live run owns one, and then reads what the journal knows:
//
//   - A call with a recorded outcome is known. The outcome is sent and the
//     call does not run, in whichever copy of the transcript this is.
//   - A call claimed and never started runs, subject to approval as always.
//   - Any other call is uncertain: it started without recording an outcome,
//     or its entry is missing, or it was saved before calls had identities.
//     An uncertain call never runs again unasked. The operator is asked,
//     and a denial, or no one to ask, sends the model a result saying the
//     outcome is unknown. view_image reads a file and has no effects, so it
//     is never uncertain.
//
// The results are committed through the session's pinned head, even when
// the context was cancelled while calls ran, so no pending state is left.
func ResolvePendingToolCalls(ctx context.Context, sess *Session, cfg core.RequestOptions, log core.Logger, notifier core.Notifier, ui core.ToolUI, approver core.Approver) (PendingResolution, error) {
	var resolution PendingResolution
	if sess == nil {
		return resolution, nil
	}

	// The lineage, the pending calls, and the head's identity are read under
	// one hold of the tree's lock, which is released before ownership of the
	// calls is taken: the run that owns them may need it to commit.
	manager := DefaultManager()
	var (
		calls  []core.ToolCall
		holder lineageMessageRef
	)
	err := withTreeLock(sess.Path, func() error {
		refs, head, err := lineageThroughHeadLocked(manager, sess.Path, sess.Head)
		if err != nil {
			return lmerrors.WrapError("read session lineage", err)
		}
		sess.Head = head
		if calls, holder, err = pendingToolCallsWithRef(ctx, refs); err != nil {
			return lmerrors.WrapError("check pending tool calls", err)
		}
		if len(calls) == 0 {
			resolution.ResultsAwaitReply, err = endsWithToolResults(refs)
		}
		return err
	})
	if err != nil || len(calls) == 0 {
		return resolution, err
	}
	resolution.Found = true
	if !cfg.ToolEnabled {
		return resolution, lmerrors.WrapError("execute pending tools", fmt.Errorf("pending tool calls require -tool to continue"))
	}

	// Work on copies. A call saved before invocations had identities is
	// keyed by where it sits in this transcript.
	work := append([]core.ToolCall(nil), calls...)
	legacy := make([]bool, len(work))
	keys := make([]string, len(work))
	for i := range work {
		if work[i].InvocationID == "" {
			legacy[i] = true
			work[i].InvocationID = legacyInvocationKey(holder, i, work[i])
		}
		keys[i] = work[i].InvocationID
	}

	journal := openJournal(manager)
	claim, err := journal.acquire(ctx, keys, func(owner journalEntry, known bool) {
		notifier.Infof("Waiting for %s, which is running the pending tool calls", describeJournalOwner(owner, known))
	})
	if err != nil {
		return resolution, lmerrors.WrapError("take ownership of pending tool calls", err)
	}
	defer claim.Release()

	results := make([]core.ToolResult, len(work))
	var toRun []int
	// recordErrs are journal writes that failed. They do not stop the
	// results from being saved, and are reported once the save was tried.
	var recordErrs []error
	for i, call := range work {
		state, outcome, err := journal.state(call.InvocationID)
		if err != nil {
			return resolution, err
		}
		switch {
		case state == invocationKnown:
			results[i] = outcome
			notifier.Infof("Tool call %s already ran; sending its recorded outcome instead of running it again", describePendingCall(call))
		case call.Name == core.ViewImageToolName:
			toRun = append(toRun, i)
		case state == invocationNeverStarted && !legacy[i]:
			toRun = append(toRun, i)
		default:
			reason := uncertainCallReason(state, legacy[i])
			asked, rerun, err := askRerun(ctx, cfg, approver, ui, call, reason)
			if err != nil {
				return resolution, err
			}
			if rerun {
				toRun = append(toRun, i)
				continue
			}
			if !asked {
				notifier.Infof("Tool call %s was not run again: %s", describePendingCall(call), reason)
			}
			results[i] = outcomeUnknownResult(call, reason)
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
	_, saveErr := SaveToolResults(persistCtx, sess, results, core.BuildTruncationNotes(results, work))
	if saveErr == nil {
		resolution.Committed = true
		resolution.ResultsAwaitReply = true
	} else {
		saveErr = lmerrors.WrapError("save pending tool results", saveErr)
	}
	// A copy of the transcript that still shows these calls pending reads
	// the journal, not this session, so an outcome the journal lacks is one
	// the operator has to hear about now.
	var recordErr error
	if len(recordErrs) > 0 {
		recordErr = lmerrors.WrapError("record pending tool call outcomes", stdErrors.Join(recordErrs...))
	}
	return resolution, stdErrors.Join(saveErr, recordErr)
}

// endsWithToolResults reports whether a lineage ends with a tool results
// message, which a model answers with its next response.
func endsWithToolResults(refs []lineageMessageRef) (bool, error) {
	if len(refs) == 0 {
		return false, nil
	}
	last := refs[len(refs)-1]
	if last.message.Role != core.RoleUser {
		return false, nil
	}
	interaction, err := LoadToolInteraction(last.path, last.message.ID)
	if err != nil {
		return false, lmerrors.WrapError("load tool interaction for message "+last.message.ID, err)
	}
	return interaction != nil && len(interaction.Results) > 0, nil
}

// askRerun asks whether to run an uncertain call again, and reports whether
// anyone was asked. Only a person can answer: with no approver, with
// -tool-non-interactive, or with no reviewed tool UI to show the call on,
// nobody is asked and the answer is no. The UI shows the call and the reason
// on the stream the question uses, which newOperatorToolSurface picks for
// both, so the operator never answers about a call they were not shown.
func askRerun(ctx context.Context, cfg core.RequestOptions, approver core.Approver, ui core.ToolUI, call core.ToolCall, reason string) (asked, rerun bool, err error) {
	if approver == nil || cfg.ToolNonInteractive || ui == nil {
		return false, false, nil
	}
	ui.ShowRerun(call, reason)
	approved, err := approver.ApproveRerun(ctx, call)
	if err != nil {
		if ctx.Err() != nil {
			return true, false, ctx.Err()
		}
		return true, false, lmerrors.WrapError("ask whether to run a tool call again", err)
	}
	return true, approved, nil
}

func uncertainCallReason(state invocationState, legacy bool) string {
	switch {
	case legacy:
		return "it was saved before lmc recorded tool calls, so whether it ran is unknown"
	case state == invocationStartedUnfinished:
		return "an earlier run started it and recorded no outcome"
	default:
		return "its journal entry is missing, so whether it ran is unknown"
	}
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

func placeholderPendingToolResults(calls []core.ToolCall) []core.ToolResult {
	results := make([]core.ToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, core.ToolResult{
			ID:     call.ID,
			Output: fmt.Sprintf("[print-curl placeholder] Tool %q (call %s) was not executed.", call.Name, call.ID),
		})
	}
	return results
}
