package session

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/ui/tools"
)

// ExecutePendingTools checks for and executes any pending tool calls from a previous session
// This centralizes the logic to avoid duplication between checking and executing
//
// Returns:
//   - hasPendingTools: true if pending tools were found (regardless of execution success), false if no pending tools
//   - err: non-nil if execution or saving failed (hasPendingTools will still be true if tools were found)
//
// The hasPendingTools return value indicates whether pending tools existed, NOT whether they executed successfully.
// This is used by callers to determine if empty input is acceptable (when continuing tool execution).
func ExecutePendingTools(ctx context.Context, sess *Session, cfg core.ToolConfig, log core.Logger, notifier core.Notifier, approver core.Approver) (hasPendingTools bool, err error) {
	pendingTools, err := CheckForPendingToolCalls(ctx, sess.Path)
	if err != nil {
		// Log the error but continue - this is non-critical
		if log != nil && log.IsDebugEnabled() {
			log.Debugf("Failed to check pending tools for session %s: %v", GetSessionID(sess.Path), err)
		}
		return false, nil
	}

	if len(pendingTools) == 0 {
		log.Debugf("No pending tools found in session %s", GetSessionID(sess.Path))
		return false, nil
	}

	if !cfg.IsToolEnabled() {
		return true, errors.WrapError("execute pending tools", fmt.Errorf("pending tool calls require -tool to continue"))
	}

	// Notify user about pending tool execution
	notifier.Infof("Executing %d pending tool call(s) from previous session", len(pendingTools))
	log.Debugf("Executing %d pending tool call(s) from previous session", len(pendingTools))

	// Execute the pending tools
	executor, err := core.NewExecutor(cfg, log, notifier, approver)
	if err != nil {
		return true, errors.WrapError("create executor for pending tools", err)
	}

	// Create a CLI Tool UI implementation for display
	ui := tools.NewCLIToolUI(notifier, cfg)

	// Display tool calls before execution
	ui.BeforeExecute(pendingTools)

	// Execute tools in parallel
	results := executor.ExecuteParallel(ctx, pendingTools)

	// Display results
	ui.AfterExecute(results)

	// Check for truncation and prepare additional text
	additionalText := core.BuildTruncationNotes(results, pendingTools)

	// Save tool results to session
	result, err := SaveToolResults(ctx, sess, results, additionalText)
	if err != nil {
		return true, errors.WrapError("save pending tool results", err)
	}
	path := result.Path
	msgID := result.MessageID

	// Update session path if sibling was created
	if path != sess.Path {
		sess.Path = path
		log.Debugf("Tool results saved to sibling branch %s as message %s",
			GetSessionID(path), msgID)
	}

	return true, nil
}
