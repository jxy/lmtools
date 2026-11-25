package config

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/mcp"
	"lmtools/internal/notebook"
	"lmtools/internal/session"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func registerContextFlags(fs *flag.FlagSet, c *Config) {
	fs.StringVar(&c.Context.Notebook, "notebook", "", "bind a directory of Markdown notes to this session")
	registerContextChoices(fs, &c.Context.Settings)
	fs.IntVar(&c.Context.Window, "context-window", 0, "cap on the context window in tokens; a run takes the model's capacity when it is smaller, and 0 removes the cap")
	fs.IntVar(&c.Context.Trigger, "context-trigger", 0, "estimated input tokens at which to compact (default 70% of the window); 0 restores the default")
	fs.BoolVar(&c.Context.Early, "context-early-cuts", false, "allow model phase hints in handoff mode (on by default with a notebook)")
}

// explicitFlags names the flags the command line set.
func explicitFlags(fs *flag.FlagSet) map[string]bool {
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	return explicit
}

// resolveStart resolves -resume to the message a run continues after, once,
// before settings are read or tools connect. A bare session stands for its
// newest message, on whichever branch it is, or for the session itself when
// it has none; no -resume starts a new session.
func resolveStart(store *session.Store, resume string) (session.Ref, error) {
	if resume == "" {
		return session.Ref{}, nil
	}
	ref, err := session.ParseRef(resume)
	if err != nil {
		return session.Ref{}, err
	}
	located, err := store.Locate(ref)
	if errors.Is(err, session.ErrOldSession) {
		return session.Ref{}, fmt.Errorf("%w; move it out of %s or delete it with -delete %s", err, store.Dir(), ref.Session)
	}
	if err != nil {
		return session.Ref{}, err
	}
	if located.Message == "" {
		located.Message, err = store.Newest(located.Session)
	}
	return located, err
}

// settingsChange is what the run's settings flags set: -system, -builtin-system,
// -notebook, -notebook-view and the -context-* flags. A policy other than
// the one in force at the start point also clears the format, returns early
// cuts to their default, and clears the trigger when it stops compaction,
// unless the run sets them too. A policy the run sets where the settings in
// force are experimental clears the format as well, so naming the policy in
// force leaves an experimental writer.
func settingsChange(c *Config, explicit map[string]bool, at session.Settings) (session.Change, error) {
	var ch session.Change
	if explicit["system"] {
		system := c.System
		ch.System = &system
	}
	ch.Builtin = c.BuiltinSystem
	if explicit["notebook"] {
		notebook := c.Context.Notebook
		if notebook != "" {
			abs, err := filepath.Abs(notebook)
			if err != nil {
				return ch, err
			}
			notebook = abs
		}
		ch.Notebook = &notebook
	}
	text := func(name, value string) *string {
		if !explicit[name] {
			return nil
		}
		return &value
	}
	number := func(name string, value int) *int {
		if !explicit[name] {
			return nil
		}
		return &value
	}
	ch.View = text("notebook-view", c.Context.View)
	ch.Policy = text("context-policy", c.Context.Policy)
	ch.Format = text("context-format", c.Context.Format)
	ch.Window = number("context-window", c.Context.Window)
	ch.Trigger = number("context-trigger", c.Context.Trigger)
	if explicit["context-early-cuts"] {
		early := c.Context.Early
		ch.Early = &early
	}
	experimental := contextcfg.Settings{Policy: at.Policy, Format: at.Format}.ExperimentalCompaction()
	if ch.Policy != nil && ch.Format == nil && (*ch.Policy != at.Policy || experimental) {
		ch.Format = new(string)
	}
	if ch.Policy != nil && *ch.Policy != at.Policy {
		if ch.Trigger == nil && !(contextcfg.Settings{Policy: *ch.Policy}).Compacts() {
			ch.Trigger = new(int)
		}
		ch.DefaultEarly = ch.Early == nil
	}
	return ch, nil
}

