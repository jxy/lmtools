package config

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// conversationWith makes a session in dir whose first message is a system
// record holding settings, followed by a question and its answer, and
// returns the session's number and the answer's ID.
func conversationWith(t *testing.T, dir string, settings session.Settings) (string, string) {
	t.Helper()
	ctx := context.Background()
	store := session.Open(dir)
	info, _, err := store.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text := func(role core.Role, s string) session.Entry {
		return session.Entry{Role: role, Content: session.Content{Blocks: []core.Block{core.TextBlock{Text: s}}}}
	}
	saved, err := store.Append(ctx, session.Ref{Session: info.Number},
		session.Entry{Role: core.RoleSystem, Settings: &settings}, text(core.RoleUser, "q"), text(core.RoleAssistant, "a"))
	if err != nil {
		t.Fatal(err)
	}
	return info.Number, saved[2].ID
}

// A new notebook conversation takes the notebook's defaults: handoff once a
// window is known, early cuts, the memory tools without process execution,
// and a catalog snapshot, without creating the notebook.
func TestNotebookDefaults(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "book")
	cfg, err := ParseFlags([]string{"-argo-user", "test", "-sessions-dir", dir, "-notebook", book})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableTool || !cfg.RequestOptions().ImplicitNotebookTools || !slices.Contains(cfg.ExcludedTools, "universal_command") || len(cfg.MCPServers) != 2 {
		t.Fatal(cfg.ExcludedTools, cfg.MCPServers)
	}
	if b := cfg.Context; b.Policy != "handoff" || !b.NotebookDefault || !b.Early || b.Notebook != book || !strings.Contains(b.Initial, "empty") {
		t.Fatalf("binding %+v", b)
	}
	if cfg.Change.Notebook == nil || *cfg.Change.Notebook != book || cfg.Change.Catalog == nil || cfg.Settings.Catalog != cfg.Context.Initial {
		t.Fatalf("change %+v, settings %+v", cfg.Change, cfg.Settings)
	}
	if _, err = os.Stat(book); !os.IsNotExist(err) {
		t.Fatal("configuration created the notebook")
	}
}

