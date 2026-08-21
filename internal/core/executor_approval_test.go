package core

import (
	"strings"
	"testing"
)

func rule(command ...string) commandRule {
	return commandRule{command: command}
}

// TestApprovalPolicyDecide covers what TestApprovalPolicyMatrix has no axis
// for: blacklist precedence, and how a rule's argv prefix matches a real
// multi-argument command. The plain autoApprove/canPrompt/whitelist cells are
// enumerated exhaustively by the matrix and are not restated here.
func TestApprovalPolicyDecide(t *testing.T) {
	tests := []struct {
		name         string
		policy       approvalPolicy
		command      []string
		wantDecision approvalDecision
	}{
		// Blacklist tests (highest precedence)
		{
			name: "blacklist blocks even if whitelisted",
			policy: approvalPolicy{
				blacklist:   []commandRule{rule("rm", "-rf")},
				whitelist:   []commandRule{rule("rm", "-rf", "/tmp")},
				autoApprove: true,
			},
			command:      []string{"rm", "-rf", "/tmp"},
			wantDecision: decisionDenyBlacklist,
		},
		{
			name: "blacklist partial match blocks",
			policy: approvalPolicy{
				blacklist: []commandRule{rule("sudo")},
			},
			command:      []string{"sudo", "apt", "update"},
			wantDecision: decisionDenyBlacklist,
		},

		// Whitelist tests
		{
			name: "whitelist exact match allows",
			policy: approvalPolicy{
				whitelist: []commandRule{rule("ls", "-la")},
			},
			command:      []string{"ls", "-la"},
			wantDecision: decisionAllow,
		},
		{
			name: "whitelist prefix match allows",
			policy: approvalPolicy{
				whitelist: []commandRule{rule("git")},
			},
			command:      []string{"git", "status"},
			wantDecision: decisionAllow,
		},
		{
			name: "whitelist no match with non-interactive denies",
			policy: approvalPolicy{
				whitelist:      []commandRule{rule("ls")},
				nonInteractive: true,
			},
			command:      []string{"cat", "file.txt"},
			wantDecision: decisionDenyNotWhitelisted,
		},

		// A whitelist that parsed to no rules is still a whitelist. The file
		// being empty, truncated, or all comments is not consent to run
		// everything, and auto-approve does not turn it into consent either.
		{
			name: "auto-approve with a configured but empty whitelist denies",
			policy: approvalPolicy{
				whitelistConfigured: true,
				autoApprove:         true,
			},
			command:      []string{"echo", "hello"},
			wantDecision: decisionDenyNotWhitelisted,
		},
		{
			name: "configured but empty whitelist still defers to the blacklist",
			policy: approvalPolicy{
				whitelistConfigured: true,
				blacklist:           []commandRule{rule("echo")},
				autoApprove:         true,
			},
			command:      []string{"echo", "hello"},
			wantDecision: decisionDenyBlacklist,
		},
		// A policy assembled in code has no path to have configured, so there
		// the rule count is the only signal there is; an allocated but empty
		// slice is not a whitelist.
		{
			name: "auto-approve with no whitelist at all allows",
			policy: approvalPolicy{
				whitelist:   []commandRule{},
				autoApprove: true,
			},
			command:      []string{"echo", "hello"},
			wantDecision: decisionAllow,
		},

		{
			name: "single element whitelist matches",
			policy: approvalPolicy{
				whitelist: []commandRule{rule("python")},
			},
			command:      []string{"python", "script.py"},
			wantDecision: decisionAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.decide(UniversalCommandArgs{Command: tt.command})
			if got != tt.wantDecision {
				t.Errorf("approvalPolicy.decide() = %v, want %v", got, tt.wantDecision)
			}
		})
	}
}

func TestCommandPrefixMatch(t *testing.T) {
	tests := []struct {
		name      string
		prefix    []string
		command   []string
		wantMatch bool
	}{
		{
			name:      "exact match",
			prefix:    []string{"git", "commit"},
			command:   []string{"git", "commit"},
			wantMatch: true,
		},
		{
			name:      "prefix match",
			prefix:    []string{"git"},
			command:   []string{"git", "commit", "-m", "test"},
			wantMatch: true,
		},
		{
			name:      "no match - different command",
			prefix:    []string{"git"},
			command:   []string{"ls", "-la"},
			wantMatch: false,
		},
		{
			name:      "no match - command shorter than prefix",
			prefix:    []string{"git", "commit"},
			command:   []string{"git"},
			wantMatch: false,
		},
		{
			name:      "empty prefix matches nothing",
			prefix:    []string{},
			command:   []string{"ls"},
			wantMatch: false,
		},
		{
			name:      "empty command matches nothing",
			prefix:    []string{"ls"},
			command:   []string{},
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commandHasPrefix(tt.command, tt.prefix)
			if got != tt.wantMatch {
				t.Errorf("commandPrefixMatch(%v, %v) = %v, want %v",
					tt.prefix, tt.command, got, tt.wantMatch)
			}
		})
	}
}

