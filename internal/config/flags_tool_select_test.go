package config

import (
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"strings"
	"testing"
)

func threeServersConfig(t *testing.T) string {
	t.Helper()
	return writeMCPConfig(t, "three.json", `{"mcpServers":{
		"alpha":{"command":"a"},
		"beta":{"command":"b","includeTools":["x","y"]},
		"gamma":{"url":"http://localhost:1/mcp","excludeTools":["z"]}}}`)
}

func serverNames(cfg Config) string {
	names := make([]string, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		names = append(names, server.Name)
	}
	return strings.Join(names, ",")
}

func TestToolSelectionSettlesBuiltinToolsAndThePrompt(t *testing.T) {
	for _, tt := range []struct {
		name         string
		args         []string
		wantExcluded string
		wantSystem   string
	}{
		{name: "exclude the image tool", args: []string{"-tool-exclude", "view_image"}, wantExcluded: "view_image", wantSystem: prompts.ToolSystemPrompt},
		{name: "include the image tool alone", args: []string{"-tool-include", "view_image"}, wantExcluded: "universal_command", wantSystem: prompts.ToolSystemPromptWithoutCommand},
		{name: "include both", args: []string{"-tool-include", "view_image, universal_command"}, wantExcluded: "", wantSystem: prompts.ToolSystemPrompt},
		{name: "repeat and split", args: []string{"-tool-include", "universal_command,view_image", "-tool-exclude", "universal_command"}, wantExcluded: "universal_command", wantSystem: prompts.ToolSystemPromptWithoutCommand},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(append([]string{"-argo-user", "testuser", "-tool"}, tt.args...))
			if err != nil {
				t.Fatalf("ParseFlags() error = %v", err)
			}
			if got := strings.Join(cfg.ExcludedTools, ","); got != tt.wantExcluded {
				t.Fatalf("ExcludedTools = %q, want %q", got, tt.wantExcluded)
			}
			opts := cfg.RequestOptions()
			if opts.EffectiveSystem != tt.wantSystem {
				t.Fatalf("EffectiveSystem = %q, want %q", opts.EffectiveSystem, tt.wantSystem)
			}
			if got := strings.Join(opts.ExcludedTools, ","); got != tt.wantExcluded {
				t.Fatalf("RequestOptions().ExcludedTools = %q, want %q", got, tt.wantExcluded)
			}
			names := make([]string, 0, 2)
			for _, tool := range core.AdvertisedTools(opts) {
				names = append(names, tool.Name)
			}
			for _, excluded := range cfg.ExcludedTools {
				if strings.Contains(strings.Join(names, ","), excluded) {
					t.Fatalf("advertised tools %v still carry %s", names, excluded)
				}
			}
		})
	}

	// An explicit system prompt is the operator's, whatever the selection.
	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-tool", "-tool-exclude", "universal_command", "-s", "mine"})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}
	if opts := cfg.RequestOptions(); opts.EffectiveSystem != "mine" {
		t.Fatalf("EffectiveSystem = %q, want the explicit prompt", opts.EffectiveSystem)
	}
}

