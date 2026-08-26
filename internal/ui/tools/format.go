package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"strings"
)

const (
	// DetailIndent aligns continuation lines under a call's first line. The
	// approval prompt is printed by cmd/lmc directly beneath the block this
	// package renders, so it has to indent to the same column.
	DetailIndent = "      "

	toolOutputChunkBytes = 32 * 1024
)

// CLIToolUI displays command review, execution, and result batches.
type CLIToolUI struct {
	notifier core.Notifier
}

// NewCLIToolUI creates a CLI renderer for tool execution batches.
func NewCLIToolUI(notifier core.Notifier) *CLIToolUI {
	return &CLIToolUI{notifier: notifier}
}

// ShowCall displays one call immediately before its approval decision. args is
// the executor's already-parsed view of the call; calls it never parsed, such
// as one past the per-round cap, arrive nil and are decoded here for display.
func (ui *CLIToolUI) ShowCall(index, total int, call core.ToolCall, args *core.UniversalCommandArgs) {
	if index == 0 {
		ui.notifier.Promptf("\n>>> Tools requested: %d\n", total)
	}
	if index > 0 {
		ui.notifier.Promptf("\n")
	}

	if args == nil {
		args = commandCallArgs(call)
	}

	prefix := fmt.Sprintf("[%d/%d]", index+1, total)
	if args != nil && len(args.Command) > 0 {
		ui.notifier.Promptf("%s Command: %s\n", prefix, core.MarshalJSONForDisplay(args.Command))
		if args.Workdir != "" {
			ui.notifier.Promptf("%sWorkdir: %s\n", DetailIndent, core.MarshalJSONForDisplay(args.Workdir))
		}
		if len(args.Environ) > 0 {
			ui.notifier.Promptf("%sEnviron: %s\n", DetailIndent, core.MarshalJSONForDisplay(args.Environ))
		}
		if args.Stdin != nil {
			ui.notifier.Promptf("%sStdin: %s\n", DetailIndent, core.MarshalJSONForDisplay(*args.Stdin))
		}
		if args.StdinFile != "" {
			ui.notifier.Promptf("%sStdin file: %s\n", DetailIndent, core.MarshalJSONForDisplay(args.StdinFile))
		}
		if args.StdoutFile != "" {
			ui.notifier.Promptf("%sStdout file: %s\n", DetailIndent, core.MarshalJSONForDisplay(args.StdoutFile))
		}
		if args.StderrFile != "" {
			ui.notifier.Promptf("%sStderr file: %s\n", DetailIndent, core.MarshalJSONForDisplay(args.StderrFile))
		}
		if args.Timeout > 0 {
			ui.notifier.Promptf("%sTimeout: %ds\n", DetailIndent, args.Timeout)
		}
		return
	}

	ui.notifier.Promptf("%s Tool: %s\n", prefix, call.Name)
	ui.notifier.Promptf("%sArguments: %s\n", DetailIndent, compactToolArguments(call.Args))
}

// BeforeRun announces execution after every approval has resolved.
func (ui *CLIToolUI) BeforeRun(total, runnable, parallel int) {
	if runnable == 0 {
		ui.notifier.Promptf("\n>>> No commands will be run.\n")
		return
	}

	var count string
	switch {
	case runnable != total:
		count = fmt.Sprintf("%d of %d commands", runnable, total)
	case runnable == 1:
		count = "1 command"
	default:
		count = fmt.Sprintf("%d commands", runnable)
	}
	parallelText := ""
	if parallel > 1 {
		parallelText = " in parallel"
	}
	ui.notifier.Promptf("\n>>> Running %s%s...\n", count, parallelText)
}

// AfterExecute displays results in their original request order. It decodes
// each call's arguments for itself rather than carrying ShowCall's copy
// forward: both phases run the same json.Unmarshal on the same bytes behind the
// same tool-name gate and nothing mutates the call in between, so the cache
// answered a question it was already answering — and a per-batch slice reset at
// the end of every round is state to keep aligned for a decode that costs one
// pass over arguments the round has already printed in full.
func (ui *CLIToolUI) AfterExecute(calls []core.ToolCall, results []core.ToolResult) {
	ui.notifier.Promptf("\n>>> Results:\n")
	total := len(calls)

	for i, result := range results {
		if i > 0 {
			ui.notifier.Promptf("\n")
		}
		formatToolResult(ui.notifier, i, total, commandCallArgs(calls[i]), result)
	}
	ui.notifier.Promptf("\n")
}

