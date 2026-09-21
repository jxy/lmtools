package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// fakeMCP plays connected servers for the executor: a fixed tool list and
// one canned result per qualified name.
type fakeMCP struct {
	results map[string]*mcp.CallToolResult
}

func (f fakeMCP) Tools() []mcp.QualifiedTool {
	var tools []mcp.QualifiedTool
	for _, name := range []string{"list_issues", "delete_repo", "create_issue"} {
		tools = append(tools, mcp.QualifiedTool{Name: mcp.QualifiedName("github", name), Server: "github", Tool: mcp.Tool{Name: name}})
	}
	return tools
}

func (fakeMCP) Instructions() []mcp.ServerInstructions { return nil }

func (f fakeMCP) Call(_ context.Context, name string, _ json.RawMessage) (*mcp.CallToolResult, error) {
	if result, ok := f.results[name]; ok {
		return result, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: "ok"}}}, nil
}

func mcpCall(id, name, args string) core.ToolCall {
	return core.ToolCall{ID: id, Name: mcp.QualifiedName("github", name), MCPServer: "github", MCPTool: name, Args: json.RawMessage(args)}
}

func TestCLIToolUIShowCallRendersMCPCalls(t *testing.T) {
	notifier := newFormatTestNotifier()
	ui := NewCLIToolUI(notifier)

	ui.ShowCall(0, 2, mcpCall("a", "list_issues", `{ "repo": "a/b" }`), nil)
	ui.ShowCall(1, 2, core.ToolCall{ID: "b", Name: "mcp__other__tool", Args: json.RawMessage(`{}`)}, nil)

	want := "\n>>> Tools requested: 2\n[1/2] MCP tool: github/list_issues\n      Arguments: {\"repo\":\"a/b\"}\n\n[2/2] Tool: mcp__other__tool\n      Arguments: {}\n"
	if got := notifier.out.String(); got != want {
		t.Fatalf("ShowCall() output = %q, want %q", got, want)
	}
}

var elapsedPattern = regexp.MustCompile(`in \d+ms`)

// Every outcome an MCP call can have, driven through the real executor and
// this package's real renderer, the way the command and image outcomes are.
func TestMCPOutcomesRenderThroughTheCLI(t *testing.T) {
	dir := t.TempDir()
	whitelist := filepath.Join(dir, "whitelist.txt")
	if err := os.WriteFile(whitelist, []byte(`{"mcp":"github","tool":"list_issues"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write whitelist: %v", err)
	}
	blacklist := filepath.Join(dir, "blacklist.txt")
	if err := os.WriteFile(blacklist, []byte(`{"mcp":"github","tool":"delete_repo"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write blacklist: %v", err)
	}
	servers := fakeMCP{results: map[string]*mcp.CallToolResult{
		"mcp__github__list_issues": {Content: []mcp.Content{
			{Type: "text", Text: "#1 open"},
			{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(viewImagePNG)},
		}},
	}}
	executor, err := core.NewExecutor(core.RequestOptions{
		ToolEnabled:   true,
		ToolWhitelist: whitelist,
		ToolBlacklist: blacklist,
		MCP:           servers,
	}, nil, core.NewTestApprover(false))
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	notifier := newFormatTestNotifier()
	executor.ExecuteParallel(context.Background(), []core.ToolCall{
		mcpCall("a", "list_issues", `{"repo":"a/b"}`),
		mcpCall("b", "delete_repo", `{}`),
		mcpCall("c", "create_issue", `{"title":"x"}`),
	}, NewCLIToolUI(notifier))

	want := "\n>>> Tools requested: 3\n" +
		"[1/3] MCP tool: github/list_issues\n      Arguments: {\"repo\":\"a/b\"}\n\n" +
		"[2/3] MCP tool: github/delete_repo\n      Arguments: {}\n\n" +
		"[3/3] MCP tool: github/create_issue\n      Arguments: {\"title\":\"x\"}\n" +
		"\n>>> Running 1 of 3 commands...\n" +
		"\n>>> Results:\n" +
		"[1/3] Completed in Nms (1 image(s) attached)\n      Output:\n#1 open\n\n" +
		"[2/3] Not run: blacklisted\n\n" +
		"[3/3] Not run: user denied permission\n\n"
	if got := elapsedPattern.ReplaceAllString(notifier.out.String(), "in Nms"); got != want {
		t.Fatalf("transcript = %q\nwant %q", got, want)
	}
}
