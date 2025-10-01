package ui

import (
	"fmt"
	"io"
	"lmtools/internal/core"
	"os"
)

// StdNotifier implements the core.Notifier interface for standard output
type StdNotifier struct {
	out io.Writer
}

// NewNotifier creates a notifier that writes to stderr
func NewNotifier() *StdNotifier {
	return &StdNotifier{out: os.Stderr}
}

// NewNotifierWithWriter creates a notifier with a custom writer (useful for testing)
func NewNotifierWithWriter(w io.Writer) *StdNotifier {
	return &StdNotifier{out: w}
}

// Infof prints an informational message with "Note:" prefix
func (n *StdNotifier) Infof(format string, args ...interface{}) {
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
