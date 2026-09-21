package core

import (
	"path/filepath"
	"testing"
)

// The example files are the first rule syntax most operators see, and a denial
// tells them to paste an object rule into one. If the loader and the examples
// disagree, the remediation loop the denial describes is closed.
func TestShippedExampleRuleFilesLoad(t *testing.T) {
	whitelistPath := filepath.Join("..", "..", "examples", "lmc", "tool-whitelist.txt")
	blacklistPath := filepath.Join("..", "..", "examples", "lmc", "tool-blacklist.txt")

	whitelist, err := loadCommandRules(whitelistPath, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("load example whitelist: %v", err)
	}
	blacklist, err := loadCommandRules(blacklistPath, matchAnyCall)
	if err != nil {
		t.Fatalf("load example blacklist: %v", err)
	}

	if !hasRuleMatchMode(whitelist, matchExactChannels) {
		t.Fatal("example whitelist has no object rule, so it never exercises the form denials tell users to add")
	}
	if !hasRuleMatchMode(blacklist, matchNamedChannelSubset) {
		t.Fatal("example blacklist has no object rule")
	}

	policy := approvalPolicy{whitelist: whitelist, blacklist: blacklist}

	// An array grant still admits the plain command.
	if got := policy.decide(UniversalCommandArgs{Command: []string{"ls", "-la"}}); got != decisionAllow {
		t.Fatalf("plain ls decision = %v, want allow", got)
	}
	// The documented image grant admits the directory it names and nothing
	// beside it, and the documented image denial holds against auto-approve.
	if got := policy.decideImage(ViewImageArgs{Path: "plots/out.png"}); got != decisionAllow {
		t.Fatalf("plots image decision = %v, want allow", got)
	}
	if got := policy.decideImage(ViewImageArgs{Path: "plots/../secret.png"}); got == decisionAllow {
		t.Fatal("the example image grant admitted a path that walks out of plots")
	}
	// The documented MCP grants admit the one tool and the whole server they
	// name and nothing beside them.
	if got := policy.decideMCP("filesystem", "read_file"); got != decisionAllow {
		t.Fatalf("filesystem read_file decision = %v, want allow", got)
	}
	if got := policy.decideMCP("docs", "search"); got != decisionAllow {
		t.Fatalf("docs search decision = %v, want allow", got)
	}
	if got := policy.decideMCP("filesystem", "list_directory"); got == decisionAllow {
		t.Fatal("the example MCP grants admitted a filesystem tool they do not name")
	}
	policy.autoApprove = true
	if got := policy.decideImage(ViewImageArgs{Path: "/etc/motd.png"}); got != decisionDenyBlacklist {
		t.Fatalf("/etc image decision = %v, want blacklist denial", got)
	}
	if got := policy.decideMCP("filesystem", "write_file"); got != decisionDenyBlacklist {
		t.Fatalf("filesystem write_file decision = %v, want blacklist denial", got)
	}
	// The documented object grant admits its exact shape.
	if got := policy.decide(UniversalCommandArgs{
		Command:    []string{"go", "test", "./..."},
		StdoutFile: "test-output.txt",
	}); got != decisionAllow {
		t.Fatalf("redirected go test decision = %v, want allow", got)
	}
	// So does the documented environ grant, and it grants that environment only.
	if got := policy.decide(UniversalCommandArgs{
		Command: []string{"go", "test", "./..."},
		Environ: map[string]string{"GOFLAGS": "-count=1"},
	}); got != decisionAllow {
		t.Fatalf("go test with the granted environment decision = %v, want allow", got)
	}
	if got := policy.decide(UniversalCommandArgs{
		Command: []string{"go", "test", "./..."},
		Environ: map[string]string{"GOFLAGS": "-count=1", "LD_PRELOAD": "/tmp/evil.so"},
	}); got == decisionAllow {
		t.Fatal("an example grant admitted an environment it does not name")
	}
	// The object denial holds even with an extra field the rule never named.
	if got := policy.decide(UniversalCommandArgs{
		Command:    []string{"tee"},
		StdoutFile: "/etc/hosts",
		StderrFile: "/tmp/e",
	}); got != decisionDenyBlacklist {
		t.Fatalf("tee to /etc/hosts decision = %v, want blacklist denial", got)
	}
	// And an array denial still beats an array grant.
	if got := policy.decide(UniversalCommandArgs{Command: []string{"rm", "-rf", "/"}}); got != decisionDenyBlacklist {
		t.Fatalf("rm decision = %v, want blacklist denial", got)
	}
}

func hasRuleMatchMode(rules []commandRule, mode ruleMatchMode) bool {
	for _, rule := range rules {
		if rule.matchMode == mode {
			return true
		}
	}
	return false
}
