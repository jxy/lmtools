package tools

import (
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/errors"
)

// ToolArgs represents the arguments for the universal_command tool
type ToolArgs struct {
	Command []string          `json:"command"`
	Environ map[string]string `json:"environ"`
	Workdir string            `json:"workdir"`
	Timeout int               `json:"timeout"`
}

// FormatToolCall formats and displays a tool call before execution
func FormatToolCall(notifier core.Notifier, call core.ToolCall) {
	// Display tool call header
	notifier.Infof("\n>>> Tool: %s [%s]", call.Name, call.ID)

	// Parse and display command info
	var args ToolArgs
	if err := json.Unmarshal(call.Args, &args); err == nil && len(args.Command) > 0 {
		cmdJSON, _ := json.Marshal(args.Command)
		notifier.Infof(">>> Command: %s", string(cmdJSON))
		if len(args.Environ) > 0 {
			envJSON, _ := json.Marshal(args.Environ)
			notifier.Infof(">>> Environ: %s", string(envJSON))
		}
		if args.Workdir != "" {
			notifier.Infof(">>> Workdir: %q", args.Workdir)
		}
		if args.Timeout > 0 {
			notifier.Infof(">>> Timeout: %d", args.Timeout)
		}
	}
}

// FormatToolResult formats and displays a tool result after execution.
func FormatToolResult(notifier core.Notifier, cfg core.RequestOptions, result core.ToolResult) {
	if result.Error != "" {
		// Use centralized error explanation
		explanation := errors.ExplainToolError(result.Code, result.Error, cfg.GetToolWhitelist())

		// Display primary error message
		notifier.Errorf(">>> Error: %s", explanation.Message)

		// Display hints
		for _, hint := range explanation.Hints {
			notifier.Infof(">>> %s", hint)
		}

		// Show output if available (except for timeout, which is handled specially)
		if result.Output != "" && result.Code != errors.ErrCodeTimeout {
			notifier.Infof(">>> Output:\n%s", result.Output)
		} else if result.Code == errors.ErrCodeTimeout && result.Output != "" {
			notifier.Infof(">>> Partial output before timeout:\n%s", result.Output)
		}

		notifier.Infof(">>> Failed in %dms", result.Elapsed)
	} else {
		if result.Output != "" {
			notifier.Infof(">>> Output:\n%s", result.Output)
		}
		if result.Truncated {
			notifier.Infof("Output was truncated to %dMB",
				core.DefaultMaxOutputSize/(1024*1024))
		}
		notifier.Infof(">>> Completed in %dms", result.Elapsed)
	}
}

// FormatToolCalls formats multiple tool calls before execution
func FormatToolCalls(notifier core.Notifier, calls []core.ToolCall) {
	for _, call := range calls {
		FormatToolCall(notifier, call)
	}
	// Display execution message
	notifier.Infof(">>> Executing...")
}

// FormatToolResults formats multiple tool results after execution.
func FormatToolResults(notifier core.Notifier, cfg core.RequestOptions, results []core.ToolResult) {
	for _, result := range results {
		FormatToolResult(notifier, cfg, result)
	}
}
