package core

import (
	"testing"
)

func TestApprovalPolicy_Decide(t *testing.T) {
	tests := []struct {
		name         string
		policy       ApprovalPolicy
		command      []string
		wantDecision ApprovalDecision
	}{
		// Blacklist tests (highest precedence)
		{
			name: "blacklist blocks even if whitelisted",
			policy: ApprovalPolicy{
				Blacklist:   [][]string{{"rm", "-rf"}},
				Whitelist:   [][]string{{"rm", "-rf", "/tmp"}},
				AutoApprove: true,
				Interactive: true,
			},
			command:      []string{"rm", "-rf", "/tmp"},
			wantDecision: DecisionDenyBlacklist,
		},
		{
			name: "blacklist partial match blocks",
			policy: ApprovalPolicy{
				Blacklist:   [][]string{{"sudo"}},
				Interactive: true,
			},
			command:      []string{"sudo", "apt", "update"},
			wantDecision: DecisionDenyBlacklist,
		},

		// Whitelist tests
		{
			name: "whitelist exact match allows",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"ls", "-la"}},
				Interactive: true,
			},
			command:      []string{"ls", "-la"},
			wantDecision: DecisionAllow,
		},
		{
			name: "whitelist prefix match allows",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"git"}},
				Interactive: true,
			},
			command:      []string{"git", "status"},
			wantDecision: DecisionAllow,
		},
		{
			name: "whitelist no match with non-interactive denies",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"ls"}},
				Interactive: false,
			},
			command:      []string{"cat", "file.txt"},
			wantDecision: DecisionDenyNotWhitelisted,
		},

		// Auto-approve tests
		{
			name: "auto-approve allows without whitelist",
			policy: ApprovalPolicy{
				AutoApprove: true,
				Interactive: true,
			},
			command:      []string{"echo", "hello"},
			wantDecision: DecisionAllow,
		},
		{
			name: "auto-approve with empty whitelist allows",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{},
				AutoApprove: true,
				Interactive: true,
			},
			command:      []string{"echo", "hello"},
			wantDecision: DecisionAllow,
		},

		// Non-interactive tests
		{
			name: "non-interactive without whitelist denies",
			policy: ApprovalPolicy{
				Interactive: false,
			},
			command:      []string{"echo", "hello"},
			wantDecision: DecisionDenyNonInteractive,
		},
		{
			name: "non-interactive with whitelist but no match denies",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"ls"}},
				Interactive: false,
			},
			command:      []string{"echo", "hello"},
			wantDecision: DecisionDenyNotWhitelisted,
		},

		// Interactive tests
		{
			name: "interactive without whitelist requires approval",
			policy: ApprovalPolicy{
				Interactive: true,
			},
			command:      []string{"echo", "hello"},
			wantDecision: DecisionRequireApproval,
		},
		{
			name: "interactive with whitelist no match requires approval",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"ls"}},
				Interactive: true,
			},
			command:      []string{"echo", "hello"},
			wantDecision: DecisionRequireApproval,
		},

		// Edge cases removed - empty command validation is now done in executeSingle
		{
			name: "single element whitelist matches",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"python"}},
				Interactive: true,
			},
			command:      []string{"python", "script.py"},
			wantDecision: DecisionAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.Decide(tt.command)
			if got != tt.wantDecision {
				t.Errorf("ApprovalPolicy.Decide() = %v, want %v", got, tt.wantDecision)
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
