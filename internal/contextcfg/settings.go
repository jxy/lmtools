// Package contextcfg describes portable settings that a saved session inherits.
package contextcfg

import "fmt"

const (
	Full     = "full"
	Archive  = "archive"
	Handoff  = "handoff"
	Verified = "verified"
	Adaptive = "adaptive"
)

type Settings struct {
	Notebook string `json:"notebook,omitempty"`
	Policy   string `json:"policy,omitempty"`
	Window   int    `json:"window,omitempty"`
	Trigger  int    `json:"trigger,omitempty"`
	View     string `json:"view,omitempty"`
	Format   string `json:"format,omitempty"`
	Early    bool   `json:"early,omitempty"`
}

func (s Settings) Enabled() bool {
	return s.Notebook != "" || s.Policy != "" || s.Window != 0 || s.Trigger != 0 || s.View != "" || s.Format != "" || s.Early
}
func (s Settings) Compacts() bool { return s.Policy != "" && s.Policy != Full }

func (s Settings) NotebookView() string {
	if s.View == "" {
		return "catalog"
	}
	return s.View
}

func (s Settings) ExperimentalCompaction() bool {
	return s.Policy == Archive || s.Policy == Adaptive || s.Policy == Verified || (s.Policy == Handoff && s.Format == "sections")
}

// Ceiling reserves one eighth for output (including reasoning), framing and estimation error.
func (s Settings) Ceiling() int { return s.Window - s.Window/8 }

func (s Settings) Threshold() int {
	if s.Trigger > 0 {
		return s.Trigger
	}
	return s.Window / 10 * 7
}

// Validate checks the on-disk schema, including settings from benchmark builds.
func (s Settings) Validate() error {
	switch s.Format {
	case "", "prose", "sections":
	default:
		return fmt.Errorf("unknown -context-format %q", s.Format)
	}
	switch s.Policy {
	case "", Full, Archive, Handoff, Verified, Adaptive:
	default:
		return fmt.Errorf("unknown -context-policy %q", s.Policy)
	}
	switch s.View {
	case "", "catalog", "tools", "full":
	default:
		return fmt.Errorf("unknown -notebook-view %q", s.View)
	}
	if s.Notebook == "" && s.View != "" {
		return fmt.Errorf("-notebook-view requires a notebook")
	}
	if s.Window < 0 || s.Window > 1000000000 || (s.Compacts() || s.Window != 0) && s.Window < 2048 {
		return fmt.Errorf("set -context-window to the model's context size (2048 to 1000000000 tokens)")
	}
	if s.Trigger < 0 || s.Trigger != 0 && (!s.Compacts() || s.Trigger >= s.Ceiling()) {
		return fmt.Errorf("-context-trigger must be below the input ceiling and requires a compaction policy")
	}
	return nil
}

// Capacity is a model's context capacity in tokens, with the model and the
// endpoint it was found for.
type Capacity struct {
	Tokens   int    `json:"tokens"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
}

// CheckpointRef identifies a saved checkpoint by the message it covers
// through and the tag under which it was published.
type CheckpointRef struct {
	Prefix string `json:"prefix"`
	Tag    string `json:"tag"`
}

// Binding is the context configuration of one run: the settings in force,
// with the window the run resolved.
type Binding struct {
	Settings
	// ManualCheckpoint is the checkpoint the operator selected on this branch.
	// It supplies a starting context even when automatic cuts are off.
	ManualCheckpoint *CheckpointRef
	// NotebookDefault records that the policy is the default of a notebook
	// conversation: handoff when a window is known, full replay otherwise.
	NotebookDefault bool
	// WindowCap is the operator's cap on the window, zero for none. The
	// run's window, Settings.Window, is the smaller of the cap and the
	// capacity when both are known, and the one known otherwise.
	WindowCap int
	// Capacity is the model's capacity the run found, which its replies
	// record for later runs.
	Capacity Capacity
	// Initial is the notebook catalog a window starting at the beginning of
	// the conversation sends.
	Initial string
}
