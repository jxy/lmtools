package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func imageRule(t *testing.T, line string, mode ruleMatchMode) commandRule {
	t.Helper()
	rule, err := parseCommandRule(line, mode)
	if err != nil {
		t.Fatalf("parse %s: %v", line, err)
	}
	return rule
}

func TestImageRuleSyntax(t *testing.T) {
	any := imageRule(t, `{"tool":"view_image"}`, matchBareCommandOnly)
	if any.tool != ViewImageToolName || any.path != nil || any.matchMode != matchExactChannels {
		t.Fatalf("whitelist any-image rule = %#v", any)
	}
	scoped := imageRule(t, `{"tool":"view_image","path":"plots"}`, matchAnyCall)
	if scoped.tool != ViewImageToolName || scoped.path == nil || *scoped.path != "plots" || scoped.matchMode != matchNamedChannelSubset {
		t.Fatalf("blacklist scoped rule = %#v", scoped)
	}

	rejected := []struct {
		name string
		rule string
		want string
	}{
		{name: "unknown tool", rule: `{"tool":"other"}`, want: `unknown tool "other"`},
		{name: "tool with command", rule: `{"tool":"view_image","command":["ls"]}`, want: "tool and path alone"},
		{name: "tool with stdin", rule: `{"tool":"view_image","stdin":true}`, want: "tool and path alone"},
		{name: "tool with environ", rule: `{"tool":"view_image","environ":{}}`, want: "tool and path alone"},
		{name: "tool with workdir", rule: `{"tool":"view_image","workdir":"/x"}`, want: "tool and path alone"},
		{name: "tool with stdout_file", rule: `{"tool":"view_image","stdout_file":"out.txt"}`, want: "tool and path alone"},
		{name: "empty path", rule: `{"tool":"view_image","path":""}`, want: `field "path" cannot be empty`},
		{name: "path on a command rule", rule: `{"command":["ls"],"path":"x"}`, want: `field "path" belongs to a rule that names "tool"`},
		{name: "detail is not a channel", rule: `{"tool":"view_image","detail":"low"}`, want: "unknown field"},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCommandRule(tt.rule, matchBareCommandOnly)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseCommandRule(%s) error = %v, want it to contain %q", tt.rule, err, tt.want)
			}
		})
	}
}

// An array is an argv prefix and nothing else: ["view_image"] grants a
// command called view_image, and no image.
func TestImageRulesAndCommandRulesDoNotCross(t *testing.T) {
	array := imageRule(t, `["view_image"]`, matchBareCommandOnly)
	if array.matchesImage(ViewImageArgs{Path: "shot.png"}) {
		t.Fatal("an array rule matched an image call")
	}
	if !array.matches(UniversalCommandArgs{Command: []string{"view_image"}}) {
		t.Fatal("an array rule stopped matching the command it names")
	}

	image := imageRule(t, `{"tool":"view_image"}`, matchBareCommandOnly)
	if image.matches(UniversalCommandArgs{Command: []string{"view_image"}}) {
		t.Fatal("an image rule matched a command call")
	}
	if !image.matchesImage(ViewImageArgs{Path: "anything.png"}) {
		t.Fatal("an any-image rule did not match an image call")
	}
	if rule("ls").matchesImage(ViewImageArgs{Path: "ls"}) {
		t.Fatal("a command rule matched an image call")
	}
}

func TestImageRulePathContainment(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tests := []struct {
		name string
		rule string
		call string
		want bool
	}{
		{name: "file inside", rule: "plots", call: "plots/out.png", want: true},
		{name: "file nested deeper", rule: "plots", call: "plots/2026/out.png", want: true},
		{name: "the rule path itself", rule: "plots/out.png", call: "plots/out.png", want: true},
		{name: "rule with trailing separator", rule: "plots/", call: "plots/out.png", want: true},
		{name: "dot-dot that stays inside", rule: "plots", call: "plots/sub/../out.png", want: true},
		{name: "dot-dot that walks out", rule: "plots", call: "plots/../secret.png", want: false},
		{name: "sibling sharing the prefix", rule: "plots", call: "plots-private/out.png", want: false},
		{name: "another file", rule: "plots/out.png", call: "plots/other.png", want: false},
		{name: "absolute rule, relative call", rule: filepath.Join(cwd, "plots"), call: "plots/out.png", want: true},
		{name: "relative rule, absolute call", rule: "plots", call: filepath.Join(cwd, "plots", "out.png"), want: true},
		{name: "absolute call elsewhere", rule: "plots", call: "/tmp/plots/out.png", want: false},
		{name: "root grants everything", rule: "/", call: "/tmp/out.png", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imagePathWithin(tt.rule, tt.call); got != tt.want {
				t.Fatalf("imagePathWithin(%q, %q) = %v, want %v", tt.rule, tt.call, got, tt.want)
			}
		})
	}
}

