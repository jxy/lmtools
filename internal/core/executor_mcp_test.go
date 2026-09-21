package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	stdErrors "errors"
	"lmtools/internal/errors"
	"lmtools/internal/mcp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMCPTools plays connected servers for the executor: a fixed tool list,
// canned results or errors by qualified name, and a record of every call.
type fakeMCPTools struct {
	tools        []mcp.QualifiedTool
	instructions []mcp.ServerInstructions
	results      map[string]*mcp.CallToolResult
	errors       map[string]error
	delay        time.Duration

	mu    sync.Mutex
	calls []fakeMCPCall
}

type fakeMCPCall struct {
	name string
	args string
}

func (f *fakeMCPTools) Tools() []mcp.QualifiedTool             { return f.tools }
func (f *fakeMCPTools) Instructions() []mcp.ServerInstructions { return f.instructions }

func (f *fakeMCPTools) Call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeMCPCall{name: name, args: string(args)})
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err, ok := f.errors[name]; ok {
		return nil, err
	}
	if result, ok := f.results[name]; ok {
		return result, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: "ok"}}}, nil
}

func (f *fakeMCPTools) recorded() []fakeMCPCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMCPCall(nil), f.calls...)
}

func githubTools() *fakeMCPTools {
	return &fakeMCPTools{
		tools: []mcp.QualifiedTool{
			{Name: "mcp__github__list_issues", Server: "github", Tool: mcp.Tool{Name: "list_issues", Description: "List issues", InputSchema: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`)}},
			{Name: "mcp__github__create_issue", Server: "github", Tool: mcp.Tool{Name: "create_issue", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		},
		instructions: []mcp.ServerInstructions{{Server: "github", Instructions: "Prefer list_issues before create_issue."}},
		results:      map[string]*mcp.CallToolResult{},
		errors:       map[string]error{},
	}
}

func mcpToolCall(id, name, args string) ToolCall {
	return ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func newMCPExecutor(t *testing.T, cfg RequestOptions, approver Approver, tools MCPTools) *Executor {
	t.Helper()
	cfg.MCP = tools
	cfg.ToolEnabled = true
	if cfg.ToolTimeout == 0 {
		cfg.ToolTimeout = 5 * time.Second
	}
	executor, err := NewExecutor(cfg, nil, approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func TestMCPCallRunsUnderAutoApprove(t *testing.T) {
	tools := githubTools()
	tools.results["mcp__github__list_issues"] = &mcp.CallToolResult{Content: []mcp.Content{
		{Type: "text", Text: "#1 open"}, {Type: "text", Text: "#2 closed"},
	}}
	executor := newMCPExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true), tools)

	calls := []ToolCall{mcpToolCall("c1", "mcp__github__list_issues", `{"repo":"a/b"}`)}
	result := singleResult(t, executor.ExecuteParallel(context.Background(), calls, TestToolUI{}))
	if result.Error != "" || result.NotRun || result.Output != "#1 open\n#2 closed" {
		t.Fatalf("result = %#v", result)
	}
	if recorded := tools.recorded(); len(recorded) != 1 || recorded[0].name != "mcp__github__list_issues" || recorded[0].args != `{"repo":"a/b"}` {
		t.Fatalf("calls = %#v", recorded)
	}
	if calls[0].MCPServer != "github" || calls[0].MCPTool != "list_issues" {
		t.Fatalf("the call was not labelled: %#v", calls[0])
	}
}

func TestMCPCallAsksTheToolQuestionAndHonoursTheAnswer(t *testing.T) {
	for _, approve := range []bool{true, false} {
		approver := NewTestApprover(approve)
		executor := newMCPExecutor(t, RequestOptions{}, approver, githubTools())

		result := singleResult(t, executor.ExecuteParallel(context.Background(),
			[]ToolCall{mcpToolCall("c1", "mcp__github__create_issue", `{"title":"x"}`)}, TestToolUI{}))

		if len(approver.MCPApprovalCalls) != 1 {
			t.Fatalf("approve=%v: MCP prompts = %#v, want one", approve, approver.MCPApprovalCalls)
		}
		asked := approver.MCPApprovalCalls[0]
		if asked.Server != "github" || asked.Tool != "create_issue" || string(asked.Arguments) != `{"title":"x"}` {
			t.Fatalf("approve=%v: asked %#v", approve, asked)
		}
		if len(approver.ApprovalCalls) != 0 || len(approver.ImageApprovalCalls) != 0 {
			t.Fatalf("approve=%v: another question was asked", approve)
		}
		if approve {
			if result.Error != "" || result.Output != "ok" {
				t.Fatalf("approved result = %#v", result)
			}
			continue
		}
		if !result.NotRun || result.Reason != "user denied permission" || result.Code != errors.ErrCodeNotApproved {
			t.Fatalf("denied result = %#v", result)
		}
	}
}

func writeRuleFile(t *testing.T, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestMCPPolicyPrecedence(t *testing.T) {
	list := mcpToolCall("c1", "mcp__github__list_issues", `{}`)

	t.Run("blacklist beats auto-approve", func(t *testing.T) {
		cfg := RequestOptions{ToolAutoApprove: true, ToolBlacklist: writeRuleFile(t, "bl.txt", `{"mcp":"github"}`)}
		executor := newMCPExecutor(t, cfg, NewTestApprover(true), githubTools())
		result := singleResult(t, executor.ExecuteParallel(context.Background(), []ToolCall{list}, TestToolUI{}))
		if !result.NotRun || result.Code != errors.ErrCodeDeniedBlacklist || result.Reason != "blacklisted" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("whitelist runs without a prompt", func(t *testing.T) {
		approver := NewTestApprover(false)
		cfg := RequestOptions{ToolWhitelist: writeRuleFile(t, "wl.txt", `{"mcp":"github","tool":"list_issues"}`)}
		executor := newMCPExecutor(t, cfg, approver, githubTools())
		result := singleResult(t, executor.ExecuteParallel(context.Background(), []ToolCall{list}, TestToolUI{}))
		if result.Error != "" || result.Output != "ok" || len(approver.MCPApprovalCalls) != 0 {
			t.Fatalf("result = %#v, prompts = %d", result, len(approver.MCPApprovalCalls))
		}
	})

	t.Run("whitelist without a prompt denies the unlisted tool", func(t *testing.T) {
		cfg := RequestOptions{ToolNonInteractive: true, ToolWhitelist: writeRuleFile(t, "wl.txt", `{"mcp":"github","tool":"create_issue"}`)}
		executor := newMCPExecutor(t, cfg, NewTestApprover(true), githubTools())
		result := singleResult(t, executor.ExecuteParallel(context.Background(), []ToolCall{list}, TestToolUI{}))
		if !result.NotRun || result.Code != errors.ErrCodeDeniedNotWhitelisted {
			t.Fatalf("result = %#v", result)
		}
		if !strings.Contains(result.Error, `Add {"mcp":"github","tool":"list_issues"} to your whitelist file`) ||
			!strings.Contains(result.Error, "Whitelist file: "+cfg.ToolWhitelist) {
			t.Fatalf("error text = %q", result.Error)
		}
	})

	t.Run("no way to ask denies with the whitelist route", func(t *testing.T) {
		executor := newMCPExecutor(t, RequestOptions{}, nil, githubTools())
		result := singleResult(t, executor.ExecuteParallel(context.Background(), []ToolCall{list}, nil))
		if !result.NotRun || result.Code != errors.ErrCodeDeniedNonInteractive {
			t.Fatalf("result = %#v", result)
		}
		if !strings.Contains(result.Error, "Add the tool to a whitelist") || !strings.Contains(result.Error, "no terminal available for approval") {
			t.Fatalf("error text = %q", result.Error)
		}
	})
}

func TestMCPSuggestedRuleAdmitsTheCallItWasPrintedFor(t *testing.T) {
	rule, err := parseCommandRule(suggestedMCPRuleJSON("github", "list_issues"), matchBareCommandOnly)
	if err != nil {
		t.Fatalf("the suggested rule does not parse: %v", err)
	}
	if !rule.matchesMCP("github", "list_issues") || rule.matchesMCP("github", "create_issue") || rule.matchesMCP("gitlab", "list_issues") {
		t.Fatal("the suggested rule admits the wrong calls")
	}
	if rule.matches(UniversalCommandArgs{Command: []string{"github"}}) || rule.matchesImage(ViewImageArgs{Path: "x.png"}) {
		t.Fatal("an MCP rule matched a command or an image")
	}
}

var mcpJPEGBytes = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1}

func TestMCPResultMapping(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(pngBytes)
	jpeg := base64.StdEncoding.EncodeToString(mcpJPEGBytes)
	for _, tt := range []struct {
		name       string
		cfg        RequestOptions
		result     mcp.CallToolResult
		wantOutput string
		wantError  string
		wantCode   string
		wantImages []ImageBlock
		wantTrunc  bool
	}{
		{
			name:       "image within the cap becomes a block with its sniffed type",
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: "see"}, {Type: "image", MimeType: "image/jpeg", Data: png}}},
			wantOutput: "see",
			wantImages: []ImageBlock{{URL: ImageDataURL("image/png", pngBytes)}},
		},
		{
			name:       "image over the cap becomes a note",
			cfg:        RequestOptions{Provider: "anthropic"},
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(make([]byte, 6*1000*1000))}}},
			wantOutput: "[image omitted: image/png, 6MB is larger than the 5MB limit]",
		},
		{
			name:       "image on the legacy Argo wire becomes a note",
			cfg:        RequestOptions{ArgoLegacy: true},
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "image", MimeType: "image/png", Data: png}}},
			wantOutput: "[image omitted: image/png, 12 bytes; view_image is not available with -argo-legacy]",
		},
		{
			name:       "bytes that are not an image become a note",
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("not an image"))}}},
			wantOutput: "[image omitted: image/png is not PNG, JPEG, GIF, or WebP]",
		},
		{
			name:       "bad base64 becomes a note",
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "image", MimeType: "image/png", Data: "@@@"}}},
			wantOutput: "[image omitted: image/png, data is not valid base64]",
		},
		{
			name:       "audio and links and unknown types become notes",
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "audio", MimeType: "audio/wav", Data: base64.StdEncoding.EncodeToString(make([]byte, 30))}, {Type: "resource_link", URI: "file:///a.rs", Name: "a.rs", MimeType: "text/x-rust", Description: "entry point"}, {Type: "video"}}},
			wantOutput: "[audio omitted: audio/wav, 30 bytes]\n[resource: file:///a.rs (a.rs, text/x-rust)] entry point\n[video content omitted]",
		},
		{
			name: "embedded resources",
			result: mcp.CallToolResult{Content: []mcp.Content{
				{Type: "resource", Resource: &mcp.ResourceContents{URI: "file:///a.txt", MimeType: "text/plain", Text: "hello"}},
				{Type: "resource", Resource: &mcp.ResourceContents{URI: "file:///b.jpg", MimeType: "image/jpeg", Blob: jpeg}},
				{Type: "resource", Resource: &mcp.ResourceContents{URI: "file:///c.bin", MimeType: "application/octet-stream", Blob: png}},
				{Type: "resource"},
			}},
			wantOutput: "[resource: file:///a.txt]\nhello\n[resource: file:///b.jpg attached as an image]\n[resource omitted: file:///c.bin, application/octet-stream, 12 bytes of binary data]\n[resource omitted: no contents]",
			wantImages: []ImageBlock{{URL: ImageDataURL("image/jpeg", mcpJPEGBytes), Name: "file:///b.jpg"}},
		},
		{
			name:       "structured content stands in for missing text",
			result:     mcp.CallToolResult{Content: []mcp.Content{}, StructuredContent: json.RawMessage(`{"temperature":22.5}`)},
			wantOutput: `{"temperature":22.5}`,
		},
		{
			name:       "structured content stays out when text came",
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: "22.5 degrees"}}, StructuredContent: json.RawMessage(`{"temperature":22.5}`)},
			wantOutput: "22.5 degrees",
		},
		{
			name:      "isError is the model facing error",
			result:    mcp.CallToolResult{IsError: true, Content: []mcp.Content{{Type: "text", Text: "date must be in the future"}}},
			wantError: "date must be in the future",
			wantCode:  errors.ErrCodeExecError,
		},
		{
			name:      "isError without a message",
			result:    mcp.CallToolResult{IsError: true},
			wantError: "the MCP tool reported an error without a message",
			wantCode:  errors.ErrCodeExecError,
		},
		{
			name:      "input required is an error the model can read",
			result:    mcp.CallToolResult{ResultType: mcp.ResultTypeInputRequired, InputRequests: json.RawMessage(`{}`)},
			wantError: "the MCP server asked for more input (elicitation, sampling, or roots), which lmc does not provide; the call cannot be completed as made",
			wantCode:  errors.ErrCodeExecError,
		},
		{
			name:       "output is capped with the truncation mark",
			cfg:        RequestOptions{ToolMaxOutputBytes: 8},
			result:     mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: "0123456789"}}},
			wantOutput: "01234567",
			wantTrunc:  true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tools := githubTools()
			result := tt.result
			tools.results["mcp__github__list_issues"] = &result
			cfg := tt.cfg
			cfg.ToolAutoApprove = true
			executor := newMCPExecutor(t, cfg, NewTestApprover(true), tools)

			got := singleResult(t, executor.ExecuteParallel(context.Background(),
				[]ToolCall{mcpToolCall("c1", "mcp__github__list_issues", `{}`)}, TestToolUI{}))
			if got.Output != tt.wantOutput || got.Error != tt.wantError || got.Code != tt.wantCode || got.NotRun {
				t.Fatalf("result = %#v", got)
			}
			if len(got.Images) != len(tt.wantImages) {
				t.Fatalf("images = %#v, want %#v", got.Images, tt.wantImages)
			}
			for i := range tt.wantImages {
				if got.Images[i] != tt.wantImages[i] {
					t.Fatalf("image %d = %#v, want %#v", i, got.Images[i], tt.wantImages[i])
				}
			}
			if got.Truncated != tt.wantTrunc || (tt.wantTrunc && got.TruncatedTo != tt.cfg.ToolMaxOutputBytes) {
				t.Fatalf("truncation = %v/%d", got.Truncated, got.TruncatedTo)
			}
		})
	}
}

func TestMCPCallErrorsAreClassified(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		wantCode string
	}{
		{name: "deadline", err: context.DeadlineExceeded, wantCode: errors.ErrCodeTimeout},
		{name: "cancel", err: context.Canceled, wantCode: errors.ErrCodeCancelled},
		{name: "server error", err: &mcp.RPCError{Code: -32602, Message: "Unknown tool"}, wantCode: errors.ErrCodeExecError},
		{name: "transport", err: stdErrors.New("server exited"), wantCode: errors.ErrCodeExecError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tools := githubTools()
			tools.errors["mcp__github__list_issues"] = tt.err
			executor := newMCPExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true), tools)
			got := singleResult(t, executor.ExecuteParallel(context.Background(),
				[]ToolCall{mcpToolCall("c1", "mcp__github__list_issues", `{}`)}, TestToolUI{}))
			if got.Code != tt.wantCode || got.Error != tt.err.Error() || got.NotRun {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestMCPCallHonoursCancellation(t *testing.T) {
	tools := githubTools()
	tools.delay = 10 * time.Second
	executor := newMCPExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true), tools)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got := singleResult(t, executor.ExecuteParallel(ctx, []ToolCall{mcpToolCall("c1", "mcp__github__list_issues", `{}`)}, TestToolUI{}))
	if got.Code != errors.ErrCodeTimeout {
		t.Fatalf("result = %#v", got)
	}
}

func TestMCPArgumentsMustBeAnObject(t *testing.T) {
	tools := githubTools()
	executor := newMCPExecutor(t, RequestOptions{ToolAutoApprove: true}, NewTestApprover(true), tools)
	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		mcpToolCall("bad", "mcp__github__list_issues", `[1]`),
		mcpToolCall("empty", "mcp__github__list_issues", ``),
		mcpToolCall("null", "mcp__github__list_issues", `null`),
	}, TestToolUI{})
	if results[0].Code != errors.ErrCodeInvalidInput || !results[0].NotRun || !strings.Contains(results[0].Error, "must be a JSON object") {
		t.Fatalf("array arguments result = %#v", results[0])
	}
	if results[1].Error != "" || results[2].Error != "" {
		t.Fatalf("empty arguments results = %#v, %#v", results[1], results[2])
	}
	recorded := tools.recorded()
	if len(recorded) != 2 || recorded[0].args != "{}" || recorded[1].args != "{}" {
		t.Fatalf("calls = %#v", recorded)
	}
}

func TestMCPToolsAbsentFromTheRun(t *testing.T) {
	executor, err := NewExecutor(RequestOptions{ToolAutoApprove: true}, nil, NewTestApprover(true))
	if err != nil {
		t.Fatal(err)
	}
	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		{ID: "labelled", Name: "mcp__github__list_issues", MCPServer: "github", MCPTool: "list_issues", Args: json.RawMessage(`{}`)},
		mcpToolCall("bare", "mcp__github__list_issues", `{}`),
		mcpToolCall("other", "frobnicate", `{}`),
	}, TestToolUI{})
	if !strings.Contains(results[0].Error, `no -mcp-config defines server "github", or the tool is excluded`) || results[0].Code != errors.ErrCodeInvalidInput {
		t.Fatalf("labelled result = %#v", results[0])
	}
	if !strings.Contains(results[1].Error, "-mcp-config") {
		t.Fatalf("bare result = %#v", results[1])
	}
	if results[2].Error != "unsupported tool: frobnicate" {
		t.Fatalf("other result = %#v", results[2])
	}
}

func TestExecutorRefusesExcludedBuiltinToolsAndKeepsMCPImages(t *testing.T) {
	tools := githubTools()
	tools.results["mcp__github__list_issues"] = &mcp.CallToolResult{Content: []mcp.Content{
		{Type: "image", MimeType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(mcpJPEGBytes)},
	}}
	cfg := RequestOptions{ToolAutoApprove: true, ExcludedTools: []string{UniversalCommandToolName, ViewImageToolName}}
	executor := newMCPExecutor(t, cfg, NewTestApprover(true), tools)

	results := executor.ExecuteParallel(context.Background(), []ToolCall{
		{ID: "cmd", Name: UniversalCommandToolName, Args: json.RawMessage(`{"command":["true"]}`)},
		{ID: "img", Name: ViewImageToolName, Args: json.RawMessage(`{"path":"x.png"}`)},
		mcpToolCall("mcp", "mcp__github__list_issues", `{}`),
	}, TestToolUI{})
	for _, i := range []int{0, 1} {
		if results[i].Code != errors.ErrCodeInvalidInput || !strings.Contains(results[i].Error, "-tool-include or -tool-exclude leaves it out") {
			t.Fatalf("result %d = %#v, want a refusal naming the flags", i, results[i])
		}
	}
	if results[2].Error != "" || len(results[2].Images) != 1 {
		t.Fatalf("MCP result = %#v, want its image attached", results[2])
	}
	if recorded := tools.recorded(); len(recorded) != 1 {
		t.Fatalf("calls = %#v, want the MCP call alone", recorded)
	}
}
