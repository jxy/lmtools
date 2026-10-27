package core

import (
	"context"
	"fmt"
	"lmtools/internal/errors"
	"strings"
	"sync"
)

// ExecuteParallel shows every call of the round, then asks about each call
// that needs approval, naming it, before starting any command; then it
// executes approved calls in parallel with a concurrency limit. Showing the
// whole round first lets the operator answer each question knowing what else
// the model asked for, and approve the rest of the round in one answer. A nil
// UI is valid only when policy does not require interactive approval.
func (e *Executor) ExecuteParallel(ctx context.Context, calls []ToolCall, ui ToolUI) []ToolResult {
	r := e.prepareRound(ctx, calls)
	if ui != nil {
		r.show(ctx, ui)
	}
	r.approve(ctx, ui)
	prepared := rejectSharedOutputFiles(r.runnable(), r.results)
	if len(prepared) == 0 && ctx.Err() != nil {
		// A round the operator cancelled before anything ran has nothing to
		// show: every result says the same thing, and the turn reports it once.
		ui = nil
	}
	workers := min(effectiveMaxParallel(e.maxParallel), len(prepared))
	if ui != nil {
		ui.BeforeRun(len(calls), len(prepared), workers)
	}

	// Every call refused before this point has its outcome already, and
	// recording it now means recovery reads the refusal instead of asking
	// again.
	runnable := make([]bool, len(calls))
	for _, exec := range prepared {
		runnable[exec.index] = true
	}
	for i := range calls {
		if !runnable[i] {
			e.recordOutcome(calls[i], r.results[i])
		}
	}

	feed := newResultFeed(ui, calls, r.results, runnable)
	jobs := make(chan preparedExecution)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for exec := range jobs {
				r.run(ctx, exec, feed)
			}
		}()
	}
	for _, exec := range prepared {
		jobs <- exec
	}
	close(jobs)
	wg.Wait()

	if ui != nil {
		ui.AfterExecute(calls, r.results)
	}
	return r.results
}

// round is one response's tool calls on their way to results: prepared,
// shown, approved, then run.
type round struct {
	e       *Executor
	calls   []ToolCall
	execs   []preparedExecution
	results []ToolResult
	// ready marks the calls that may still run.
	ready []bool
	// asking counts the calls that will ask a question.
	asking int
}

func (e *Executor) prepareRound(ctx context.Context, calls []ToolCall) *round {
	r := &round{
		e:       e,
		calls:   calls,
		execs:   make([]preparedExecution, len(calls)),
		results: make([]ToolResult, len(calls)),
		ready:   make([]bool, len(calls)),
	}
	for i, call := range calls {
		if i >= DefaultMaxToolCalls {
			r.results[i] = ToolResult{
				ID:    call.ID,
				Error: fmt.Sprintf("maximum tool calls per round exceeded (%d)", DefaultMaxToolCalls),
				Code:  errors.ErrCodeInvalidInput,
			}
			continue
		}
		r.execs[i], r.results[i], r.ready[i] = e.prepareSingle(ctx, call)
		if r.execs[i].mcp != nil {
			// Label the call for the review line and the result list, even
			// when response parsing had no definitions to label it from.
			calls[i].MCPServer, calls[i].MCPTool = r.execs[i].mcp.call.Server, r.execs[i].mcp.call.Tool
		}
		if r.ready[i] && r.execs[i].approvalRequired {
			r.asking++
		}
	}
	return r
}

// show presents every call. In a round that asks questions, a note under a
// call says why it will not run, so the operator answering knows it was not
// overlooked, or why it cannot start now.
func (r *round) show(ctx context.Context, ui ToolUI) {
	for i, call := range r.calls {
		ui.ShowCall(i, len(r.calls), call, r.execs[i].args)
		if r.asking == 0 || ctx.Err() != nil {
			continue
		}
		switch {
		case !r.ready[i]:
			ui.ShowCallNote(i, "Will not run: "+refusalReason(r.results[i]))
		case r.execs[i].startWarning != "":
			ui.ShowCallNote(i, "Cannot start now: "+r.execs[i].startWarning)
		}
	}
}

