package core

import (
	"encoding/json"
	"lmtools/internal/mcp"
	"lmtools/internal/prompts"
	"strings"
	"testing"
)

func TestMCPToolDefinitions(t *testing.T) {
	defs := MCPToolDefinitions([]mcp.QualifiedTool{
		{Name: "mcp__s__described", Server: "s", Tool: mcp.Tool{Name: "described", Title: "Described", Description: "Does things", InputSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"a":{"type":"string"}}}`)}},
		{Name: "mcp__s__bare", Server: "s", Tool: mcp.Tool{Name: "bare"}},
	})
	if len(defs) != 2 {
		t.Fatalf("defs = %#v", defs)
	}
	described := defs[0]
	if described.Name != "mcp__s__described" || described.MCPServer != "s" || described.MCPTool != "described" {
		t.Fatalf("described = %#v", described)
	}
	if described.Description != "Described: Does things" {
		t.Fatalf("description = %q", described.Description)
	}
	schema, ok := described.InputSchema.(map[string]interface{})
	if !ok || schema["type"] != "object" {
		t.Fatalf("schema = %#v", described.InputSchema)
	}
	if _, present := schema["$schema"]; present {
		t.Fatal("$schema survived")
	}
	bare := defs[1]
	if bare.Description != "Tool bare of MCP server s." {
		t.Fatalf("bare description = %q", bare.Description)
	}
	if schema, ok := bare.InputSchema.(map[string]interface{}); !ok || schema["type"] != "object" {
		t.Fatalf("bare schema = %#v", bare.InputSchema)
	}
	encoded, err := json.Marshal(described)
	if err != nil || strings.Contains(string(encoded), "MCPServer") || strings.Contains(string(encoded), `"s"`) {
		t.Fatalf("the server leaked into the wire shape: %s (%v)", encoded, err)
	}
}

func TestAdvertisedTools(t *testing.T) {
	tools := githubTools()
	if got := AdvertisedTools(RequestOptions{MCP: tools}); got != nil {
		t.Fatalf("tools advertised with -tool off: %#v", got)
	}
	names := func(defs []ToolDefinition) string {
		var out []string
		for _, def := range defs {
			out = append(out, def.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(AdvertisedTools(RequestOptions{ToolEnabled: true, MCP: tools})); got != "universal_command,view_image,mcp__github__list_issues,mcp__github__create_issue" {
		t.Fatalf("advertised = %s", got)
	}
	if got := names(AdvertisedTools(RequestOptions{ToolEnabled: true, ArgoLegacy: true, MCP: tools})); got != "universal_command,mcp__github__list_issues,mcp__github__create_issue" {
		t.Fatalf("advertised on legacy Argo = %s", got)
	}
	if got := names(AdvertisedTools(RequestOptions{ToolEnabled: true})); got != "universal_command,view_image" {
		t.Fatalf("advertised without MCP = %s", got)
	}

	defs := AdvertisedTools(RequestOptions{ToolEnabled: true, MCP: tools})
	if got := ConvertToolsToAnthropicTyped(defs); len(got) != 4 || got[2].Name != "mcp__github__list_issues" {
		t.Fatalf("anthropic tools = %#v", got)
	}
	if got := ConvertToolsToOpenAITyped(defs); len(got) != 4 || got[2].Function.Name != "mcp__github__list_issues" {
		t.Fatalf("openai tools = %#v", got)
	}
	google := ConvertToolsToGoogleTyped(defs)
	if len(google) != 1 || len(google[0].FunctionDeclarations) != 4 || google[0].FunctionDeclarations[2].Name != "mcp__github__list_issues" {
		t.Fatalf("google tools = %#v", google)
	}
}

func TestAnnotateMCPToolCalls(t *testing.T) {
	defs := AdvertisedTools(RequestOptions{ToolEnabled: true, MCP: githubTools()})
	calls := []ToolCall{
		{ID: "1", Name: "mcp__github__list_issues"},
		{ID: "2", Name: "universal_command"},
		{ID: "3", Name: "mcp__unknown__tool"},
	}
	AnnotateMCPToolCalls(calls, defs)
	if calls[0].MCPServer != "github" || calls[0].MCPTool != "list_issues" {
		t.Fatalf("call 1 = %#v", calls[0])
	}
	if calls[1].MCPServer != "" || calls[2].MCPServer != "" {
		t.Fatalf("calls 2 and 3 were labelled: %#v %#v", calls[1], calls[2])
	}
	AnnotateMCPToolCalls(calls, nil)
	if calls[0].MCPServer != "github" {
		t.Fatal("a second pass without definitions cleared the label")
	}
}

func TestMCPSystemPromptAddendum(t *testing.T) {
	if got := MCPSystemPromptAddendum(nil); got != "" {
		t.Fatalf("addendum without servers = %q", got)
	}
	if got := MCPSystemPromptAddendum(&fakeMCPTools{}); got != "" {
		t.Fatalf("addendum without tools = %q", got)
	}
	got := MCPSystemPromptAddendum(githubTools())
	if !strings.HasPrefix(got, "\n\nTools named mcp__<server>__<tool> are provided by MCP servers") {
		t.Fatalf("addendum = %q", got)
	}
	if !strings.HasSuffix(got, "\n\nInstructions from MCP server github:\nPrefer list_issues before create_issue.") {
		t.Fatalf("addendum = %q", got)
	}

	opts := RequestOptions{}
	ApplyMCP(&opts, githubTools())
	if !opts.ToolEnabled || opts.MCP == nil {
		t.Fatalf("ApplyMCP left opts = %#v", opts)
	}
	if opts.GetEffectiveSystem() != opts.EffectiveSystem || !strings.HasPrefix(opts.EffectiveSystem, prompts.DefaultSystemPrompt) {
		t.Fatalf("effective system = %q", opts.GetEffectiveSystem())
	}
	tooling := RequestOptions{ToolEnabled: true, EffectiveSystem: prompts.ToolSystemPrompt}
	ApplyMCP(&tooling, githubTools())
	if tooling.EffectiveSystem != prompts.ToolSystemPrompt+got {
		t.Fatalf("tool prompt with addendum = %q", tooling.EffectiveSystem)
	}
}
