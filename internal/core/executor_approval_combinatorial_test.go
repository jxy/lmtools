package core

import (
	"testing"
)

// policyConfig represents a combination of policy settings for testing
type policyConfig struct {
	hasBlacklist bool
	hasWhitelist bool
	autoApprove  bool
	interactive  bool
}

// TestApprovalPolicyDecideCombinatorialCases tests all combinations of approval policy settings
// to ensure the precedence order is correctly implemented:
// 1. Blacklist (highest precedence)
// 2. Whitelist
// 3. Non-interactive mode with whitelist
// 4. Auto-approve
// 5. Non-interactive mode
// 6. Interactive mode (lowest precedence)
func TestApprovalPolicyDecideCombinatorialCases(t *testing.T) {
	// Define test commands
	blacklistedCmd := []string{"rm", "-rf", "/"}
	whitelistedCmd := []string{"ls", "-la"}
	neutralCmd := []string{"echo", "hello"}

	// Generate all combinations (2^4 = 16)
	var configs []policyConfig
	for i := 0; i < 16; i++ {
		configs = append(configs, policyConfig{
			hasBlacklist: (i & 0x8) != 0,
			hasWhitelist: (i & 0x4) != 0,
			autoApprove:  (i & 0x2) != 0,
			interactive:  (i & 0x1) != 0,
		})
	}

	for _, config := range configs {
		// Build policy based on config
		policy := ApprovalPolicy{
			AutoApprove: config.autoApprove,
			Interactive: config.interactive,
		}
		if config.hasBlacklist {
			policy.Blacklist = [][]string{{"rm", "-rf"}}
		}
		if config.hasWhitelist {
			policy.Whitelist = [][]string{{"ls"}}
		}

		// Test blacklisted command
		t.Run(buildTestName("blacklisted", config), func(t *testing.T) {
			decision := policy.Decide(blacklistedCmd)
			if config.hasBlacklist {
				// Blacklist always wins
				if decision != DecisionDenyBlacklist {
					t.Errorf("Expected DecisionDenyBlacklist, got %v", decision)
				}
			} else {
				// No blacklist, check other rules
				verifyNonBlacklistDecision(t, decision, blacklistedCmd, policy, config, false)
			}
		})

		// Test whitelisted command
		t.Run(buildTestName("whitelisted", config), func(t *testing.T) {
			decision := policy.Decide(whitelistedCmd)
			if config.hasBlacklist && commandHasPrefix(whitelistedCmd, policy.Blacklist[0]) {
				// This shouldn't happen with our test data, but check anyway
				if decision != DecisionDenyBlacklist {
					t.Errorf("Expected DecisionDenyBlacklist, got %v", decision)
				}
			} else if config.hasWhitelist {
				// Whitelist match always allows
				if decision != DecisionAllow {
					t.Errorf("Expected DecisionAllow for whitelisted command, got %v", decision)
				}
			} else {
				// No whitelist match
				verifyNonBlacklistDecision(t, decision, whitelistedCmd, policy, config, false)
			}
		})

		// Test neutral command (not in blacklist or whitelist)
		t.Run(buildTestName("neutral", config), func(t *testing.T) {
			decision := policy.Decide(neutralCmd)
			verifyNonBlacklistDecision(t, decision, neutralCmd, policy, config, true)
		})
	}
}

func buildTestName(cmdType string, config policyConfig) string {
	return cmdType + "_cmd" +
		boolStr("_blacklist", config.hasBlacklist) +
		boolStr("_whitelist", config.hasWhitelist) +
		boolStr("_auto", config.autoApprove) +
		boolStr("_interactive", config.interactive)
}

func boolStr(prefix string, val bool) string {
	if val {
		return prefix
	}
	return ""
}