func TestCommandRuleSyntaxAndRedirectionMatching(t *testing.T) {
	plainWhitelist, err := parseCommandRule(`["sort"]`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse plain whitelist rule: %v", err)
	}
	if !plainWhitelist.matches(UniversalCommandArgs{Command: []string{"sort"}}) {
		t.Fatal("plain whitelist rule did not match a command without file redirection")
	}
	redirected := []struct {
		name string
		args UniversalCommandArgs
	}{
		{name: "stdin", args: UniversalCommandArgs{Command: []string{"sort"}, StdinFile: "in.txt"}},
		{name: "stdout", args: UniversalCommandArgs{Command: []string{"sort"}, StdoutFile: "out.txt"}},
		{name: "stderr", args: UniversalCommandArgs{Command: []string{"sort"}, StderrFile: "err.txt"}},
	}
	for _, tt := range redirected {
		t.Run("plain whitelist rejects "+tt.name, func(t *testing.T) {
			if plainWhitelist.matches(tt.args) {
				t.Fatal("plain whitelist rule matched a redirected command")
			}
		})
	}
	// A grant written before the stdin channel existed cannot be read as
	// consent to it: ["python3"] means "run the interpreter", not "run whatever
	// program the model writes into it".
	literal := "beta\nalpha\n"
	if plainWhitelist.matches(UniversalCommandArgs{Command: []string{"sort"}, Stdin: &literal}) {
		t.Fatal("plain whitelist rule granted a command carrying literal stdin")
	}
	empty := ""
	if plainWhitelist.matches(UniversalCommandArgs{Command: []string{"sort"}, Stdin: &empty}) {
		t.Fatal("plain whitelist rule granted an explicitly empty literal stdin")
	}

	plainBlacklist, err := parseCommandRule(`["sort"]`, matchAnyCall)
	if err != nil {
		t.Fatalf("parse plain blacklist rule: %v", err)
	}
	if !plainBlacklist.matches(UniversalCommandArgs{
		Command:    []string{"sort"},
		Workdir:    "/tmp/job",
		StdinFile:  "in.txt",
		StdoutFile: "out.txt",
		StderrFile: "err.txt",
	}) {
		t.Fatal("legacy blacklist rule became weaker for redirected commands")
	}

	exact, err := parseCommandRule(`{"command":["sort"],"workdir":"/tmp/job","stdin_file":"in.txt","stdout_file":"out.txt","stderr_file":"err.txt"}`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse exact rule: %v", err)
	}
	want := UniversalCommandArgs{
		Command:    []string{"sort", "-r"},
		Workdir:    "/tmp/job",
		StdinFile:  "in.txt",
		StdoutFile: "out.txt",
		StderrFile: "err.txt",
	}
	if !exact.matches(want) {
		t.Fatal("exact object rule did not match its argv prefix and I/O shape")
	}
	for name, mutate := range map[string]func(*UniversalCommandArgs){
		"workdir": func(args *UniversalCommandArgs) { args.Workdir = "/tmp/other" },
		"stdin":   func(args *UniversalCommandArgs) { args.StdinFile = "other.txt" },
		"stdout":  func(args *UniversalCommandArgs) { args.StdoutFile = "other.txt" },
		"stderr":  func(args *UniversalCommandArgs) { args.StderrFile = "other.txt" },
	} {
		t.Run(name+" mismatch", func(t *testing.T) {
			args := want
			mutate(&args)
			if exact.matches(args) {
				t.Fatalf("exact rule matched changed %s", name)
			}
		})
	}
}

func TestCommandRuleObjectRejectsAmbiguousFields(t *testing.T) {
	tests := []struct {
		name string
		rule string
		want string
	}{
		{name: "unknown field", rule: `{"command":["sort"],"stdout":"out.txt"}`, want: "unknown field"},
		{name: "empty stdin file", rule: `{"command":["sort"],"stdin_file":""}`, want: `field "stdin_file" cannot be empty`},
		{name: "empty stdout file", rule: `{"command":["sort"],"stdout_file":""}`, want: `field "stdout_file" cannot be empty`},
		{name: "no redirection", rule: `{"command":["sort"]}`, want: "must include environ, stdin, stdin_file, stdout_file, or stderr_file"},
		{name: "workdir only", rule: `{"command":["sort"],"workdir":"/tmp"}`, want: "must include environ, stdin, stdin_file, stdout_file, or stderr_file"},
		{name: "empty command", rule: `{"command":[],"stdout_file":"out.txt"}`, want: "empty command array"},
		{name: "not JSON rule", rule: `sort`, want: "JSON command array or object"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCommandRule(tt.rule, matchBareCommandOnly)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseCommandRule() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestApprovalPolicyTreatsRedirectedCommandsAsDistinctGrants(t *testing.T) {
	plain := rule("sort")
	exact := commandRule{
		command:    []string{"sort"},
		workdir:    stringPtr("/tmp/job"),
		stdinFile:  stringPtr("in.txt"),
		stdoutFile: stringPtr("out.txt"),
		matchMode:  matchExactChannels,
	}
	policy := approvalPolicy{
		whitelist: []commandRule{plain, exact},
		canPrompt: true,
	}

	if got := policy.decide(UniversalCommandArgs{Command: []string{"sort"}}); got != decisionAllow {
		t.Fatalf("plain command decision = %v, want allow", got)
	}
	if got := policy.decide(UniversalCommandArgs{Command: []string{"sort"}, StdoutFile: "other.txt"}); got != decisionRequireApproval {
		t.Fatalf("unlisted redirection decision = %v, want approval", got)
	}
	if got := policy.decide(UniversalCommandArgs{
		Command:    []string{"sort", "-r"},
		Workdir:    "/tmp/job",
		StdinFile:  "in.txt",
		StdoutFile: "out.txt",
	}); got != decisionAllow {
		t.Fatalf("listed redirection decision = %v, want allow", got)
	}

	policy.blacklist = []commandRule{{
		command:    []string{"sort"},
		stdoutFile: stringPtr("out.txt"),
		matchMode:  matchExactChannels,
	}}
	if got := policy.decide(UniversalCommandArgs{Command: []string{"sort"}, StdoutFile: "out.txt"}); got != decisionDenyBlacklist {
		t.Fatalf("exact blacklisted redirection decision = %v, want blacklist denial", got)
	}
}
