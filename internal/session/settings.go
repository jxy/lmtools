package session

import (
	"encoding/json"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
)

// Settings are a conversation's settings: what the operator set with
// -system, -builtin-system, -notebook, -notebook-view and the -context-*
// flags. A system record holds them, and they stay in force for every later
// message of its lineage until another record replaces them. An empty value
// is a default, which each run resolves for itself.
type Settings struct {
	// System is the custom system prompt; nil stands for the built-in
	// prompt, which each run builds from the tools it advertises. An empty
	// custom prompt sends no system message.
	System   *string `json:"system,omitempty"`
	Notebook string  `json:"notebook,omitempty"`
	View     string  `json:"notebook_view,omitempty"`
	Policy   string  `json:"policy,omitempty"`
	Format   string  `json:"format,omitempty"`
	// Window caps the context window; zero leaves the model's capacity.
	Window  int   `json:"window_cap,omitempty"`
	Trigger int   `json:"trigger,omitempty"`
	Early   *bool `json:"early_cuts,omitempty"`
	// Catalog is the notebook catalog snapshot a window that starts at the
	// beginning of the conversation sends.
	Catalog string `json:"catalog,omitempty"`
	// ManualCheckpoint selects the shortened context from this record on.
	ManualCheckpoint *contextcfg.CheckpointRef `json:"manual_checkpoint,omitempty"`
}

// Change is what a run's settings flags set. A nil field leaves its setting
// as it is.
type Change struct {
	// System sets a custom prompt; Builtin restores the built-in one.
	System  *string
	Builtin bool
	// Notebook sets the notebook; an empty one detaches it, which also
	// clears the view and the catalog.
	Notebook *string
	View     *string
	Policy   *string
	Format   *string
	// Window sets the cap; zero removes it.
	Window *int
	// Trigger sets the trigger; zero restores the default.
	Trigger *int
	// Early sets early cuts; DefaultEarly returns them to their default,
	// which each run resolves from the notebook and the policy.
	Early        *bool
	DefaultEarly bool
	// Catalog is the snapshot the caller took of the notebook, which a
	// record keeps when the notebook or the view changes.
	Catalog *string
}

// Apply returns the settings with c applied and the names of the settings
// it changed, empty when it changed none.
func (s Settings) Apply(c Change) (Settings, []string) {
	next := s
	if c.Builtin {
		next.System = nil
	} else if c.System != nil {
		text := *c.System
		next.System = &text
	}
	set := func(field *string, value *string) {
		if value != nil {
			*field = *value
		}
	}
	set(&next.Notebook, c.Notebook)
	set(&next.View, c.View)
	set(&next.Policy, c.Policy)
	if c.Policy != nil && *c.Policy == contextcfg.Full {
		next.ManualCheckpoint = nil
	}
	set(&next.Format, c.Format)
	if next.Notebook != s.Notebook || next.View != s.View {
		next.Catalog = ""
		set(&next.Catalog, c.Catalog)
	}
	if c.Window != nil {
		next.Window = *c.Window
	}
	if c.Trigger != nil {
		next.Trigger = *c.Trigger
	}
	if c.Early != nil {
		early := *c.Early
		next.Early = &early
	} else if c.DefaultEarly {
		next.Early = nil
	}
	if next.Notebook == "" {
		next.View, next.Catalog = "", ""
	}

	var changed []string
	differs := func(name string, a, b any) {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if string(x) != string(y) {
			changed = append(changed, name)
		}
	}
	differs("system prompt", s.System, next.System)
	differs("notebook", s.Notebook, next.Notebook)
	differs("notebook view", s.View, next.View)
	differs("context policy", s.Policy, next.Policy)
	differs("context format", s.Format, next.Format)
	differs("context window cap", s.Window, next.Window)
	differs("context trigger", s.Trigger, next.Trigger)
	differs("early cuts", s.Early, next.Early)
	if s.ManualCheckpoint != next.ManualCheckpoint {
		changed = append(changed, "manual checkpoint")
	}
	return next, changed
}

// SettingsAt returns the settings in force at the end of lineage: those of
// its newest system record, or the defaults when it holds none.
func SettingsAt(lineage []Message) Settings {
	for i := len(lineage) - 1; i >= 0; i-- {
		if lineage[i].Settings != nil {
			return *lineage[i].Settings
		}
	}
	return Settings{}
}

// SettingsAt returns the settings in force at the message at names, or the
// defaults for a session without messages.
func (s *Store) SettingsAt(at Ref) (Settings, error) {
	if at.Message == "" {
		if _, err := s.Locate(at); err != nil {
			return Settings{}, err
		}
		return Settings{}, nil
	}
	lineage, err := s.Lineage(at)
	if err != nil {
		return Settings{}, err
	}
	return SettingsAt(lineage), nil
}

// SystemPrompt returns the system message a request under s sends: the
// conversation's prompt, then what the tools opts advertises add. The prompt
// is the custom one, or the built-in opening for the run's tools: the
// default prompt without tools. The tools add the rules for
// universal_command, or a shorter guidance when the run withholds it, then
// what the run's MCP servers add. A custom prompt sets only the first part,
// so an empty one with tools sends the tool texts alone, and without tools
// no system message.
func SystemPrompt(s Settings, opts core.RequestOptions) string {
	if !opts.ToolEnabled {
		if s.System != nil {
			return *s.System
		}
		return prompts.DefaultSystemPrompt
	}
	opening, tools := core.ToolPrompt(opts.ExcludedTools)
	if s.System != nil {
		opening = *s.System
	}
	if opening != "" {
		tools = opening + "\n\n" + tools
	}
	return tools + core.MCPSystemPromptAddendum(opts.MCP)
}