func verifyNonBlacklistDecision(t *testing.T, decision ApprovalDecision, cmd []string, policy ApprovalPolicy, config policyConfig, isNeutral bool) {
	// Follow the precedence order
	if config.hasWhitelist && !isNeutral && commandHasPrefix(cmd, policy.Whitelist[0]) {
		if decision != DecisionAllow {
			t.Errorf("Expected DecisionAllow for whitelisted command, got %v", decision)
		}
	} else if config.hasWhitelist && !config.interactive {
		if decision != DecisionDenyNotWhitelisted {
			t.Errorf("Expected DecisionDenyNotWhitelisted, got %v", decision)
		}
	} else if config.autoApprove {
		if decision != DecisionAllow {
			t.Errorf("Expected DecisionAllow with auto-approve, got %v", decision)
		}
	} else if !config.interactive {
		if decision != DecisionDenyNonInteractive {
			t.Errorf("Expected DecisionDenyNonInteractive, got %v", decision)
		}
	} else {
		if decision != DecisionRequireApproval {
			t.Errorf("Expected DecisionRequireApproval, got %v", decision)
		}
	}
}

// TestApprovalPolicyPrecedenceOrder explicitly tests the documented precedence order
func TestApprovalPolicyPrecedenceOrder(t *testing.T) {
	tests := []struct {
		name        string
		policy      ApprovalPolicy
		command     []string
		expected    ApprovalDecision
		description string
	}{
		// Test 1: Blacklist overrides everything
		{
			name: "blacklist_overrides_all",
			policy: ApprovalPolicy{
				Blacklist:   [][]string{{"dangerous"}},
				Whitelist:   [][]string{{"dangerous"}}, // Even if whitelisted
				AutoApprove: true,                      // Even with auto-approve
				Interactive: true,                      // Even in interactive mode
			},
			command:     []string{"dangerous", "command"},
			expected:    DecisionDenyBlacklist,
			description: "Blacklist has highest precedence",
		},

		// Test 2: Whitelist overrides auto-approve and interactive
		{
			name: "whitelist_overrides_lower",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"safe"}},
				AutoApprove: false, // Even without auto-approve
				Interactive: true,  // Even in interactive mode
			},
			command:     []string{"safe", "command"},
			expected:    DecisionAllow,
			description: "Whitelist match allows regardless of other settings",
		},

		// Test 3: Non-interactive with whitelist but no match
		{
			name: "non_interactive_whitelist_no_match",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"safe"}},
				AutoApprove: true, // Auto-approve is ignored
				Interactive: false,
			},
			command:     []string{"other", "command"},
			expected:    DecisionDenyNotWhitelisted,
			description: "Non-interactive with whitelist requires match",
		},

		// Test 4: Auto-approve when no whitelist/blacklist match
		{
			name: "auto_approve_takes_effect",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"safe"}},
				AutoApprove: true,
				Interactive: true,
			},
			command:     []string{"other", "command"},
			expected:    DecisionAllow,
			description: "Auto-approve allows when no list matches",
		},

		// Test 5: Non-interactive without whitelist
		{
			name: "non_interactive_no_whitelist",
			policy: ApprovalPolicy{
				AutoApprove: false,
				Interactive: false,
			},
			command:     []string{"any", "command"},
			expected:    DecisionDenyNonInteractive,
			description: "Non-interactive without whitelist denies all",
		},

		// Test 6: Interactive mode prompts user
		{
			name: "interactive_prompts",
			policy: ApprovalPolicy{
				AutoApprove: false,
				Interactive: true,
			},
			command:     []string{"any", "command"},
			expected:    DecisionRequireApproval,
			description: "Interactive mode prompts for approval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.Decide(tt.command)
			if got != tt.expected {
				t.Errorf("%s: got %v, want %v", tt.description, got, tt.expected)
			}
		})
	}
}

