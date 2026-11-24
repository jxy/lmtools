package session

import (
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
)

// Settings are a conversation's settings: what the operator set with -s,
// -builtin-system, -notebook, -notebook-view and the -context-* flags. A
// system record holds them, and they stay in force for every later message
// of its lineage until another record replaces them. An empty value is a
// default, which each run resolves for itself.
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
	Early   *bool
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

// SystemPrompt returns the system prompt a request under s sends: the custom
// prompt, or the built-in prompt for the tools opts advertises, the default
// prompt without tools and the tool prompt with them, each followed by what
// the run's MCP servers add, which a custom prompt gets too. An empty prompt
// sends no system message.
func SystemPrompt(s Settings, opts core.RequestOptions) string {
	var base string
	switch {
	case s.System != nil:
		base = *s.System
	case opts.ToolEnabled:
		base = core.ToolSystemPromptFor(opts.ExcludedTools)
	default:
		base = prompts.DefaultSystemPrompt
	}
	if opts.ToolEnabled {
		base += core.MCPSystemPromptAddendum(opts.MCP)
	}
	return base
}
