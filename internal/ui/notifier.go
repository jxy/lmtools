package ui

import (
	"fmt"
	"io"
	"lmtools/internal/core"
	"os"
	"time"
)

// StdNotifier implements core.Notifier on the CLI diagnostic stream.
type StdNotifier struct {
	out       io.Writer
	plainInfo bool
	hideUsage bool
	response  int
	// elapsed, when set, measures from the request a response answers, for
	// the usage line.
	elapsed func() time.Duration
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

// ReportCheckpointUsage keeps maintenance inference out of user-response numbering.
func (n *StdNotifier) ReportCheckpointUsage(u *core.Usage) {
	if !n.hideUsage && u != nil {
		n.Promptf("Checkpoint tokens: %s\n", u.DisplaySummary())
	}
}

// SetElapsedSource makes the usage line report the time from the request to
// the response's end, as source measures it.
func (n *StdNotifier) SetElapsedSource(source func() time.Duration) { n.elapsed = source }

// ReportUsage reports one response's usage, without treating cache or
// reasoning counts as additional tokens or inventing missing totals. Every
// response counts, those without counts included. In the REPL every response
// of a turn with tool calls is numbered, the first included, since a response
// that asked for tools is followed by another.
func (n *StdNotifier) ReportUsage(usage *core.Usage, toolCalls bool) {
	n.response++
	if n.hideUsage || usage == nil {
		return
	}
	label := "Tokens"
	if n.plainInfo && (n.response > 1 || toolCalls) {
		label = fmt.Sprintf("Tokens (response %d)", n.response)
	}
	line := label + ": " + usage.DisplaySummary()
	if n.elapsed != nil {
		if d := n.elapsed(); d > 0 {
			line += " | " + formatResponseTime(d)
		}
	}
	n.Promptf("%s\n", line)
}

// formatResponseTime gives tenths of a second under a minute.
func formatResponseTime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
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