// -resume takes the settings in force at the message it names, whichever
// way it names it, and resolves a bare session to its newest message; the
// notebook tools follow the notebook in force there.
func TestResumeTakesTheSettingsAtItsStart(t *testing.T) {
	dir, book := t.TempDir(), t.TempDir()
	number, answer := conversationWith(t, dir, session.Settings{Notebook: book, Policy: "handoff", Window: 16384, Trigger: 4000, Catalog: "snapshot-at-start"})
	info, err := session.Open(dir).Info(number)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{number, number + "/" + answer, info.Identity} {
		cfg, err := ParseFlags([]string{"-argo-user", "test", "-sessions-dir", dir, "-resume", ref})
		if err != nil {
			t.Fatal(ref, err)
		}
		if cfg.Start != (session.Ref{Session: number, Message: answer}) {
			t.Fatalf("%s resolved to %+v", ref, cfg.Start)
		}
		b := cfg.Context
		if b.Notebook != book || b.Initial != "snapshot-at-start" || b.Trigger != 4000 || b.WindowCap != 16384 || !b.Early {
			t.Fatalf("%s: binding %+v", ref, b)
		}
		if _, changed := cfg.Settings.Apply(cfg.Change); len(changed) != 0 || !cfg.RequestOptions().ImplicitNotebookTools {
			t.Fatalf("%s: the run changes %v", ref, changed)
		}
	}
	for _, ref := range []string{number + "/00ff", "00ff"} {
		if _, err := ParseFlags([]string{"-argo-user", "test", "-sessions-dir", dir, "-resume", ref}); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	old := filepath.Join(dir, "0007")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFlags([]string{"-argo-user", "test", "-sessions-dir", dir, "-resume", "0007"}); err == nil || !strings.Contains(err.Error(), "-delete 0007") {
		t.Fatalf("resuming an old session: %v", err)
	}
}

// Each setting can be set and cleared from a turn on: -notebook ” detaches
// the notebook with its view and catalog, -context-window 0 removes the cap,
// -context-trigger 0 restores the default trigger, and a new policy returns
// the format and early cuts to their defaults and clears a trigger it cannot
// use. Attaching a notebook later takes a snapshot of it.
func TestSettingsFlagsSetAndClear(t *testing.T) {
	dir, book := t.TempDir(), t.TempDir()
	number, _ := conversationWith(t, dir, session.Settings{Notebook: book, View: "tools", Policy: "handoff", Window: 16384, Trigger: 4000})
	resume := []string{"-argo-user", "test", "-sessions-dir", dir, "-resume", number}
	parse := func(flags ...string) Config {
		t.Helper()
		cfg, err := ParseFlags(append(slices.Clone(resume), flags...))
		if err != nil {
			t.Fatal(flags, err)
		}
		return cfg
	}
	if cfg := parse("-notebook", ""); cfg.Settings.Notebook != "" || cfg.Settings.View != "" || cfg.Context.Notebook != "" || cfg.Context.Initial != "" || cfg.Context.Policy != "handoff" || cfg.RequestOptions().ImplicitNotebookTools {
		t.Fatalf("detached: settings %+v, binding %+v", cfg.Settings, cfg.Context)
	}
	if cfg := parse("-context-window", "0", "-context-policy", "full"); cfg.Settings.Window != 0 || cfg.Context.WindowCap != 0 || cfg.Settings.Trigger != 0 || cfg.Context.Early {
		t.Fatalf("cap removed: %+v", cfg.Settings)
	}
	if cfg := parse("-context-trigger", "0"); cfg.Settings.Trigger != 0 || cfg.Settings.Window != 16384 {
		t.Fatalf("default trigger: %+v", cfg.Settings)
	}
	early := false
	number, _ = conversationWith(t, dir, session.Settings{Notebook: book, Policy: "full", Format: "prose", Early: &early})
	resume[len(resume)-1] = number
	if cfg := parse("-context-policy", "handoff"); cfg.Settings.Format != "" || cfg.Settings.Early != nil || !cfg.Context.Early {
		t.Fatalf("a new policy kept %+v", cfg.Settings)
	}
	number, _ = conversationWith(t, dir, session.Settings{Notebook: book, Policy: "handoff", Format: "prose", Early: &early})
	resume[len(resume)-1] = number
	if cfg := parse("-context-policy", "handoff"); cfg.Settings.Format != "prose" || cfg.Settings.Early == nil || *cfg.Settings.Early {
		t.Fatalf("restating the policy in force changed %+v", cfg.Settings)
	} else if _, changed := (session.Settings{Notebook: book, Policy: "handoff", Format: "prose", Early: &early}).Apply(cfg.Change); len(changed) != 0 {
		t.Fatalf("restating the policy in force changes %v", changed)
	}
	number, _ = conversationWith(t, dir, session.Settings{Policy: "full", Window: 8192})
	resume[len(resume)-1] = number
	cfg := parse("-notebook", book)
	if cfg.Context.Notebook != book || cfg.Context.Policy != "full" || cfg.Context.WindowCap != 8192 || !strings.Contains(cfg.Context.Initial, "empty") {
		t.Fatalf("attached: %+v", cfg.Context)
	}
	if _, changed := (session.Settings{Policy: "full", Window: 8192}).Apply(cfg.Change); !slices.Equal(changed, []string{"notebook"}) {
		t.Fatalf("attaching changes %v", changed)
	}
}

// -builtin-system restores the built-in prompt, and is refused beside
// -system, an empty one included.
func TestBuiltinSystemFlag(t *testing.T) {
	base := []string{"-argo-user", "test", "-sessions-dir", t.TempDir()}
	for _, flags := range [][]string{{"-system", "custom"}, {"-system", ""}} {
		if _, err := ParseFlags(append(slices.Clone(base), append(flags, "-builtin-system")...)); err == nil || !strings.Contains(err.Error(), "-builtin-system") {
			t.Fatalf("%v: %v", flags, err)
		}
	}
	cfg, err := ParseFlags(append(slices.Clone(base), "-builtin-system"))
	if err != nil || !cfg.Change.Builtin {
		t.Fatalf("change %+v: %v", cfg.Change, err)
	}
}

func TestNotebookToolNamesAreReserved(t *testing.T) {
	dir, book := t.TempDir(), t.TempDir()
	base := []string{"-argo-user", "fixture", "-sessions-dir", dir, "-notebook", book}
	for _, flags := range [][]string{{"-tool"}, {"-tool-include", "history"}} {
		c, err := ParseFlags(append(slices.Clone(base), flags...))
		if err != nil || c.RequestOptions().ImplicitNotebookTools {
			t.Fatal(flags, err)
		}
	}
	for _, name := range []string{"history", "history__db", "history.", "history_db"} {
		p := filepath.Join(dir, "servers.json")
		if err := os.WriteFile(p, []byte(`{"mcpServers":{"`+name+`":{"command":"test-server"}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ParseFlags(append(slices.Clone(base), "-mcp-config", p))
		if name == "history_db" {
			if err != nil {
				t.Fatal(name, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "in use") {
			t.Fatal(name, err)
		}
	}
}

func TestContextChoicesAndSavedExperiments(t *testing.T) {
	dir, book := t.TempDir(), t.TempDir()
	base := []string{"-argo-user", "test", "-sessions-dir", dir, "-context-window", "8192"}
	for _, args := range [][]string{
		{"-context-policy", "verified"},
		{"-context-policy", "adaptive"},
		{"-context-policy", "archive"},
		{"-context-policy", "handoff", "-context-format", "sections"},
		{"-notebook", book, "-notebook-view", "full"},
	} {
		if _, err := ParseFlags(append(slices.Clone(base), args...)); err == nil {
			t.Fatalf("accepted experimental options %v", args)
		}
	}
	number, _ := conversationWith(t, dir, session.Settings{Notebook: book, Policy: "verified", Format: "sections", View: "tools", Window: 16384, Trigger: 4000, Catalog: "frozen background"})
	resume := []string{"-argo-user", "test", "-sessions-dir", dir, "-resume", number}
	if _, err := ParseFlags(resume); err == nil || !strings.Contains(err.Error(), "-context-policy handoff") {
		t.Fatalf("missing way to resume the experiment: %v", err)
	}
	c, err := ParseFlags(append(slices.Clone(resume), "-context-policy", "handoff"))
	if err != nil || c.Context.Policy != "handoff" || c.Context.Initial != "frozen background" || c.Context.View != "tools" || c.Context.WindowCap != 16384 || c.Context.Trigger != 4000 {
		t.Fatalf("lost settings on explicit resume: %+v %v", c.Context, err)
	}
	if c.Context.Format != "" {
		t.Fatal("standard resume inherited the experimental writer")
	}
	for _, policy := range []string{"handoff", ""} {
		sections, _ := conversationWith(t, dir, session.Settings{Notebook: book, Policy: policy, Format: "sections", View: "tools", Window: 16384})
		resume := []string{"-argo-user", "test", "-sessions-dir", dir, "-resume", sections}
		if _, err := ParseFlags(resume); err == nil || !strings.Contains(err.Error(), "-context-policy handoff") {
			t.Fatalf("missing way to resume the sections writer under policy %q: %v", policy, err)
		}
		c, err := ParseFlags(append(slices.Clone(resume), "-context-policy", "handoff"))
		if err != nil || c.Context.Policy != "handoff" || c.Context.Format != "" || c.Context.WindowCap != 16384 {
			t.Fatalf("the advised resume of the sections writer under policy %q: %+v %v", policy, c.Context, err)
		}
		c, err = ParseFlags(append(slices.Clone(resume), "-context-policy", "handoff", "-context-format", "prose"))
		if err != nil || c.Context.Format != "prose" {
			t.Fatalf("the resume lost the format it set: %+v %v", c.Context, err)
		}
	}
}

func TestTextOnlyContextAndInvalidSettings(t *testing.T) {
	base := []string{"-argo-user", "test", "-sessions-dir", t.TempDir()}
	cfg, err := ParseFlags(append(slices.Clone(base), "-context-policy", "handoff", "-context-window", "16384"))
	if err != nil || cfg.EnableTool || len(cfg.MCPServers) != 0 {
		t.Fatal(cfg, err)
	}
	for _, bad := range [][]string{{"-context-policy", "x"}, {"-context-policy", "verified"}, {"-context-policy", "adaptive", "-context-window", "8192", "-context-trigger", "99999"}, {"-notebook-view", "catalog"}} {
		if _, err := ParseFlags(append(slices.Clone(base), bad...)); err == nil {
			t.Fatal(bad)
		}
	}
}

// A notebook's policy defaults to handoff, whose window the run resolves
// later; -context-policy full replays, and early cuts can be turned off.
func TestNotebookPolicyChoices(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "book")
	base := []string{"-argo-user", "fixture", "-sessions-dir", dir, "-notebook", book}
	c, err := ParseFlags(append(slices.Clone(base), "-context-policy", "full"))
	if err != nil || c.Context.Compacts() || c.Context.Early || c.Context.NotebookDefault {
		t.Fatal(c.Context, err)
	}
	c, err = ParseFlags(append(slices.Clone(base), "-context-window", "0"))
	if err != nil || c.Context.Policy != "handoff" || !c.Context.NotebookDefault || c.Context.WindowCap != 0 {
		t.Fatal(c.Context, err)
	}
	c, err = ParseFlags(append(slices.Clone(base), "-context-early-cuts=false", "-context-window", "8192"))
	if err != nil || c.Context.Policy != "handoff" || c.Context.Early || c.Context.WindowCap != 8192 {
		t.Fatal(c.Context, err)
	}
}