func TestApprovalPolicyDecideImage(t *testing.T) {
	grantPlots := imageRule(t, `{"tool":"view_image","path":"plots"}`, matchBareCommandOnly)
	denyEtc := imageRule(t, `{"tool":"view_image","path":"/etc"}`, matchAnyCall)
	denyAll := imageRule(t, `{"tool":"view_image"}`, matchAnyCall)

	tests := []struct {
		name   string
		policy approvalPolicy
		call   string
		want   approvalDecision
	}{
		{
			name:   "whitelisted directory allows without a prompt",
			policy: approvalPolicy{whitelist: []commandRule{grantPlots}, whitelistConfigured: true},
			call:   "plots/out.png",
			want:   decisionAllow,
		},
		{
			name:   "outside the directory is unlisted",
			policy: approvalPolicy{whitelist: []commandRule{grantPlots}, whitelistConfigured: true},
			call:   "notes/out.png",
			want:   decisionDenyNotWhitelisted,
		},
		{
			name:   "a whitelist of commands alone lists no image",
			policy: approvalPolicy{whitelist: []commandRule{rule("ls")}, whitelistConfigured: true, autoApprove: true},
			call:   "plots/out.png",
			want:   decisionDenyNotWhitelisted,
		},
		{
			name:   "blacklist beats the whitelist and auto-approve",
			policy: approvalPolicy{blacklist: []commandRule{denyEtc}, whitelist: []commandRule{imageRule(t, `{"tool":"view_image"}`, matchBareCommandOnly)}, autoApprove: true},
			call:   "/etc/motd.png",
			want:   decisionDenyBlacklist,
		},
		{
			name:   "blacklist of every image",
			policy: approvalPolicy{blacklist: []commandRule{denyAll}, autoApprove: true},
			call:   "plots/out.png",
			want:   decisionDenyBlacklist,
		},
		{
			name:   "unlisted with a prompt available asks",
			policy: approvalPolicy{whitelist: []commandRule{grantPlots}, canPrompt: true},
			call:   "notes/out.png",
			want:   decisionRequireApproval,
		},
		{
			name:   "unlisted with auto-approve and no whitelist allows",
			policy: approvalPolicy{autoApprove: true},
			call:   "notes/out.png",
			want:   decisionAllow,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.decideImage(ViewImageArgs{Path: tt.call}); got != tt.want {
				t.Fatalf("decideImage(%q) = %v, want %v", tt.call, got, tt.want)
			}
		})
	}
}

func TestSuggestedImageRuleNamesTheFileAndParses(t *testing.T) {
	suggestion := suggestedImageRuleJSON(ViewImageArgs{Path: "plots/out.png", Detail: "high"})
	if want := `{"tool":"view_image","path":"plots/out.png"}`; suggestion != want {
		t.Fatalf("suggestedImageRuleJSON() = %s, want %s", suggestion, want)
	}
	granted := imageRule(t, suggestion, matchBareCommandOnly)
	if !granted.matchesImage(ViewImageArgs{Path: "plots/out.png"}) {
		t.Fatal("the suggested rule does not admit the call it was written for")
	}
	if granted.matchesImage(ViewImageArgs{Path: "plots/other.png"}) {
		t.Fatal("the suggested rule admits a file it does not name")
	}
}

func TestViewImageWhitelistRuleRunsWithoutAPrompt(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "plots", "out.png")
	if err := os.MkdirAll(filepath.Dir(image), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(image, pngBytes, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	whitelist := filepath.Join(dir, "wl.txt")
	if err := os.WriteFile(whitelist, []byte(`{"tool":"view_image","path":"`+filepath.Join(dir, "plots")+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write whitelist: %v", err)
	}
	// No prompt can be answered and the approver would refuse anyway: the
	// rule alone admits the file.
	approver := NewTestApprover(false)
	executor := newViewImageExecutor(t, RequestOptions{ToolWhitelist: whitelist, ToolNonInteractive: true}, approver)

	result := singleResult(t, executor.ExecuteParallel(context.Background(),
		[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: image})}, nil))

	if result.Error != "" || len(result.Images) != 1 || result.Images[0].Name != "out.png" {
		t.Fatalf("result = %#v, want the image attached under the rule", result)
	}
	if len(approver.ImageApprovalCalls) != 0 {
		t.Fatal("a whitelisted image was still put to the approver")
	}
}

// The rules are consulted before the file is opened: a blacklisted path that
// does not exist is reported as blacklisted, which is only possible if
// nothing tried to read it.
func TestViewImageBlacklistRuleDeniesBeforeTheFileIsRead(t *testing.T) {
	dir := t.TempDir()
	blacklist := filepath.Join(dir, "bl.txt")
	if err := os.WriteFile(blacklist, []byte(`{"tool":"view_image","path":"`+dir+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write blacklist: %v", err)
	}
	executor := newViewImageExecutor(t, RequestOptions{ToolBlacklist: blacklist, ToolAutoApprove: true}, NewTestApprover(true))

	result := singleResult(t, executor.ExecuteParallel(context.Background(),
		[]ToolCall{viewImagePathCall(t, "img", ViewImageArgs{Path: filepath.Join(dir, "missing.png")})}, nil))

	if !result.NotRun || result.Reason != "blacklisted" || result.Error != "denied: blacklisted" {
		t.Fatalf("result = %#v, want the blacklist denial", result)
	}
}
