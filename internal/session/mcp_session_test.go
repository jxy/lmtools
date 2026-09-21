package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/mcp"
	"lmtools/internal/prompts"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeMCPTools plays connected servers for the fork policy.
type fakeMCPTools struct {
	tools        []mcp.QualifiedTool
	instructions []mcp.ServerInstructions
}

func (f fakeMCPTools) Tools() []mcp.QualifiedTool             { return f.tools }
func (f fakeMCPTools) Instructions() []mcp.ServerInstructions { return f.instructions }
func (fakeMCPTools) Call(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, nil
}

func githubMCP() fakeMCPTools {
	return fakeMCPTools{
		tools:        []mcp.QualifiedTool{{Name: "mcp__github__list_issues", Server: "github", Tool: mcp.Tool{Name: "list_issues"}}},
		instructions: []mcp.ServerInstructions{{Server: "github", Instructions: "Be brief."}},
	}
}

func TestDecideResumeForkWithMCPServers(t *testing.T) {
	withMCP := prompts.ToolSystemPrompt + core.MCPSystemPromptAddendum(githubMCP())
	for _, tt := range []struct {
		name          string
		sessionSystem *string
		wantFork      bool
		wantSystem    string
	}{
		{name: "tool prompt gains the addendum", sessionSystem: stringPtr(prompts.ToolSystemPrompt), wantFork: true, wantSystem: withMCP},
		{name: "default prompt gains tools and the addendum", sessionSystem: stringPtr(prompts.DefaultSystemPrompt), wantFork: true, wantSystem: withMCP},
		{name: "the same addendum does not fork", sessionSystem: stringPtr(withMCP), wantFork: false},
		{name: "a custom prompt stays", sessionSystem: stringPtr("custom system"), wantFork: false},
		{name: "no prompt forks to the addendum", sessionSystem: nil, wantFork: true, wantSystem: withMCP},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newTestCoordinatorConfig()
			cfg.ToolEnabled = true
			cfg.MCP = githubMCP()
			decision := DecideResumeFork(tt.sessionSystem, cfg)
			if decision.ShouldFork != tt.wantFork || decision.NewSystem != tt.wantSystem {
				t.Fatalf("decision = %+v, want fork %v to %q", decision, tt.wantFork, tt.wantSystem)
			}
		})
	}

	// A run without MCP servers on a session that had them goes back to the
	// plain tool prompt, the way a built-in prompt follows the run.
	cfg := newTestCoordinatorConfig()
	cfg.ToolEnabled = true
	decision := DecideResumeFork(stringPtr(withMCP), cfg)
	if !decision.ShouldFork || decision.NewSystem != prompts.ToolSystemPrompt {
		t.Fatalf("decision without servers = %+v", decision)
	}
}

func TestMCPToolCallsKeepTheirLabelsInTheSessionAndShow(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	sess, err := CreateSession("system prompt", logger.GetLogger())
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	call := core.ToolCall{ID: "mcp-1", Name: "mcp__github__list_issues", MCPServer: "github", MCPTool: "list_issues", Args: json.RawMessage(`{"repo":"a/b"}`)}
	if _, err := AppendMessageWithToolInteraction(ctx, sess, Message{
		Role:      core.RoleAssistant,
		Timestamp: time.Now(),
		Model:     "test-model",
	}, []core.ToolCall{call}, nil); err != nil {
		t.Fatalf("AppendMessageWithToolInteraction() error = %v", err)
	}
	if _, err := SaveToolResults(ctx, sess, []core.ToolResult{{ID: "mcp-1", Output: "#1 open", Elapsed: 3}}, ""); err != nil {
		t.Fatalf("SaveToolResults() error = %v", err)
	}

	pending, err := CheckForPendingToolCalls(ctx, sess.Path)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending calls after the result = %v, %v", pending, err)
	}
	messages, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	var sawCall bool
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if use, ok := block.(core.ToolUseBlock); ok && use.Name == "mcp__github__list_issues" {
				sawCall = true
				if use.Namespace != "" || use.OriginalName != "" {
					t.Fatalf("the label leaked into the tool use block: %#v", use)
				}
			}
		}
	}
	if !sawCall {
		t.Fatalf("messages = %#v, want the tool use", messages)
	}

	toolsJSON, err := os.ReadFile(buildMessageFilePaths(sess.Path, "0001").ToolsPath)
	if err != nil {
		t.Fatalf("read .tools.json: %v", err)
	}
	var stored core.ToolInteraction
	if err := json.Unmarshal(toolsJSON, &stored); err != nil {
		t.Fatalf("decode .tools.json: %v", err)
	}
	if len(stored.Calls) != 1 || stored.Calls[0].MCPServer != "github" || stored.Calls[0].MCPTool != "list_issues" {
		t.Fatalf(".tools.json = %s, want the labels", toolsJSON)
	}

	output, err := captureDisplayStdout(t, func() error { return ShowConversation(sess.Path, core.NewTestNotifier()) })
	if err != nil {
		t.Fatalf("ShowConversation() error = %v", err)
	}
	if !strings.Contains(output, "  • mcp__github__list_issues (ID: mcp-1)\n     MCP: github/list_issues\n") {
		t.Fatalf("-show output = %q", output)
	}
}