func formatToolResult(notifier core.Notifier, index, total int, args *core.UniversalCommandArgs, result core.ToolResult) {
	prefix := fmt.Sprintf("[%d/%d]", index+1, total)
	status, hints := toolResultStatus(result)
	details := toolResultDetails(args)
	// A redirected stream landed in a file, so capturing nothing is what was
	// asked for rather than something to report.
	redirected := args != nil && (args.StdoutFile != "" || args.StderrFile != "")
	if result.Error == "" && result.Output == "" && !redirected {
		details = append(details, "no captured output")
	}
	if result.Truncated {
		details = append(details, "output truncated at "+core.FormatByteCount(result.TruncatedTo))
	}
	if len(details) > 0 {
		status += " (" + strings.Join(details, "; ") + ")"
	}
	notifier.Promptf("%s %s\n", prefix, status)
	for _, hint := range hints {
		lines := strings.Split(hint, "\n")
		notifier.Promptf("%sHint: %s\n", DetailIndent, lines[0])
		// Continuation lines are indented by len("Hint: ") so a multi-line hint
		// reads as one hint under the detail column rather than as stray
		// unindented output.
		for _, line := range lines[1:] {
			notifier.Promptf("%s      %s\n", DetailIndent, line)
		}
	}

	if result.Output == "" {
		return
	}
	label := "Output"
	if result.Code == errors.ErrCodeTimeout {
		label = "Partial output"
	}
	notifier.Promptf("%s%s:\n", DetailIndent, label)
	writeToolOutput(notifier, result.Output)
}

// commandCallArgs decodes a universal_command call's arguments, and returns nil
// for any other tool or for arguments that do not decode.
func commandCallArgs(call core.ToolCall) *core.UniversalCommandArgs {
	if call.Name != core.UniversalCommandToolName {
		return nil
	}
	var args core.UniversalCommandArgs
	if json.Unmarshal(call.Args, &args) != nil {
		return nil
	}
	return &args
}

func toolResultDetails(args *core.UniversalCommandArgs) []string {
	if args == nil {
		return nil
	}

	details := make([]string, 0, 3)
	if args.Stdin != nil {
		details = append(details, "stdin provided")
	} else if args.StdinFile != "" {
		details = append(details, fmt.Sprintf("stdin file %s", core.MarshalJSONForDisplay(args.StdinFile)))
	}

	if args.StdoutFile != "" {
		details = append(details, fmt.Sprintf("stdout file %s", core.MarshalJSONForDisplay(args.StdoutFile)))
	}
	if args.StderrFile != "" {
		details = append(details, fmt.Sprintf("stderr file %s", core.MarshalJSONForDisplay(args.StderrFile)))
	}
	return details
}

// toolResultStatus renders the executor's verdict. Whether the command ever
// ran is read off the result rather than reconstructed from its error code:
// this switch used to carry its own list of the six codes that meant "never
// ran", plus "Reason is set" to split a pre-run cancellation from an
// interrupted one, and nothing tied either to the executor. Nine untyped
// string constants, no exhaustiveness check, and one changeset that added two
// codes was enough for the list to be wrong — a code missing from it printed
// "Failed in 0ms" and dropped the executor's hints, and every command os/exec
// declined to start printed that too, having no denial code to be listed under.
//
// What remains keyed on Code is the two ways a command that did run was
// stopped, which is a distinction about the run and not about whether there
// was one.
func toolResultStatus(result core.ToolResult) (string, []string) {
	if result.NotRun {
		// The executor owns why the command never ran, and says so in the
		// result's structured reason and hints when it recorded them, or in its
		// error text otherwise. Restating either here would send half these
		// readers after a flag they never passed.
		if result.Reason != "" {
			return "Not run: " + result.Reason, result.Hints
		}
		return "Not run: " + result.Error, result.Hints
	}

	if result.Error == "" {
		return fmt.Sprintf("Completed in %dms", result.Elapsed), nil
	}

	switch result.Code {
	case errors.ErrCodeTimeout:
		return fmt.Sprintf("Timed out in %dms", result.Elapsed), nil
	case errors.ErrCodeCancelled:
		return fmt.Sprintf("Cancelled in %dms", result.Elapsed), nil
	}
	return fmt.Sprintf("Failed in %dms: %s", result.Elapsed, result.Error), nil
}

func compactToolArguments(args json.RawMessage) string {
	if len(args) == 0 {
		return "{}"
	}
	var compact bytes.Buffer
	if json.Compact(&compact, args) == nil {
		return compact.String()
	}
	return string(args)
}

// writeToolOutput forwards the executor's already-bounded output in small
// writes. Promptf uses fmt.Fprintf, which buffers each formatted call, so one
// call containing the whole result would still duplicate a large output. The
// caller guarantees output is non-empty; the trailing-newline check reads the
// last byte.
func writeToolOutput(notifier core.Notifier, output string) {
	for len(output) > toolOutputChunkBytes {
		notifier.Promptf("%s", output[:toolOutputChunkBytes])
		output = output[toolOutputChunkBytes:]
	}
	notifier.Promptf("%s", output)
	if output[len(output)-1] != '\n' {
		notifier.Promptf("\n")
	}
}