// TestApprovalPolicyEdgeCases tests edge cases and boundary conditions
func TestApprovalPolicyEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		policy   ApprovalPolicy
		command  []string
		expected ApprovalDecision
	}{
		// Empty lists
		{
			name: "empty_blacklist_and_whitelist",
			policy: ApprovalPolicy{
				Blacklist:   [][]string{},
				Whitelist:   [][]string{},
				AutoApprove: false,
				Interactive: true,
			},
			command:  []string{"cmd"},
			expected: DecisionRequireApproval,
		},

		// Multiple entries in lists
		{
			name: "multiple_blacklist_entries",
			policy: ApprovalPolicy{
				Blacklist: [][]string{
					{"rm", "-rf"},
					{"dd", "if=/dev/zero"},
					{"mkfs"},
				},
				Interactive: true,
			},
			command:  []string{"dd", "if=/dev/zero", "of=/dev/sda"},
			expected: DecisionDenyBlacklist,
		},
		{
			name: "multiple_whitelist_entries",
			policy: ApprovalPolicy{
				Whitelist: [][]string{
					{"git"},
					{"ls"},
					{"cat"},
				},
				Interactive: true,
			},
			command:  []string{"cat", "/etc/passwd"},
			expected: DecisionAllow,
		},

		// Exact vs prefix matching
		{
			name: "blacklist_prefix_blocks_longer_command",
			policy: ApprovalPolicy{
				Blacklist:   [][]string{{"sudo"}},
				Interactive: true,
			},
			command:  []string{"sudo", "apt", "update"},
			expected: DecisionDenyBlacklist,
		},
		{
			name: "whitelist_prefix_allows_longer_command",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"git"}},
				Interactive: true,
			},
			command:  []string{"git", "commit", "-m", "test"},
			expected: DecisionAllow,
		},

		// Command shorter than list entry
		{
			name: "command_shorter_than_blacklist_entry",
			policy: ApprovalPolicy{
				Blacklist:   [][]string{{"git", "push", "origin", "master", "--force"}},
				Interactive: true,
			},
			command:  []string{"git", "push"},
			expected: DecisionRequireApproval, // No match because command is shorter
		},

		// Case sensitivity (assuming case-sensitive matching)
		{
			name: "case_sensitive_no_match",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"LS"}},
				Interactive: true,
			},
			command:  []string{"ls", "-la"},
			expected: DecisionRequireApproval, // "LS" != "ls"
		},

		// Very long commands
		{
			name: "very_long_command",
			policy: ApprovalPolicy{
				Whitelist:   [][]string{{"python"}},
				Interactive: true,
			},
			command: []string{"python", "-c", "import sys; " +
				"print('x' * 1000); " +
				"sys.exit(0)"},
			expected: DecisionAllow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.Decide(tt.command)
			if got != tt.expected {
				t.Errorf("ApprovalPolicy.Decide() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestApprovalPolicyRealWorldScenarios tests common real-world command patterns
func TestApprovalPolicyRealWorldScenarios(t *testing.T) {
	// Common development policy
	devPolicy := ApprovalPolicy{
		Blacklist: [][]string{
			{"rm", "-rf", "/"},
			{"dd", "if=/dev/zero"},
			{"mkfs"},
			{"format"},
			{":(){:|:&};:"}, // Fork bomb
		},
		Whitelist: [][]string{
			{"git"},
			{"go"},
			{"make"},
			{"docker"},
			{"kubectl"},
			{"npm"},
			{"yarn"},
			{"python"},
			{"pip"},
			{"cargo"},
			{"rustc"},
		},
		AutoApprove: false,
		Interactive: true,
	}

	tests := []struct {
		name     string
		command  []string
		expected ApprovalDecision
	}{
		// Allowed development commands
		{
			name:     "git_commit",
			command:  []string{"git", "commit", "-m", "feat: add new feature"},
			expected: DecisionAllow,
		},
		{
			name:     "go_test",
			command:  []string{"go", "test", "./..."},
			expected: DecisionAllow,
		},
		{
			name:     "npm_install",
			command:  []string{"npm", "install", "express"},
			expected: DecisionAllow,
		},

		// Dangerous commands that should be blocked
		{
			name:     "rm_root",
			command:  []string{"rm", "-rf", "/"},
			expected: DecisionDenyBlacklist,
		},
		{
			name:     "fork_bomb",
			command:  []string{":(){:|:&};:"},
			expected: DecisionDenyBlacklist,
		},

		// Commands requiring approval
		{
			name:     "curl_download",
			command:  []string{"curl", "-O", "https://example.com/file.zip"},
			expected: DecisionRequireApproval,
		},
		{
			name:     "system_update",
			command:  []string{"apt", "update"},
			expected: DecisionRequireApproval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := devPolicy.Decide(tt.command)
			if got != tt.expected {
				t.Errorf("DevPolicy.Decide(%v) = %v, want %v", tt.command, got, tt.expected)
			}
		})
	}
}
