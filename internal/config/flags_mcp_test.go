package config

import (
	"lmtools/internal/prompts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMCPConfig(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestMCPConfigFlagLoadsServersAndImpliesTool(t *testing.T) {
	path := writeMCPConfig(t, "mcp.json", `{"mcpServers":{"local":{"command":"fake-server","args":["--x"],"trust":true}}}`)
	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-mcp-config", path})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "local" || cfg.MCPServers[0].Command != "fake-server" {
		t.Fatalf("MCPServers = %+v", cfg.MCPServers)
	}
	if !cfg.EnableTool {
		t.Fatal("-mcp-config did not imply -tool")
	}
	if len(cfg.MCPWarnings) != 1 || !strings.Contains(cfg.MCPWarnings[0], `unknown field "trust"`) {
		t.Fatalf("MCPWarnings = %v", cfg.MCPWarnings)
	}
	opts := cfg.RequestOptions()
	if !opts.ToolEnabled || opts.EffectiveSystem != prompts.ToolSystemPrompt {
		t.Fatalf("RequestOptions() = ToolEnabled %v, EffectiveSystem %q", opts.ToolEnabled, opts.EffectiveSystem)
	}
}

func TestMCPConfigFlagIsRepeatable(t *testing.T) {
	first := writeMCPConfig(t, "first.json", `{"mcpServers":{"zeta":{"command":"z"}}}`)
	second := writeMCPConfig(t, "second.json", `{"mcpServers":{"alpha":{"url":"http://localhost:1/mcp"}}}`)
	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-mcp-config", first, "-mcp-config", second})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}
	if len(cfg.MCPServers) != 2 || cfg.MCPServers[0].Name != "alpha" || cfg.MCPServers[1].Name != "zeta" {
		t.Fatalf("MCPServers = %+v", cfg.MCPServers)
	}
	if strings.Join(cfg.MCPConfigPaths, ",") != first+","+second {
		t.Fatalf("MCPConfigPaths = %v", cfg.MCPConfigPaths)
	}

	dup := writeMCPConfig(t, "dup.json", `{"mcpServers":{"zeta":{"command":"other"}}}`)
	if _, err := ParseFlags([]string{"-argo-user", "testuser", "-mcp-config", first, "-mcp-config", dup}); err == nil || !strings.Contains(err.Error(), `server "zeta" is already defined`) {
		t.Fatalf("duplicate server error = %v", err)
	}
}

func TestMCPConfigFlagRejections(t *testing.T) {
	good := writeMCPConfig(t, "mcp.json", `{"mcpServers":{"local":{"command":"fake-server"}}}`)
	empty := writeMCPConfig(t, "empty.json", `{"mcpServers":{}}`)
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing file", args: []string{"-mcp-config", filepath.Join(t.TempDir(), "none.json")}, want: "-mcp-config: read"},
		{name: "embed mode", args: []string{"-e", "-mcp-config", good}, want: "chat mode"},
		{name: "no servers", args: []string{"-mcp-config", empty}, want: "no servers are configured"},
		{name: "non-interactive without a policy", args: []string{"-mcp-config", good, "-tool-non-interactive"}, want: "tool-non-interactive requires"},
		{name: "empty path", args: []string{"-mcp-config", ""}, want: "requires a file path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlags(append([]string{"-argo-user", "testuser"}, tt.args...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseFlags() error = %v, want %q", err, tt.want)
			}
		})
	}
}
