package core

import (
	"strings"
	"testing"
)

func TestMCPRuleSyntax(t *testing.T) {
	for _, tt := range []struct {
		line     string
		wantErr  string
		wantTool string
	}{
		{line: `{"mcp":"github"}`},
		{line: `{"mcp":"github","tool":"list_issues"}`, wantTool: "list_issues"},
		{line: `{"mcp":"github","path":"x"}`, wantErr: "mcp and tool alone"},
		{line: `{"mcp":"github","command":["ls"]}`, wantErr: "mcp and tool alone"},
		{line: `{"mcp":"github","stdin":true}`, wantErr: "mcp and tool alone"},
		{line: `{"mcp":"github","environ":{}}`, wantErr: "mcp and tool alone"},
		{line: `{"mcp":" github"}`, wantErr: "surrounding whitespace"},
		{line: `{"mcp":"github","unknown":1}`, wantErr: "unknown field"},
	} {
		t.Run(tt.line, func(t *testing.T) {
			for _, mode := range []ruleMatchMode{matchBareCommandOnly, matchAnyCall} {
				rule, err := parseCommandRule(tt.line, mode)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("mode %v: error = %v, want %q", mode, err, tt.wantErr)
					}
					continue
				}
				if err != nil {
					t.Fatalf("mode %v: error = %v", mode, err)
				}
				if rule.mcp != "github" || rule.tool != tt.wantTool || rule.matchMode != objectRuleMode(mode) {
					t.Fatalf("mode %v: rule = %#v", mode, rule)
				}
			}
		})
	}
}

func TestMCPRulesMatchOnlyMCPCalls(t *testing.T) {
	server, err := parseCommandRule(`{"mcp":"github"}`, matchBareCommandOnly)
	if err != nil {
		t.Fatal(err)
	}
	one, err := parseCommandRule(`{"mcp":"github","tool":"view_image"}`, matchBareCommandOnly)
	if err != nil {
		t.Fatal(err)
	}
	image, err := parseCommandRule(`{"tool":"view_image"}`, matchBareCommandOnly)
	if err != nil {
		t.Fatal(err)
	}
	command, err := parseCommandRule(`["github"]`, matchAnyCall)
	if err != nil {
		t.Fatal(err)
	}

	if !server.matchesMCP("github", "anything") || server.matchesMCP("gitlab", "anything") {
		t.Fatal("a server rule matched the wrong server")
	}
	if !one.matchesMCP("github", "view_image") || one.matchesMCP("github", "other") {
		t.Fatal("a tool rule matched the wrong tool")
	}
	// A server tool that happens to be named view_image is still an MCP rule.
	if one.matchesImage(ViewImageArgs{Path: "x.png"}) || server.matchesImage(ViewImageArgs{Path: "x.png"}) {
		t.Fatal("an MCP rule matched an image")
	}
	if one.matches(UniversalCommandArgs{Command: []string{"github"}}) || server.matches(UniversalCommandArgs{Command: []string{"github"}}) {
		t.Fatal("an MCP rule matched a command")
	}
	if image.matchesMCP("github", "view_image") || command.matchesMCP("github", "view_image") {
		t.Fatal("an image or command rule matched an MCP call")
	}
}

func TestDecideMCPPrecedence(t *testing.T) {
	deny, _ := parseCommandRule(`{"mcp":"github","tool":"delete_repo"}`, matchAnyCall)
	grant, _ := parseCommandRule(`{"mcp":"github"}`, matchBareCommandOnly)
	policy := approvalPolicy{blacklist: []commandRule{deny}, whitelist: []commandRule{grant}, whitelistConfigured: true, autoApprove: true, canPrompt: true}

	if got := policy.decideMCP("github", "delete_repo"); got != decisionDenyBlacklist {
		t.Fatalf("blacklisted tool = %v", got)
	}
	if got := policy.decideMCP("github", "list_issues"); got != decisionAllow {
		t.Fatalf("whitelisted server = %v", got)
	}
	if got := policy.decideMCP("gitlab", "list_issues"); got != decisionAllow {
		t.Fatalf("unlisted under auto-approve = %v", got)
	}
	policy.autoApprove = false
	if got := policy.decideMCP("gitlab", "list_issues"); got != decisionRequireApproval {
		t.Fatalf("unlisted with a prompt = %v", got)
	}
	policy.canPrompt = false
	if got := policy.decideMCP("gitlab", "list_issues"); got != decisionDenyNotWhitelisted {
		t.Fatalf("unlisted without a prompt under a whitelist = %v", got)
	}
	if got := policy.decideMCP("github", "list_issues"); got != decisionAllow {
		t.Fatalf("whitelisted without a prompt = %v", got)
	}
}
