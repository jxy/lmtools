package config

import (
	"context"
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
	fs.IntVar(&c.Context.Window, "context-window", 0, "model context size in tokens; new notebook sessions can discover it from the catalog")
	fs.IntVar(&c.Context.Trigger, "context-trigger", 0, "estimated input tokens at which to compact (default 70% of the window)")
	fs.BoolVar(&c.Context.Early, "context-early-cuts", false, "allow model phase hints in handoff mode (on for new notebook sessions)")
}

// Start from the creation-time binding and apply this invocation's explicit
// flags. The immutable notebook snapshot is selected after resolving the view.
func sessionContext(fs *flag.FlagSet, requested, saved contextcfg.Binding, found bool) (contextcfg.Binding, error) {
	b := saved
	b.Initial = ""
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
		switch f.Name {
		case "notebook":
			b.Notebook = requested.Notebook
		case "context-policy":
			b.Policy, b.NotebookDefault = requested.Policy, false
		case "context-window":
			b.Window, b.WindowModel, b.WindowProvider = requested.Window, "", ""
		case "context-trigger":
			b.Trigger = requested.Trigger
		case "context-early-cuts":
			b.Early = requested.Early
		case "notebook-view":
			b.View = requested.View
		case "context-format":
			b.Format = requested.Format
		}
	})
	if err := selectContextChoices(&b.Settings, saved.Settings, found, explicit); err != nil {
		return b, err
	}
	if !found && b.Notebook != "" {
		if !explicit["context-policy"] {
			b.NotebookDefault = !explicit["context-window"] || b.Window != 0
			b.Policy = contextcfg.Full
			if b.NotebookDefault {
				b.Policy = contextcfg.Handoff
			}
		}
		if !explicit["context-early-cuts"] {
			b.Early = b.Policy == contextcfg.Handoff
		}
	}
	if explicit["context-policy"] {
		if !explicit["context-trigger"] && !b.Compacts() {
			b.Trigger = 0
		}
		if !explicit["context-early-cuts"] && b.Policy != contextcfg.Handoff {
			b.Early = false
		}
	}
	if explicit["context-early-cuts"] && b.Early && b.Policy != contextcfg.Handoff && b.Policy != contextcfg.Adaptive {
		return b, fmt.Errorf("-context-early-cuts requires a handoff policy that accepts phase hints")
	}
	if b.WindowModel == "" {
		b.WindowCap = b.Window
	}
	check := b.Settings
	if b.Notebook != "" && check.Window == 0 && check.Compacts() && !explicit["context-window"] {
		// The authenticated model catalog supplies the actual limit after startup.
		check.Window = 1000000000
	}
	return b, check.Validate()
}

func configureContext(c *Config, fs *flag.FlagSet) error {
	if c.Embed || c.ShowSessions || c.Show != "" || c.Delete != "" || c.ListModels {
		if c.Context.Enabled() {
			return fmt.Errorf("notebook and context options require chat mode")
		}
		return nil
	}
	ref := c.Resume
	if ref == "" {
		ref = c.Branch
	}
	existing, found, err := session.NewManager(c.SessionsDir).ContextBinding(ref)
	if err != nil {
		return fmt.Errorf("read session context: %w", err)
	}
	if !found && !c.Context.Enabled() {
		return nil
	}
	c.Context, err = sessionContext(fs, c.Context, existing, found)
	if err != nil {
		return err
	}
	if c.Context.Notebook != "" {
		c.Context.Notebook, err = filepath.Abs(c.Context.Notebook)
		if err != nil {
			return err
		}
	}
	if existing.Notebook != "" && existing.Notebook != c.Context.Notebook {
		return fmt.Errorf("session is bound to notebook %q", existing.Notebook)
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
		if existing.Notebook == c.Context.Notebook && existing.NotebookView() == c.Context.NotebookView() {
			c.Context.Initial = existing.Initial
		} else {
			c.Context.Initial, err = notebook.Snapshot(context.Background(), c.Context.Notebook, c.Context.View)
			if err != nil {
				return err
			}
		}
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