func TestToolSelectionSettlesServersBeforeTheyStart(t *testing.T) {
	path := threeServersConfig(t)
	for _, tt := range []struct {
		name         string
		args         []string
		wantServers  string
		wantExcluded string
		check        func(t *testing.T, cfg Config)
	}{
		{
			name: "include a server and a tool of another", args: []string{"-tool-include", "beta,gamma/keep"},
			wantServers: "beta,gamma", wantExcluded: "universal_command,view_image",
			check: func(t *testing.T, cfg Config) {
				if sel := cfg.MCPServers[0].Selection; len(sel.Include) != 0 || len(sel.Exclude) != 0 {
					t.Fatalf("beta selection = %+v, want every tool", sel)
				}
				if sel := cfg.MCPServers[1].Selection; strings.Join(sel.Include, ",") != "keep" || len(sel.Exclude) != 0 {
					t.Fatalf("gamma selection = %+v, want keep alone", sel)
				}
				if got := strings.Join(cfg.MCPServers[1].ExcludeTools, ","); got != "z" {
					t.Fatalf("the file's excludeTools were lost: %q", got)
				}
			},
		},
		{
			name: "a whole server named beside its tools keeps every tool", args: []string{"-tool-include", "gamma/one,gamma,universal_command"},
			wantServers: "gamma", wantExcluded: "view_image",
			check: func(t *testing.T, cfg Config) {
				if sel := cfg.MCPServers[0].Selection; len(sel.Include) != 0 {
					t.Fatalf("gamma selection = %+v, want every tool", sel)
				}
			},
		},
		{
			name: "exclude a server and a tool of another", args: []string{"-tool-exclude", "alpha", "-tool-exclude", "beta/y"},
			wantServers: "beta,gamma", wantExcluded: "",
			check: func(t *testing.T, cfg Config) {
				if sel := cfg.MCPServers[0].Selection; strings.Join(sel.Exclude, ",") != "y" || len(sel.Include) != 0 {
					t.Fatalf("beta selection = %+v, want y excluded", sel)
				}
				if got := strings.Join(cfg.MCPServers[0].IncludeTools, ","); got != "x,y" {
					t.Fatalf("the file's includeTools were lost: %q", got)
				}
			},
		},
		{
			name: "exclude wins over include", args: []string{"-tool-include", "alpha,beta/x", "-tool-exclude", "alpha,beta/x"},
			wantServers: "beta", wantExcluded: "universal_command,view_image",
			check: func(t *testing.T, cfg Config) {
				if sel := cfg.MCPServers[0].Selection; strings.Join(sel.Include, ",") != "x" || strings.Join(sel.Exclude, ",") != "x" {
					t.Fatalf("beta selection = %+v", sel)
				}
			},
		},
		{
			name: "excluding every server leaves the built-in tools", args: []string{"-tool-exclude", "alpha,beta,gamma"},
			wantServers: "", wantExcluded: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(append([]string{"-argo-user", "testuser", "-mcp-config", path}, tt.args...))
			if err != nil {
				t.Fatalf("ParseFlags() error = %v", err)
			}
			if got := serverNames(cfg); got != tt.wantServers {
				t.Fatalf("servers = %q, want %q", got, tt.wantServers)
			}
			if got := strings.Join(cfg.ExcludedTools, ","); got != tt.wantExcluded {
				t.Fatalf("ExcludedTools = %q, want %q", got, tt.wantExcluded)
			}
			if !cfg.EnableTool {
				t.Fatal("tool mode is off")
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestToolSelectionRejections(t *testing.T) {
	path := threeServersConfig(t)
	reserved := writeMCPConfig(t, "reserved.json", `{"mcpServers":{"view_image":{"command":"v"}}}`)
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "without tool mode", args: []string{"-tool-exclude", "view_image"}, want: "need -tool or -mcp-config"},
		{name: "empty selector", args: []string{"-tool", "-tool-include", "view_image,"}, want: "empty selector"},
		{name: "unknown name", args: []string{"-tool", "-tool-include", "github"}, want: `"github" is neither a built-in tool nor a configured MCP server`},
		{name: "unknown server with a tool", args: []string{"-mcp-config", path, "-tool-exclude", "delta/x"}, want: `names MCP server "delta", which no -mcp-config configures`},
		{name: "a built-in tool has no tools", args: []string{"-tool", "-tool-exclude", "view_image/x"}, want: `names MCP server "view_image"`},
		{name: "server without a tool name", args: []string{"-mcp-config", path, "-tool-exclude", "alpha/"}, want: `names no tool of server "alpha"`},
		{name: "nothing before the slash", args: []string{"-mcp-config", path, "-tool-exclude", "/x"}, want: "names no tool or server"},
		{name: "nothing left", args: []string{"-tool", "-tool-exclude", "universal_command,view_image"}, want: "leave the run without a tool"},
		{name: "nothing left with servers", args: []string{"-mcp-config", path, "-tool-include", "alpha", "-tool-exclude", "alpha"}, want: "leave the run without a tool"},
		{name: "reserved server name", args: []string{"-mcp-config", reserved}, want: `server "view_image" has the name of a built-in tool`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlags(append([]string{"-argo-user", "testuser"}, tt.args...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseFlags() error = %v, want %q", err, tt.want)
			}
		})
	}
}
