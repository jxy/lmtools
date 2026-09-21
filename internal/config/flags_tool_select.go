package config

import (
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"strings"
)

// -tool-include and -tool-exclude choose which advertised tools a run
// keeps. A selector is a built-in tool name, an MCP server name for every
// tool of the server, or server/tool for one of them. Include narrows the
// run to what it names; exclude removes from what remains. Servers are
// settled here, before any is launched. A server's tools are settled when
// it lists them, from the Selection this file leaves on its configuration.

// selectorFlag collects comma separated selectors across repeats.
type selectorFlag struct {
	flag  string
	names *[]string
}

func (f selectorFlag) String() string {
	if f.names == nil {
		return ""
	}
	return strings.Join(*f.names, ",")
}

func (f selectorFlag) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return fmt.Errorf("%s has an empty selector", f.flag)
		}
		*f.names = append(*f.names, item)
	}
	return nil
}

// toolSelector is one parsed selector: a built-in tool, a whole server, or
// one tool of a server.
type toolSelector struct {
	name    string
	tool    string
	builtin bool
}

// validateToolSelection resolves the selectors against the run's tools and
// settles what they leave: the built-in tools the run withholds, in
// ExcludedTools, and the servers it keeps, each carrying the tool names
// the flags say about it. It runs after validateMCPFlags, which loads the
// servers a selector may name.
func validateToolSelection(cfg *Config) error {
	if len(cfg.ToolInclude) == 0 && len(cfg.ToolExclude) == 0 {
		return nil
	}
	if !cfg.EnableTool {
		return fmt.Errorf("-tool-include and -tool-exclude need -tool or -mcp-config")
	}
	servers := make(map[string]bool, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		servers[server.Name] = true
	}
	include, err := parseToolSelectors("-tool-include", cfg.ToolInclude, servers)
	if err != nil {
		return err
	}
	exclude, err := parseToolSelectors("-tool-exclude", cfg.ToolExclude, servers)
	if err != nil {
		return err
	}

	// Built-in tools: named by include or not withheld at all, then
	// withheld by exclude.
	keep := make(map[string]bool, len(core.BuiltinToolNames))
	for _, name := range core.BuiltinToolNames {
		keep[name] = len(include) == 0
	}
	for _, sel := range include {
		if sel.builtin {
			keep[sel.name] = true
		}
	}
	for _, sel := range exclude {
		if sel.builtin {
			keep[sel.name] = false
		}
	}
	cfg.ExcludedTools = nil
	for _, name := range core.BuiltinToolNames {
		if !keep[name] {
			cfg.ExcludedTools = append(cfg.ExcludedTools, name)
		}
	}

	// Servers: a whole server named by include keeps every tool, one named
	// through its tools keeps those, one named by neither is dropped when
	// include is set. Exclude drops a whole server or its named tools.
	selection := make(map[string]*mcp.ToolSelection, len(cfg.MCPServers))
	wholeServer := make(map[string]bool)
	for _, sel := range include {
		if sel.builtin {
			continue
		}
		if selection[sel.name] == nil {
			selection[sel.name] = &mcp.ToolSelection{}
		}
		if sel.tool == "" {
			wholeServer[sel.name] = true
		} else {
			selection[sel.name].Include = append(selection[sel.name].Include, sel.tool)
		}
	}
	dropped := make(map[string]bool)
	for _, sel := range exclude {
		if sel.builtin {
			continue
		}
		if sel.tool == "" {
			dropped[sel.name] = true
			continue
		}
		if selection[sel.name] == nil {
			selection[sel.name] = &mcp.ToolSelection{}
		}
		selection[sel.name].Exclude = append(selection[sel.name].Exclude, sel.tool)
	}
	kept := make([]mcp.ServerConfig, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		if dropped[server.Name] {
			continue
		}
		sel := selection[server.Name]
		if len(include) > 0 && (sel == nil || (len(sel.Include) == 0 && !wholeServer[server.Name])) {
			continue
		}
		if sel != nil {
			server.Selection = *sel
			if wholeServer[server.Name] {
				server.Selection.Include = nil
			}
		}
		kept = append(kept, server)
	}
	cfg.MCPServers = kept

	if len(cfg.ExcludedTools) == len(core.BuiltinToolNames) && len(cfg.MCPServers) == 0 {
		return fmt.Errorf("-tool-include and -tool-exclude leave the run without a tool")
	}
	return nil
}

// parseToolSelectors reads one flag's selectors. Every name must be
// something the run has, so a typo fails here rather than leaving a tool
// exposed or hidden by accident.
func parseToolSelectors(flag string, raw []string, servers map[string]bool) ([]toolSelector, error) {
	out := make([]toolSelector, 0, len(raw))
	for _, item := range raw {
		name, tool, hasTool := strings.Cut(item, "/")
		switch {
		case name == "":
			return nil, fmt.Errorf("%s: %q names no tool or server", flag, item)
		case hasTool && tool == "":
			return nil, fmt.Errorf("%s: %q names no tool of server %q", flag, item, name)
		case hasTool:
			if !servers[name] {
				return nil, fmt.Errorf("%s: %q names MCP server %q, which no -mcp-config configures", flag, item, name)
			}
			out = append(out, toolSelector{name: name, tool: tool})
		case isBuiltinToolName(name):
			out = append(out, toolSelector{name: name, builtin: true})
		case servers[name]:
			out = append(out, toolSelector{name: name})
		default:
			return nil, fmt.Errorf("%s: %q is neither a built-in tool nor a configured MCP server", flag, item)
		}
	}
	return out, nil
}

func isBuiltinToolName(name string) bool {
	for _, builtin := range core.BuiltinToolNames {
		if name == builtin {
			return true
		}
	}
	return false
}
