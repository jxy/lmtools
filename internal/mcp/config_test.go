package mcp_test

import (
	"lmtools/internal/mcp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookupFrom(values map[string]string) mcp.LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestParseConfigReadsTheHarnessShape(t *testing.T) {
	data := `{
  "otherSetting": true,
  "mcpServers": {
    "zeta": {"type": "http", "url": "${BASE:-https://mcp.example.test}/mcp", "headers": {"Authorization": "Bearer ${TOKEN}"}, "timeout": 5000, "excludeTools": ["delete"]},
    "alpha": {"command": "npx", "args": ["-y", "@scope/server", "--token", "${TOKEN}"], "env": {"HOME": "${HOME:-/nohome}", "MODE": "test"}, "cwd": "/srv", "startupTimeout": 1500, "includeTools": ["a"], "optional": true, "trust": true},
    "gem": {"httpUrl": "http://localhost:8080/mcp", "oauth": {"scopes": "x"}}
  }
}`
	cfg, err := mcp.ParseConfig([]byte(data), lookupFrom(map[string]string{"TOKEN": "s3cret"}))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if len(cfg.Servers) != 3 || cfg.Servers[0].Name != "alpha" || cfg.Servers[1].Name != "gem" || cfg.Servers[2].Name != "zeta" {
		t.Fatalf("servers = %+v", cfg.Servers)
	}
	alpha := cfg.Servers[0]
	if alpha.Type != mcp.TransportStdio || alpha.Command != "npx" || strings.Join(alpha.Args, " ") != "-y @scope/server --token s3cret" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if alpha.Env["HOME"] != "/nohome" || alpha.Env["MODE"] != "test" || alpha.Cwd != "/srv" || !alpha.Optional {
		t.Fatalf("alpha env/cwd/optional = %+v", alpha)
	}
	if alpha.StartupTimeout != 1500*time.Millisecond || alpha.Timeout != 0 || strings.Join(alpha.IncludeTools, ",") != "a" {
		t.Fatalf("alpha timeouts/tools = %+v", alpha)
	}
	zeta := cfg.Servers[2]
	if zeta.Type != mcp.TransportHTTP || zeta.URL != "https://mcp.example.test/mcp" || zeta.Headers["Authorization"] != "Bearer s3cret" {
		t.Fatalf("zeta = %+v", zeta)
	}
	if zeta.Timeout != 5*time.Second || strings.Join(zeta.ExcludeTools, ",") != "delete" {
		t.Fatalf("zeta timeout/tools = %+v", zeta)
	}
	if gem := cfg.Servers[1]; gem.Type != mcp.TransportHTTP || gem.URL != "http://localhost:8080/mcp" {
		t.Fatalf("gem = %+v", gem)
	}
	if strings.Join(cfg.Warnings, "\n") != `server "alpha": ignoring unknown field "trust"`+"\n"+`server "gem": ignoring unknown field "oauth"` {
		t.Fatalf("warnings = %q", cfg.Warnings)
	}
}

func TestParseConfigRejections(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{name: "not an object", data: `[]`, want: "mcpServers"},
		{name: "no servers member", data: `{"servers":{}}`, want: "no mcpServers member"},
		{name: "sse transport", data: `{"mcpServers":{"a":{"type":"sse","url":"http://x/sse"}}}`, want: "deprecated HTTP+SSE"},
		{name: "unknown type", data: `{"mcpServers":{"a":{"type":"grpc","url":"http://x"}}}`, want: `unknown type "grpc"`},
		{name: "command and url", data: `{"mcpServers":{"a":{"command":"x","url":"http://x"}}}`, want: "both command and url"},
		{name: "neither", data: `{"mcpServers":{"a":{"args":["x"]}}}`, want: "neither command nor url"},
		{name: "type stdio with url", data: `{"mcpServers":{"a":{"type":"stdio","url":"http://x"}}}`, want: "needs command"},
		{name: "type http with command", data: `{"mcpServers":{"a":{"type":"http","command":"x"}}}`, want: "needs url"},
		{name: "bad url", data: `{"mcpServers":{"a":{"url":"ftp://x/mcp"}}}`, want: "not an http or https URL"},
		{name: "unset variable", data: `{"mcpServers":{"a":{"command":"${MISSING}"}}}`, want: "MISSING is not set"},
		{name: "unterminated reference", data: `{"mcpServers":{"a":{"command":"${MISSING"}}}`, want: "unterminated"},
		{name: "bad header name", data: `{"mcpServers":{"a":{"url":"http://x","headers":{"Bad Name":"v"}}}}`, want: "invalid header name"},
		{name: "negative timeout", data: `{"mcpServers":{"a":{"command":"x","timeout":-1}}}`, want: "must not be negative"},
		{name: "wrong field type", data: `{"mcpServers":{"a":{"command":["x"]}}}`, want: "wrong type"},
		{name: "empty name", data: `{"mcpServers":{"":{"command":"x"}}}`, want: "empty server name"},
		{name: "name with space", data: `{"mcpServers":{"my server":{"command":"x"}}}`, want: "whitespace"},
		{name: "bad env name", data: `{"mcpServers":{"a":{"command":"x","env":{"A=B":"v"}}}}`, want: "invalid variable name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mcp.ParseConfig([]byte(tt.data), lookupFrom(nil))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfigFilesMergesAndRefusesDuplicates(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")
	if err := os.WriteFile(first, []byte(`{"mcpServers":{"b":{"command":"b"},"a":{"command":"a","unknown":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(`{"mcpServers":{"c":{"url":"http://localhost/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := mcp.LoadConfigFiles([]string{second, first}, lookupFrom(nil))
	if err != nil {
		t.Fatalf("LoadConfigFiles() error = %v", err)
	}
	var names []string
	for _, server := range cfg.Servers {
		names = append(names, server.Name)
	}
	if strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("servers = %v", names)
	}
	if len(cfg.Warnings) != 1 || !strings.HasPrefix(cfg.Warnings[0], first+": ") {
		t.Fatalf("warnings = %v", cfg.Warnings)
	}

	dup := filepath.Join(dir, "dup.json")
	if err := os.WriteFile(dup, []byte(`{"mcpServers":{"a":{"command":"other"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = mcp.LoadConfigFiles([]string{first, dup}, lookupFrom(nil))
	if err == nil || !strings.Contains(err.Error(), `server "a" is already defined in `+first) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := mcp.LoadConfigFiles([]string{filepath.Join(dir, "missing.json")}, lookupFrom(nil)); err == nil {
		t.Fatal("a missing file loaded")
	}
}

func TestExpandEnv(t *testing.T) {
	lookup := lookupFrom(map[string]string{"A": "1", "EMPTY": ""})
	for input, want := range map[string]string{
		"plain":             "plain",
		"${A}":              "1",
		"x${A}y${A}":        "x1y1",
		"${EMPTY}":          "",
		"${B:-fallback}":    "fallback",
		"${A:-fallback}":    "1",
		"${B:-}":            "",
		"${B:-with:colon}":  "with:colon",
		"$A stays literal":  "$A stays literal",
		"${B:-${A}} nested": "${A} nested",
	} {
		got, err := mcp.ExpandEnv(input, lookup)
		if err != nil || got != want {
			t.Errorf("ExpandEnv(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"${B}", "${}", "${A", "${bad name}"} {
		if _, err := mcp.ExpandEnv(input, lookup); err == nil {
			t.Errorf("ExpandEnv(%q) succeeded", input)
		}
	}
}