// refusalReason is the first line of why a call was refused before it ran.
func refusalReason(result ToolResult) string {
	reason := result.Reason
	if reason == "" {
		reason = result.Error
	}
	first, _, _ := strings.Cut(reason, "\n")
	return first
}

// approve asks about each call that needs approval, in order, until an answer
// approves the rest of the round. Ctrl-C at any question, or while the round
// was shown, cancels the round, the calls approved before it included; none
// has started, and no later question is asked.
func (r *round) approve(ctx context.Context, ui ToolUI) {
	remaining := r.asking
	rest := false
	for i := range r.calls {
		if ctx.Err() != nil {
			break
		}
		if !r.ready[i] || !r.execs[i].approvalRequired {
			continue
		}
		remaining--
		if rest {
			continue
		}
		askCtx := WithApprovalTarget(ctx, ApprovalTarget{Index: i, Total: len(r.calls), Remaining: remaining})
		r.ready[i], rest = r.e.resolveApproval(askCtx, r.execs[i], ui, &r.results[i])
	}
	if ctx.Err() != nil {
		for i := range r.calls {
			if r.ready[i] {
				markCancelledBeforeRun(&r.results[i])
				r.ready[i] = false
			}
		}
	}
}

// runnable marks every call that will not run as not run, and returns the
// calls that will, in request order.
func (r *round) runnable() []preparedExecution {
	prepared := make([]preparedExecution, 0, len(r.calls))
	for i := range r.calls {
		if !r.ready[i] {
			// One place says so for every way a call can be refused before it
			// runs, including the ones above that assemble their own result.
			// Leaving each site to remember would put the CLI's old
			// error-code list back, one struct literal at a time.
			r.results[i].NotRun = true
			continue
		}
		r.execs[i].index = i
		prepared = append(prepared, r.execs[i])
	}
	return prepared
}

// run executes one prepared call on a worker and records its outcome.
func (r *round) run(ctx context.Context, exec preparedExecution, feed *resultFeed) {
	call := r.calls[exec.index]
	if ctx.Err() == nil {
		if err := r.e.recordStart(call); err != nil {
			result := ToolResult{
				ID:     exec.id,
				NotRun: true,
				Error:  fmt.Sprintf("not run: recording the call's start failed: %v", err),
				Code:   errors.ErrCodeExecError,
			}
			r.results[exec.index] = result
			r.e.recordOutcome(call, result)
			feed.ready(exec.index)
			return
		}
	}
	started := ctx.Err() == nil
	if started {
		feed.started(exec.index)
	}
	r.results[exec.index] = r.e.executePrepared(ctx, exec)
	if started {
		feed.finished(exec.index)
	}
	r.e.recordOutcome(call, r.results[exec.index])
	feed.ready(exec.index)
}

// resultFeed follows a running round for the UI: each call as it starts and
// ends, and each result in request order once it and every earlier one are
// known. Workers call it one at a time. A nil feed, for a round without a UI,
// does nothing.
type resultFeed struct {
	mu      sync.Mutex
	ui      ToolUI
	calls   []ToolCall
	results []ToolResult
	done    []bool
	next    int
}

// newResultFeed hands over at once the results of the calls that will not
// run, up to the first call that will.
func newResultFeed(ui ToolUI, calls []ToolCall, results []ToolResult, runnable []bool) *resultFeed {
	if ui == nil {
		return nil
	}
	f := &resultFeed{ui: ui, calls: calls, results: results, done: make([]bool, len(calls))}
	for i := range calls {
		f.done[i] = !runnable[i]
	}
	f.deliver()
	return f
}

func (f *resultFeed) started(index int) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ui.CallStarted(index, f.calls[index])
}

func (f *resultFeed) finished(index int) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ui.CallFinished(index)
}

// ready records that a call's result is in place and hands over every result
// now known in order.
func (f *resultFeed) ready(index int) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done[index] = true
	f.deliver()
}

func (f *resultFeed) deliver() {
	for f.next < len(f.calls) && f.done[f.next] {
		f.ui.ResultReady(f.next, len(f.calls), f.calls[f.next], f.results[f.next])
		f.next++
	}
}
