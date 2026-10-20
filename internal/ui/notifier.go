package ui

import (
	"fmt"
	"io"
	"lmtools/internal/core"
	"os"
)

// StdNotifier implements core.Notifier on the CLI diagnostic stream.
type StdNotifier struct {
	out       io.Writer
	plainInfo bool
	hideUsage bool
	response  int
}

// NewNotifier creates a notifier that writes to stderr
func NewNotifier() *StdNotifier {
	return &StdNotifier{out: os.Stderr}
}

// NewNotifierWithWriter creates a notifier with a custom writer (useful for testing)
func NewNotifierWithWriter(w io.Writer) *StdNotifier {
	return &StdNotifier{out: w}
}

// NewREPLNotifier keeps routine state messages compact on the conversation surface.
func NewREPLNotifier(w io.Writer) *StdNotifier {
	return &StdNotifier{out: w, plainInfo: true}
}

func (n *StdNotifier) BeginTurn() { n.response = 0 }

func (n *StdNotifier) SetUsageEnabled(enabled bool) { n.hideUsage = !enabled }

// ReportUsage identifies model responses within a tool turn, without treating
// cache or reasoning counts as additional tokens or inventing missing totals.
func (n *StdNotifier) ReportUsage(usage *core.Usage) {
	n.response++
	if n.hideUsage || usage == nil {
		return
	}
	label := "Tokens"
	if n.plainInfo && n.response > 1 {
		label = fmt.Sprintf("Tokens (response %d)", n.response)
	}
	n.Promptf("%s: %s\n", label, usage.DisplaySummary())
}

// Infof prints an informational message with "Note:" prefix
func (n *StdNotifier) Infof(format string, args ...interface{}) {
	if n.plainInfo {
		fmt.Fprintf(n.out, format+"\n", args...)
		return
	}
	fmt.Fprintf(n.out, "Note: "+format+"\n", args...)
}

// Warnf prints a warning message with "Warning:" prefix
func (n *StdNotifier) Warnf(format string, args ...interface{}) {
	fmt.Fprintf(n.out, "Warning: "+format+"\n", args...)
}

// Errorf prints an error message with "Error:" prefix
func (n *StdNotifier) Errorf(format string, args ...interface{}) {
	fmt.Fprintf(n.out, "Error: "+format+"\n", args...)
}

// Promptf prints a prompt without any prefix (for interactive prompts)
func (n *StdNotifier) Promptf(format string, args ...interface{}) {
	fmt.Fprintf(n.out, format, args...)
}

// Ensure StdNotifier implements core.Notifier
var _ core.Notifier = (*StdNotifier)(nil)