// runBinding resolves the defaults of settings for this run. An empty
// policy with a notebook is handoff, which the window discovery makes full
// replay when no window is known; unset early cuts are on for a notebook
// under handoff. at are the settings the run started from.
func runBinding(next, at session.Settings, explicit map[string]bool) (contextcfg.Binding, error) {
	b := contextcfg.Binding{
		Settings: contextcfg.Settings{
			Notebook: next.Notebook, View: next.View, Policy: next.Policy, Format: next.Format,
			Window: next.Window, Trigger: next.Trigger,
		},
		WindowCap:        next.Window,
		Initial:          next.Catalog,
		ManualCheckpoint: next.ManualCheckpoint,
	}
	if b.Policy == "" && b.Notebook != "" {
		b.Policy, b.NotebookDefault = contextcfg.Handoff, true
	}
	if next.Early != nil {
		b.Early = *next.Early
	} else {
		b.Early = b.Notebook != "" && b.Policy == contextcfg.Handoff
	}
	if explicit["notebook-view"] && b.Notebook == "" {
		return b, fmt.Errorf("-notebook-view requires a notebook")
	}
	if explicit["notebook-view"] && b.View == "full" && at.View != "full" {
		return b, fmt.Errorf("the full notebook view is available in lmc-context-bench; choose catalog or tools")
	}
	if b.ExperimentalCompaction() {
		if !explicit["context-policy"] {
			return b, fmt.Errorf("saved session uses experimental context settings (policy %q, format %q); resume with -context-policy handoff or full, or use lmc-context-bench for the original settings", b.Policy, b.Format)
		}
		return b, fmt.Errorf("set -context-policy to full or handoff (experimental policies are available in lmc-context-bench)")
	}
	if explicit["context-early-cuts"] && b.Early && b.Policy != contextcfg.Handoff && b.Policy != contextcfg.Adaptive {
		return b, fmt.Errorf("-context-early-cuts requires a handoff policy that accepts phase hints")
	}
	check := b.Settings
	if check.Window == 0 && check.Compacts() {
		// The history or the authenticated model catalog supplies the
		// model's capacity after startup.
		check.Window = 1000000000
	}
	return b, check.Validate()
}

// configureContext resolves where a chat run starts and the settings it
// runs under: those in force at its start point with the run's settings
// flags applied, which the run's commit records when they differ. A new
// notebook or view gets a catalog snapshot, which windows starting at the
// beginning of the conversation send.
func configureContext(c *Config, fs *flag.FlagSet) error {
	if c.Embed || c.ShowSessions || c.Show != "" || c.Delete != "" || c.ListModels {
		if c.Context.Enabled() {
			return fmt.Errorf("notebook and context options require chat mode")
		}
		return nil
	}
	explicit := explicitFlags(fs)
	var at session.Settings
	if !c.NoSession {
		store := c.Store()
		start, err := resolveStart(store, c.Resume)
		if err != nil {
			return err
		}
		c.Start = start
		if start.Session != "" {
			// Reading the settings also checks that the message exists.
			if at, err = store.SettingsAt(start); err != nil {
				return err
			}
		}
	}
	change, err := settingsChange(c, explicit, at)
	if err != nil {
		return err
	}
	next, _ := at.Apply(change)
	if next.Notebook != "" && (next.Notebook != at.Notebook || next.View != at.View) {
		catalog, err := notebook.Snapshot(context.Background(), next.Notebook, next.View)
		if err != nil {
			return err
		}
		change.Catalog = &catalog
		next, _ = at.Apply(change)
	}
	c.Change, c.Settings = change, next
	if c.Context, err = runBinding(next, at, explicit); err != nil {
		return err
	}
	if !c.Context.Enabled() {
		return nil
	}
	// A text-only compactor works on providers without tools. Notebook users
	// get scoped memory tools without implicitly enabling process execution.
	if c.Context.Notebook == "" && !c.EnableTool && len(c.ToolInclude) == 0 {
		return nil
	}
	if !c.EnableTool && len(c.ToolInclude) == 0 {
		c.implicitNotebookTools = true
		c.ToolInclude = []string{"history"}
		if c.Context.Notebook != "" {
			c.ToolInclude = append(c.ToolInclude, "notebook")
		}
	}
	c.EnableTool = true
	for _, server := range c.MCPServers {
		if strings.HasPrefix(mcp.QualifiedName(server.Name, ""), mcp.QualifiedName("history", "")) || (server.Name == "notebook" && c.Context.Notebook != "") {
			return fmt.Errorf("MCP name %q is already in use by session context tools", server.Name)
		}
	}
	c.MCPServers = append(c.MCPServers, mcp.ServerConfig{Name: "history", Type: "local"})
	if c.Context.Notebook != "" {
		c.MCPServers = append(c.MCPServers, mcp.ServerConfig{Name: "notebook", Type: "stdio", Command: "notebook", Args: []string{"-root", c.Context.Notebook}})
	}
	return nil
}

// ResolveNotebookExecutable prefers the notebook binary built alongside lmc.
func ResolveNotebookExecutable() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			candidate := filepath.Join(filepath.Dir(real), "notebook")
			if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	return exec.LookPath("notebook")
}
