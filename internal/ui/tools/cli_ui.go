package tools

import (
	"lmtools/internal/core"
)

// CLIToolUI implements the core.ToolUI interface for CLI display
type CLIToolUI struct {
	notifier core.Notifier
	cfg      core.RequestOptions
}

// NewCLIToolUI creates a new CLI tool UI instance
func NewCLIToolUI(notifier core.Notifier, cfg core.RequestOptions) *CLIToolUI {
	return &CLIToolUI{
		notifier: notifier,
		cfg:      cfg,
	}
}

// BeforeExecute displays tool calls before execution
func (ui *CLIToolUI) BeforeExecute(calls []core.ToolCall) {
	FormatToolCalls(ui.notifier, calls)
}

// AfterExecute displays tool results after execution
func (ui *CLIToolUI) AfterExecute(results []core.ToolResult) {
	FormatToolResults(ui.notifier, ui.cfg, results)
}
