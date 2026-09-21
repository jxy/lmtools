package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/mcp"
	"lmtools/internal/mcp/mcptest"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func connectHost(t *testing.T, servers []mcp.ServerConfig, opts mcp.HostOptions) *mcp.Host {
	t.Helper()
	host, err := mcp.Connect(context.Background(), servers, opts)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(host.Close)
	return host
}

func toolNames(host *mcp.Host) []string {
	var names []string
	for _, tool := range host.Tools() {
		names = append(names, tool.Name)
	}
	return names
}

func TestHostQualifiesAndRoutesAcrossServers(t *testing.T) {
	alpha, _ := stdioConfig(t, "alpha", mcptest.Scenario{
		Era:          mcptest.EraModern,
		Instructions: "alpha says hi",
		Tools: []mcp.Tool{
			echoTool(),
			{Name: "a.b", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "a_b", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Results: map[string]mcp.CallToolResult{"echo": {Content: []mcp.Content{{Type: "text", Text: "from alpha"}}}},
	})
	beta, _ := stdioConfig(t, "beta", mcptest.Scenario{
		Era:     mcptest.EraLegacy,
		Tools:   []mcp.Tool{echoTool()},
		Results: map[string]mcp.CallToolResult{"echo": {Content: []mcp.Content{{Type: "text", Text: "from beta"}}}},
	})
	warnings := &testLog{}
	host := connectHost(t, []mcp.ServerConfig{alpha, beta}, mcp.HostOptions{Warn: warnings.logf})

	if got := strings.Join(toolNames(host), ","); got != "mcp__alpha__echo,mcp__alpha__a_b,mcp__beta__echo" {
		t.Fatalf("tools = %s", got)
	}
	if !warnings.contains(`dropping tool "a_b": its name mcp__alpha__a_b collides with tool "a.b"`) {
		t.Fatalf("warnings = %v", warnings.all())
	}
	for server, want := range map[string]string{"alpha": "from alpha", "beta": "from beta"} {
		result, err := host.Call(context.Background(), mcp.QualifiedName(server, "echo"), json.RawMessage(`{"text":"x"}`))
		if err != nil || result.Content[0].Text != want {
			t.Fatalf("%s call = %+v, %v", server, result, err)
		}
	}
	if _, err := host.Call(context.Background(), "mcp__gamma__echo", nil); err == nil || !strings.Contains(err.Error(), "no MCP tool") {
		t.Fatalf("unknown tool error = %v", err)
	}

	instructions := host.Instructions()
	if len(instructions) != 1 || instructions[0].Server != "alpha" || instructions[0].Instructions != "alpha says hi" {
		t.Fatalf("Instructions() = %+v", instructions)
	}
	servers := host.Servers()
	if len(servers) != 2 || servers[0].Era != "modern" || servers[1].Era != "legacy" || servers[0].ToolCount != 2 {
		t.Fatalf("Servers() = %+v", servers)
	}
	tool, ok := host.Lookup("mcp__alpha__echo")
	if !ok || tool.Server != "alpha" || tool.Tool.Name != "echo" {
		t.Fatalf("Lookup() = %+v, %v", tool, ok)
	}
}

func TestHostFiltersToolsAndReportsUnknownNames(t *testing.T) {
	cfg, _ := stdioConfig(t, "filtered", mcptest.Scenario{
		Era: mcptest.EraModern,
		Tools: []mcp.Tool{
			{Name: "keep", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "drop", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "other", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	})
	cfg.IncludeTools = []string{"keep", "drop", "missing"}
	cfg.ExcludeTools = []string{"drop"}
	warnings := &testLog{}
	host := connectHost(t, []mcp.ServerConfig{cfg}, mcp.HostOptions{Warn: warnings.logf})
	if got := strings.Join(toolNames(host), ","); got != "mcp__filtered__keep" {
		t.Fatalf("tools = %s", got)
	}
	if !warnings.contains(`includeTools names "missing"`) {
		t.Fatalf("warnings = %v", warnings.all())
	}
}

func TestHostDropsToolsWithSchemasItCannotUse(t *testing.T) {
	cfg, _ := stdioConfig(t, "schemas", mcptest.Scenario{
		Era: mcptest.EraModern,
		Tools: []mcp.Tool{
			{Name: "remote", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://example.test/schema.json"}}}`)},
			{Name: "local", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/$defs/x"}},"$defs":{"x":{"type":"string"}}}`)},
			{Name: "array", InputSchema: json.RawMessage(`[]`)},
			{Name: "", InputSchema: json.RawMessage(`{}`)},
		},
	})
	warnings := &testLog{}
	host := connectHost(t, []mcp.ServerConfig{cfg}, mcp.HostOptions{Warn: warnings.logf})
	if got := strings.Join(toolNames(host), ","); got != "mcp__schemas__local" {
		t.Fatalf("tools = %s", got)
	}
	for _, want := range []string{`dropping tool "remote"`, `dropping tool "array"`, "dropping a tool with no name"} {
		if !warnings.contains(want) {
			t.Errorf("warning %q missing from %v", want, warnings.all())
		}
	}
}

func TestHostOptionalServersAreSkippedAndRequiredOnesFail(t *testing.T) {
	broken := mcp.ServerConfig{Name: "broken", Type: mcp.TransportStdio, Command: "/nonexistent/mcp-server"}
	working, _ := stdioConfig(t, "working", mcptest.Scenario{Era: mcptest.EraModern, Tools: []mcp.Tool{echoTool()}})

	_, err := mcp.Connect(context.Background(), []mcp.ServerConfig{broken, working}, mcp.HostOptions{})
	if err == nil || !strings.Contains(err.Error(), `mcp server "broken"`) {
		t.Fatalf("Connect() error = %v, want the broken server", err)
	}

	broken.Optional = true
	warnings := &testLog{}
	host := connectHost(t, []mcp.ServerConfig{broken, working}, mcp.HostOptions{Warn: warnings.logf})
	if got := strings.Join(toolNames(host), ","); got != "mcp__working__echo" {
		t.Fatalf("tools = %s", got)
	}
	if !warnings.contains(`skipping optional MCP server "broken"`) {
		t.Fatalf("warnings = %v", warnings.all())
	}
}

func TestHostAppliesTheServerTimeout(t *testing.T) {
	cfg, _ := stdioConfig(t, "slow", mcptest.Scenario{Era: mcptest.EraModern, Tools: []mcp.Tool{echoTool()}, CallDelayMs: 10000})
	cfg.Timeout = 200 * time.Millisecond
	host := connectHost(t, []mcp.ServerConfig{cfg}, mcp.HostOptions{CallTimeout: time.Minute})

	started := time.Now()
	_, err := host.Call(context.Background(), "mcp__slow__echo", json.RawMessage(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestHostHeaderAnnotationsGovernHTTPOnly(t *testing.T) {
	bad := mcp.Tool{Name: "bad", InputSchema: json.RawMessage(`{"type":"object","properties":{"list":{"type":"array","items":{"type":"string","x-mcp-header":"Item"}}}}`)}
	good := mcp.Tool{Name: "good", InputSchema: json.RawMessage(`{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}}}`)}

	web := httptest.NewServer(mcptest.NewHandler(mcptest.Scenario{Era: mcptest.EraModern, Tools: []mcp.Tool{bad, good}}))
	t.Cleanup(web.Close)
	stdio, _ := stdioConfig(t, "local", mcptest.Scenario{Era: mcptest.EraModern, Tools: []mcp.Tool{bad, good}})

	warnings := &testLog{}
	host := connectHost(t, []mcp.ServerConfig{
		{Name: "web", Type: mcp.TransportHTTP, URL: web.URL},
		stdio,
	}, mcp.HostOptions{Warn: warnings.logf})
	if got := strings.Join(toolNames(host), ","); got != "mcp__web__good,mcp__local__bad,mcp__local__good" {
		t.Fatalf("tools = %s", got)
	}
	if !warnings.contains(`MCP server "web": dropping tool "bad"`) {
		t.Fatalf("warnings = %v", warnings.all())
	}
}

func TestHostAppliesTheRunSelectionAfterTheFile(t *testing.T) {
	cfg, _ := stdioConfig(t, "selected", mcptest.Scenario{
		Era: mcptest.EraModern,
		Tools: []mcp.Tool{
			{Name: "keep", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "also", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "hidden", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "other", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	})
	cfg.ExcludeTools = []string{"hidden"}
	cfg.Selection = mcp.ToolSelection{Include: []string{"keep", "also", "hidden", "missing"}, Exclude: []string{"also", "typo"}}
	warnings := &testLog{}
	host := connectHost(t, []mcp.ServerConfig{cfg}, mcp.HostOptions{Warn: warnings.logf})
	if got := strings.Join(toolNames(host), ","); got != "mcp__selected__keep" {
		t.Fatalf("tools = %s", got)
	}
	for _, want := range []string{`-tool-include names "missing"`, `-tool-exclude names "typo"`} {
		if !warnings.contains(want) {
			t.Fatalf("warnings = %v, want %q", warnings.all(), want)
		}
	}
	if warnings.contains(`names "hidden"`) {
		t.Fatalf("a tool the server offers was reported: %v", warnings.all())
	}
}
