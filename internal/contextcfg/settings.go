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

type Binding struct {
	Settings
	// NotebookDefault records the user's implicit policy choice, including
	// when this particular model has no discoverable window.
	NotebookDefault bool   `json:"notebook_default,omitempty"`
	WindowModel     string `json:"window_model,omitempty"`
	WindowProvider  string `json:"window_provider,omitempty"`
	WindowCap       int    `json:"window_cap,omitempty"`
	// Initial is the actual background text sent in the first context window.
	Initial string `json:"initial,omitempty"`
}
