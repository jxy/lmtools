//go:build !windows

package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/prompts"
	"path/filepath"
	"strings"
	"testing"
)

// oneMCPTool plays a connected MCP server with one tool, for the addendum
// its tools add to the tool prompt.
type oneMCPTool struct{}

func (oneMCPTool) Tools() []mcp.QualifiedTool {
	return []mcp.QualifiedTool{{Name: mcp.QualifiedPrefix + "files__read", Server: "files", Tool: mcp.Tool{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
}

func (oneMCPTool) Instructions() []mcp.ServerInstructions { return nil }

func (oneMCPTool) Call(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, nil
}

func strp(s string) *string { return &s }

func describePrompt(system *string) string {
	if system == nil {
		return "none"
	}
	return fmt.Sprintf("%.40q", *system)
}

// sessionUnder creates a session whose lineage carries system, a stored
// system message holding it, or no system message for nil. The session holds
// a question, an answer, and a second question, and a branch replaces the
// second question with a question and an answer of its own. It returns the
// root, the branch, and the ID of the branch's answer.
func sessionUnder(t *testing.T, ctx context.Context, system *string) (root, branch *Session, answerID string) {
	t.Helper()
	root = createPlanSession(t, "")
	if system != nil {
		if _, err := saveSystemMessage(root, *system); err != nil {
			t.Fatalf("saveSystemMessage() error = %v", err)
		}
	}
	appendPlanMessage(t, ctx, root, core.RoleUser, "first question")
	appendPlanMessage(t, ctx, root, core.RoleAssistant, "first answer")
	second := appendPlanMessage(t, ctx, root, core.RoleUser, "second question")
	branchPath, err := CreateSibling(ctx, root.Path, second)
	if err != nil {
		t.Fatalf("CreateSibling() error = %v", err)
	}
	branch = &Session{Path: branchPath}
	appendPlanMessage(t, ctx, branch, core.RoleUser, "other question")
	answerID = appendPlanMessage(t, ctx, branch, core.RoleAssistant, "other answer")
	return root, branch, answerID
}

// requestSystem returns the text of the system message a request begins
// with, and false when it begins with another message.
func requestSystem(messages []core.TypedMessage) (string, bool) {
	if len(messages) == 0 || messages[0].Role != string(core.RoleSystem) {
		return "", false
	}
	var text strings.Builder
	for _, block := range messages[0].Blocks {
		if b, ok := block.(core.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String(), true
}

// directoriesUnder counts the directories under path, path included.
func directoriesUnder(t *testing.T, path string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			count++
		}
		return err
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", path, err)
	}
	return count
}

// Both ways of resuming a branch, by its session path and by a message in
// it, decide the fork from the system prompt its lineage carries, which the
// branch inherits from its root. A custom prompt or a stored empty one
// stays. A lineage with none, or on a built-in prompt this run does not
// advertise, follows the built-in tool prompt, and an explicit -s forks with
// its prompt. A resume by message that forks leaves no branch behind, and the
// fork holds the prompt the request carried.
func TestResumeDecidesTheForkFromTheLineagePrompt(t *testing.T) {
	toolPrompt := core.ToolSystemPromptFor(nil)
	tools := func(cfg *core.RequestOptions) { cfg.ToolEnabled = true }
	explicit := func(system string) func(*core.RequestOptions) {
		return func(cfg *core.RequestOptions) {
			cfg.ToolEnabled = true
			cfg.System = system
			cfg.SystemExplicitlySet = true
		}
	}
	cases := []struct {
		name      string
		stored    *string
		configure func(*core.RequestOptions)
		// want is the system prompt the request carries, nil for none.
		want *string
		fork bool
	}{
		{name: "custom prompt", stored: strp("custom prompt"), configure: tools, want: strp("custom prompt")},
		{name: "stored empty prompt", stored: strp(""), configure: tools, want: strp("")},
		{name: "no prompt", configure: tools, want: strp(toolPrompt), fork: true},
		{name: "default prompt", stored: strp(prompts.DefaultSystemPrompt), configure: tools, want: strp(toolPrompt), fork: true},
		{name: "tool prompt", stored: strp(toolPrompt), configure: tools, want: strp(toolPrompt)},
		{name: "tool prompt under another tool set", stored: strp(toolPrompt), configure: func(cfg *core.RequestOptions) {
			cfg.ToolEnabled = true
			cfg.ExcludedTools = []string{core.UniversalCommandToolName}
		}, want: strp(prompts.ToolSystemPromptWithoutCommand), fork: true},
		{name: "tool prompt with an MCP addendum", stored: strp(toolPrompt + core.MCPSystemPromptAddendum(oneMCPTool{})), configure: tools, want: strp(toolPrompt), fork: true},
		{name: "explicit prompt that differs", stored: strp("custom prompt"), configure: explicit("other prompt"), want: strp("other prompt"), fork: true},
		{name: "explicit empty prompt", stored: strp("custom prompt"), configure: explicit(""), fork: true},
	}
	resumes := []struct {
		name string
		ref  func(branch *Session, answerID string) string
	}{
		{name: "by session", ref: func(branch *Session, _ string) string { return GetSessionID(branch.Path) }},
		{name: "by message", ref: func(branch *Session, answerID string) string { return GetSessionID(branch.Path) + "/" + answerID }},
	}
	for _, tc := range cases {
		for _, resume := range resumes {
			t.Run(tc.name+" "+resume.name, func(t *testing.T) {
				ctx := setupCoordinatorTestEnv(t)
				root, branch, answerID := sessionUnder(t, ctx, tc.stored)
				cfg := newTestCoordinatorConfig()
				tc.configure(&cfg)
				cfg.Resume = resume.ref(branch, answerID)

				plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "next question", false, PendingToolSkip)
				if err != nil {
					t.Fatalf("PrepareRequest() error = %v", err)
				}
				system, carried := requestSystem(plan.Messages)
				if carried != (tc.want != nil) || (carried && system != *tc.want) {
					got := "none"
					if carried {
						got = describePrompt(&system)
					}
					t.Fatalf("request system prompt = %s, want %s", got, describePrompt(tc.want))
				}

				directories := directoriesUnder(t, root.Path)
				committed, err := plan.Commit(ctx)
				if err != nil {
					t.Fatalf("Commit() error = %v", err)
				}
				if forked := GetRootSession(committed.Path) != GetRootSession(root.Path); forked != tc.fork {
					t.Fatalf("forked = %v, want %v; committed to %s", forked, tc.fork, committed.Path)
				}
				if !tc.fork {
					return
				}
				stored, err := GetSystemMessage(committed.Path)
				if err != nil {
					t.Fatalf("GetSystemMessage() error = %v", err)
				}
				if (stored == nil) != (tc.want == nil) || (stored != nil && *stored != *tc.want) {
					t.Fatalf("fork's system prompt = %s, want %s", describePrompt(stored), describePrompt(tc.want))
				}
				if after := directoriesUnder(t, root.Path); after != directories {
					t.Fatalf("source tree holds %d directories after the fork, want the %d it held", after, directories)
				}
			})
		}
	}
}

// A branch at a session's first user message still continues under the
// session's system prompt. Its lineage carries the prompt, a request that
// branches there carries it, and resuming the branch with tools on, by its
// path or by a message in it, keeps it without forking.
func TestBranchAtTheFirstUserMessageKeepsTheSystemPrompt(t *testing.T) {
	ctx := setupCoordinatorTestEnv(t)
	root := createPlanSession(t, "custom prompt")
	first := appendPlanMessage(t, ctx, root, core.RoleUser, "first question")
	appendPlanMessage(t, ctx, root, core.RoleAssistant, "first answer")

	cfg := newTestCoordinatorConfig()
	cfg.Branch = GetSessionID(root.Path) + "/" + first
	plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "other question", false, PendingToolSkip)
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if system, ok := requestSystem(plan.Messages); !ok || system != "custom prompt" || len(plan.Messages) != 2 {
		t.Fatalf("branch request = %d messages beginning with %q (%v), want the prompt and the question", len(plan.Messages), system, ok)
	}
	branch, err := plan.Commit(ctx)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	answerID := appendPlanMessage(t, ctx, &Session{Path: branch.Path}, core.RoleAssistant, "other answer")
	if system, err := GetSystemMessage(branch.Path); err != nil || system == nil || *system != "custom prompt" {
		t.Fatalf("branch system prompt = %s, %v; want the root's", describePrompt(system), err)
	}

	for _, resume := range []string{GetSessionID(branch.Path), GetSessionID(branch.Path) + "/" + answerID} {
		cfg := newTestCoordinatorConfig()
		cfg.ToolEnabled = true
		cfg.Resume = resume
		plan, err := PrepareRequest(ctx, cfg, core.NewTestNotifier(), core.TestToolUI{}, "next question", false, PendingToolSkip)
		if err != nil {
			t.Fatalf("PrepareRequest(%s) error = %v", resume, err)
		}
		if system, ok := requestSystem(plan.Messages); !ok || system != "custom prompt" {
			t.Fatalf("resume %s: request system prompt = %q (%v), want the root's", resume, system, ok)
		}
		committed, err := plan.Commit(ctx)
		if err != nil {
			t.Fatalf("Commit(%s) error = %v", resume, err)
		}
		if GetRootSession(committed.Path) != GetRootSession(root.Path) {
			t.Fatalf("resume %s forked to %s", resume, committed.Path)
		}
	}
}
