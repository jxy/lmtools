package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNotebookDefaultsAndBinding(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "book")
	cfg, err := ParseFlags([]string{"-argo-user", "test", "-sessions-dir", dir, "-notebook", book, "-context-window", "16384"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableTool || !cfg.RequestOptions().ImplicitNotebookTools || !slices.Contains(cfg.ExcludedTools, "universal_command") || len(cfg.MCPServers) != 2 {
		t.Fatal(cfg.ExcludedTools, cfg.MCPServers)
	}
	if _, err = os.Stat(book); !os.IsNotExist(err) {
		t.Fatal("configuration created notebook")
	}
	sess := filepath.Join(dir, "0001")
	if err = os.Mkdir(sess, 0o700); err != nil {
		t.Fatal(err)
	}
	put := `{"notebook":"` + book + `","policy":"handoff","format":"prose","window":16384,"trigger":4000,"initial":"snapshot-at-start"}`
	if err = os.WriteFile(filepath.Join(sess, ".lmc-context.json"), []byte(put), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"0001", sess, "0001/0002"} {
		cfg, err = ParseFlags([]string{"-argo-user", "test", "-sessions-dir", dir, "-resume", ref, "-context-format", "prose", "-notebook-view", "catalog"})
		if err != nil || cfg.Context.Notebook != book || cfg.Context.Initial != "snapshot-at-start" || cfg.Context.Trigger != 4000 {
			t.Fatal(ref, cfg.Context, err)
		}
	}
	cfg, err = ParseFlags([]string{"-argo-user", "test", "-sessions-dir", dir, "-resume", "0001", "-context-policy", "full"})
	if err != nil || cfg.Context.Trigger != 0 || cfg.Context.Policy != "full" {
		t.Fatal(cfg.Context, err)
	}
}

func TestAttachNotebookAfterUsingHistoryAndReserveHistoryNames(t *testing.T) {
	dir, book := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "0001")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ".lmc-context.json"), []byte(`{"policy":"full","window":8192}`), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"-argo-user", "fixture", "-sessions-dir", dir, "-resume", "0001", "-notebook", book}
	c, err := ParseFlags(base)
	if err != nil || c.Context.Notebook != book || c.Context.Policy != "full" || c.Context.Window != 8192 || !strings.Contains(c.Context.Initial, "empty") {
		t.Fatal(c.Context, err)
	}
	for _, flags := range [][]string{{"-tool"}, {"-tool-include", "history"}} {
		c, err = ParseFlags(append(slices.Clone(base), flags...))
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
	sess := filepath.Join(dir, "0001")
	if err := os.Mkdir(sess, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := []byte(`{"notebook":"` + book + `","policy":"verified","format":"sections","view":"tools","window":16384,"trigger":4000,"initial":"frozen background"}`)
	file := filepath.Join(sess, ".lmc-context.json")
	if err := os.WriteFile(file, binding, 0o600); err != nil {
		t.Fatal(err)
	}
	resume := []string{"-argo-user", "test", "-sessions-dir", dir, "-resume", "0001"}
	c, err := ParseFlags(resume)
	if err == nil || !strings.Contains(err.Error(), "-context-policy handoff") {
		t.Fatalf("missing way to resume the experiment: %v", err)
	}
	c, err = ParseFlags(append(slices.Clone(resume), "-context-policy", "handoff"))
	if err != nil || c.Context.Policy != "handoff" || c.Context.Initial != "frozen background" || c.Context.View != "tools" || c.Context.Window != 16384 || c.Context.Trigger != 4000 {
		t.Fatalf("lost settings on explicit resume: %+v %v", c.Context, err)
	}
	if c.Context.Format != "" {
		t.Fatal("standard resume inherited the experimental writer")
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != string(binding) {
		t.Fatal("reading the experiment changed its binding", err)
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

func TestNewNotebookDefaultAndExistingReplay(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "book")
	base := []string{"-argo-user", "fixture", "-sessions-dir", dir, "-notebook", book}
	c, err := ParseFlags(base)
	if err != nil || c.Context.Policy != "handoff" || !c.Context.NotebookDefault || !c.Context.Early {
		t.Fatal(c.Context, err)
	}
	c, err = ParseFlags(append(slices.Clone(base), "-context-policy", "full"))
	if err != nil || c.Context.Compacts() || c.Context.Early {
		t.Fatal(c.Context, err)
	}
	c, err = ParseFlags(append(slices.Clone(base), "-context-window", "0"))
	if err != nil || c.Context.Policy != "full" || c.Context.NotebookDefault {
		t.Fatal(c.Context, err)
	}
	c, err = ParseFlags(append(slices.Clone(base), "-context-early-cuts=false", "-context-window", "8192"))
	if err != nil || c.Context.Policy != "handoff" || c.Context.Early || c.Context.WindowCap != 8192 {
		t.Fatal(c.Context, err)
	}
	sess := filepath.Join(dir, "0001")
	if err = os.Mkdir(sess, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(sess, ".lmc-context.json"), []byte(`{"notebook":"`+book+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = ParseFlags(append(slices.Clone(base), "-resume", "0001"))
	if err != nil || c.Context.Compacts() || c.Context.NotebookDefault || c.Context.Early {
		t.Fatal(c.Context, err)
	}
	if _, err = ParseFlags(append(slices.Clone(base), "-resume", "0001", "-notebook", "")); err == nil || !strings.Contains(err.Error(), "bound to notebook") {
		t.Fatal("detached the stored notebook from a full-replay session", err)
	}
}
