package core

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/mcp"
	"strings"
)

// MCPTools is what a set of connected MCP servers offers the tool loop: the
// tools to advertise, the guidance their servers gave, and the way to call
// one. *mcp.Host satisfies it; tests use a double.
type MCPTools interface {
	Tools() []mcp.QualifiedTool
	Instructions() []mcp.ServerInstructions
	Call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)
}

// MCPCall is one MCP tool call as the approver and the UI see it: the
// configured server, the server's own tool name, and the arguments as the
// model gave them.
type MCPCall struct {
	Server    string
	Tool      string
	Arguments json.RawMessage
}

// AdvertisedTools is the one list a request advertises: the built-in tools,
// then the MCP tools in host order. It replaces GetBuiltinTools wherever the
// MCP tools belong beside the built-in ones.
func AdvertisedTools(cfg RequestOptions) []ToolDefinition {
	if !cfg.ToolEnabled {
		return nil
	}
	tools := GetBuiltinTools(cfg)
	if cfg.MCP != nil {
		tools = append(tools, MCPToolDefinitions(cfg.MCP.Tools())...)
	}
	return tools
}

// MCPToolDefinitions renders qualified tools as definitions. The server and
// the server's tool name ride in fields no wire renders, so a call the
// model makes under the qualified name can be labelled with them.
func MCPToolDefinitions(tools []mcp.QualifiedTool) []ToolDefinition {
	defs := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		defs = append(defs, ToolDefinition{
			Name:        tool.Name,
			Description: mcpToolDescription(tool),
			InputSchema: mcpToolInputSchema(tool.Tool.InputSchema),
			MCPServer:   tool.Server,
			MCPTool:     tool.Tool.Name,
		})
	}
	return defs
}

// mcpToolDescription is the server's description, or a line naming the tool
// and its server when the server gave none, since a provider rejects an
// empty description and the model needs to know what it is calling.
func mcpToolDescription(tool mcp.QualifiedTool) string {
	description := strings.TrimSpace(tool.Tool.Description)
	if description == "" {
		description = fmt.Sprintf("Tool %s of MCP server %s.", tool.Tool.Name, tool.Server)
	}
	if title := strings.TrimSpace(tool.Tool.Title); title != "" && !strings.Contains(description, title) {
		description = title + ": " + description
	}
	return description
}

// mcpToolInputSchema decodes the server's schema for the renderers, which
// take a map, and drops the $schema keyword, which names a dialect the
// providers do not read. A missing schema is the empty object schema.
func mcpToolInputSchema(raw json.RawMessage) interface{} {
	var schema map[string]interface{}
	if len(raw) == 0 || json.Unmarshal(raw, &schema) != nil || schema == nil {
		return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}
	delete(schema, "$schema")
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	return schema
}

// AnnotateMCPToolCalls labels the calls a response made under a qualified
// name with the server and tool the definition named, so the session, the
// review line, and -show can say github/list_issues.
func AnnotateMCPToolCalls(calls []ToolCall, defs []ToolDefinition) {
	if len(calls) == 0 {
		return
	}
	var byName map[string]ToolDefinition
	for _, def := range defs {
		if def.MCPServer == "" {
			continue
		}
		if byName == nil {
			byName = make(map[string]ToolDefinition)
		}
		byName[def.Name] = def
	}
	if byName == nil {
		return
	}
	for i := range calls {
		if def, ok := byName[calls[i].Name]; ok {
			calls[i].MCPServer = def.MCPServer
			calls[i].MCPTool = def.MCPTool
		}
	}
}

// MCPSystemPromptAddendum is what the tool system prompt gains when MCP
// tools are advertised: how the names read, and each server's own
// instructions under a heading naming the server. It is empty when there
// are no tools, so a run without MCP keeps the prompt it had.
func MCPSystemPromptAddendum(tools MCPTools) string {
	if tools == nil || len(tools.Tools()) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n\nTools named ")
	out.WriteString(mcp.QualifiedPrefix)
	out.WriteString("<server>")
	out.WriteString("__<tool> are provided by MCP servers the operator configured. Call them with the arguments their schema describes; their results are what the server returned.")
	for _, item := range tools.Instructions() {
		out.WriteString("\n\nInstructions from MCP server ")
		out.WriteString(item.Server)
		out.WriteString(":\n")
		out.WriteString(strings.TrimSpace(item.Instructions))
	}
	return out.String()
}

// ApplyMCP attaches connected servers to a run: the tools are advertised,
// tool mode is on, and the effective system prompt carries the addendum.
func ApplyMCP(opts *RequestOptions, tools MCPTools) {
	opts.MCP = tools
	opts.ToolEnabled = true
	opts.EffectiveSystem = opts.GetEffectiveSystem() + MCPSystemPromptAddendum(tools)
}
