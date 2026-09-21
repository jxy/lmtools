package config

import (
	"fmt"
	"lmtools/internal/mcp"
	"os"
)

// mcpConfigFlag collects every -mcp-config in command-line order.
type mcpConfigFlag struct {
	paths *[]string
}

func (f mcpConfigFlag) String() string {
	if f.paths == nil {
		return ""
	}
	return fmt.Sprint(*f.paths)
}

func (f mcpConfigFlag) Set(value string) error {
	if value == "" {
		return fmt.Errorf("-mcp-config requires a file path")
	}
	*f.paths = append(*f.paths, value)
	return nil
}

// validateMCPFlags reads every -mcp-config file at parse time, the way
// -json-schema and -image read theirs, so a broken file fails before a
// prompt is read. Configured servers turn tool mode on: a server nobody can
// call is a server nobody meant to configure.
func validateMCPFlags(cfg *Config) error {
	if len(cfg.MCPConfigPaths) == 0 {
		return nil
	}
	if cfg.Embed {
		return fmt.Errorf("invalid flag combination: -mcp-config is only supported in chat mode")
	}
	loaded, err := mcp.LoadConfigFiles(cfg.MCPConfigPaths, os.LookupEnv)
	if err != nil {
		return fmt.Errorf("-mcp-config: %w", err)
	}
	if len(loaded.Servers) == 0 {
		return fmt.Errorf("-mcp-config: no servers are configured")
	}
	cfg.MCPServers = loaded.Servers
	cfg.MCPWarnings = loaded.Warnings
	cfg.EnableTool = true
	return nil
}
