package session

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
)

type PendingToolMode int

const (
	PendingToolSkip PendingToolMode = iota
	PendingToolExecute
	PendingToolPreview
)

type pendingToolResolution struct {
	HasPending     bool
	PreviewCalls   []core.ToolCall
	PreviewResults []core.ToolResult
	AdditionalText string
}

// ExecutePendingTools checks for and executes any pending tool calls from a previous session
// This centralizes the logic to avoid duplication between checking and executing
//
// Returns:
//   - hasPendingTools: true if pending tools were found (regardless of execution success), false if no pending tools
//   - err: non-nil if execution or saving failed (hasPendingTools will still be true if tools were found)
//
// The hasPendingTools return value indicates whether pending tools existed, NOT whether they executed successfully.
// This is used by callers to determine if empty input is acceptable (when continuing tool execution).
func ExecutePendingTools(ctx context.Context, sess *Session, cfg core.RequestOptions, log core.Logger, ui core.ToolUI, approver core.Approver) (hasPendingTools bool, err error) {
	resolution, err := resolvePendingTools(ctx, sess, cfg, log, ui, approver, PendingToolExecute)
	if err != nil || !resolution.HasPending || len(resolution.PreviewResults) == 0 {
		return resolution.HasPending, err
	}
	if err := commitPendingToolResults(ctx, sess, resolution, log); err != nil {
		return true, err
	}
	return true, nil
}

func resolvePendingTools(ctx context.Context, sess *Session, cfg core.RequestOptions, log core.Logger, ui core.ToolUI, approver core.Approver, mode PendingToolMode) (pendingToolResolution, error) {
	if sess == nil || mode == PendingToolSkip {
		return pendingToolResolution{}, nil
	}

	pendingTools, err := CheckForPendingToolCalls(ctx, sess.Path)
	if err != nil {
		// Log the error but continue - this is non-critical
		if log != nil && log.IsDebugEnabled() {
			log.Debugf("Failed to check pending tools for session %s: %v", GetSessionID(sess.Path), err)
		}
		return pendingToolResolution{}, nil
	}

	if len(pendingTools) == 0 {
		if log != nil {
			log.Debugf("No pending tools found in session %s", GetSessionID(sess.Path))
		}
		return pendingToolResolution{}, nil
	}

	if !cfg.ToolEnabled {
		return pendingToolResolution{HasPending: true}, errors.WrapError("execute pending tools", fmt.Errorf("pending tool calls require -tool to continue"))
	}

	if mode == PendingToolPreview {
		return pendingToolResolution{
			HasPending:     true,
			PreviewCalls:   pendingTools,
			PreviewResults: placeholderPendingToolResults(pendingTools),
		}, nil
	}

	if log != nil {
		log.Debugf("Executing %d pending tool call(s) from previous session", len(pendingTools))
	}

	// Execute the pending tools
	executor, err := core.NewExecutor(cfg, log, approver)
	if err != nil {
		return pendingToolResolution{HasPending: true}, errors.WrapError("create executor for pending tools", err)
	}

	// Review, approve, execute, and display pending calls through the same UI
	// lifecycle used by new tool rounds. The UI is injected from the edge so
	// this storage-layer package never decides where operator output goes.
	results := executor.ExecuteParallel(ctx, pendingTools, ui)

	// Check for truncation and prepare additional text
	additionalText := core.BuildTruncationNotes(results, pendingTools)

	// Stage tool results for the provider request. They are committed only after
	// the provider response succeeds so failed follow-up requests do not make the
	// session look as if the pending tools were consumed.
	return pendingToolResolution{
		HasPending:     true,
		PreviewCalls:   pendingTools,
		PreviewResults: results,
		AdditionalText: additionalText,
	}, nil
}

func commitPendingToolResults(ctx context.Context, sess *Session, resolution pendingToolResolution, log core.Logger) error {
	if sess == nil || len(resolution.PreviewResults) == 0 {
		return nil
	}

	result, err := SaveToolResults(ctx, sess, resolution.PreviewResults, resolution.AdditionalText)
	if err != nil {
		return errors.WrapError("save pending tool results", err)
	}
	if result.Path != sess.Path {
		sess.Path = result.Path
		if log != nil {
			log.Debugf("Tool results saved to sibling branch %s as message %s",
				GetSessionID(result.Path), result.MessageID)
		}
	}
	return nil
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
