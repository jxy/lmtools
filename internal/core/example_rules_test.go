package core

import (
	"path/filepath"
	"testing"
)

// The example files are the first rule syntax most operators see, and a denial
// tells them to paste an object rule into one. If the loader and the examples
// disagree, the remediation loop the denial describes is closed.
func TestShippedExampleRuleFilesLoad(t *testing.T) {
	whitelistPath := filepath.Join("..", "..", "examples", "tools", "whitelist.txt")
	blacklistPath := filepath.Join("..", "..", "examples", "tools", "blacklist.txt")

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
