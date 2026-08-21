package core

import (
	"context"
	"fmt"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// whitelistState is the third axis of the matrix. A configured whitelist that
// parsed to no rules is its own case, not a synonym for either neighbour:
// loadCommandRules returns (nil, nil) for a file that is empty, truncated, or
// entirely comments, so the state is reachable from an ordinary
// -tool-whitelist and used to behave like "no whitelist at all".
type whitelistState int

const (
	whitelistAbsent whitelistState = iota
	whitelistConfiguredEmpty
	whitelistWithRule
)

func (s whitelistState) String() string {
	switch s {
	case whitelistConfiguredEmpty:
		return "configured-empty"
	case whitelistWithRule:
		return "populated"
	default:
		return "absent"
	}
}

// TestApprovalPolicyMatrix walks every combination of the four inputs that
// decide() consults, so no single branch can be changed without a named case
// moving. The combination that regressed in review — auto-approve with a
// non-empty whitelist the command does not match, and no way to prompt — is
// "unmatched whitelist beats auto-approve when nobody can be asked" below.
func TestApprovalPolicyMatrix(t *testing.T) {
	const listed = "ls"
	const unlisted = "curl"

	for _, autoApprove := range []bool{false, true} {
		for _, canPrompt := range []bool{false, true} {
			for _, whitelist := range []whitelistState{whitelistAbsent, whitelistConfiguredEmpty, whitelistWithRule} {
				for _, command := range []string{listed, unlisted} {
					policy := approvalPolicy{
						autoApprove: autoApprove,
						canPrompt:   canPrompt,
						// nonInteractive only selects wording, so leaving it
						// false keeps this table about the decision itself.
					}
					switch whitelist {
					case whitelistConfiguredEmpty:
						policy.whitelistConfigured = true
					case whitelistWithRule:
						policy.whitelistConfigured = true
						policy.whitelist = []commandRule{rule(listed)}
					}

					want := expectedMatrixDecision(autoApprove, canPrompt, whitelist, command == listed)
					name := fmt.Sprintf("auto=%v/prompt=%v/whitelist=%v/cmd=%s",
						autoApprove, canPrompt, whitelist, command)

					t.Run(name, func(t *testing.T) {
						got := policy.decide(UniversalCommandArgs{Command: []string{command}})
						if got != want {
							t.Fatalf("decide() = %v, want %v", got, want)
						}
					})
				}
			}
		}
	}
}

// expectedMatrixDecision restates the policy independently of the code under
// test. A table of literals would drift into agreeing with whatever decide()
// happens to do; this restates the intent instead.
func expectedMatrixDecision(autoApprove, canPrompt bool, whitelist whitelistState, commandListed bool) approvalDecision {
	if whitelist == whitelistWithRule && commandListed {
		return decisionAllow
	}
	// Configuring a whitelist says unlisted commands need review, whether or
	// not the file turned out to list anything. With review unreachable the
	// answer is no, and auto-approve does not override it.
	if whitelist != whitelistAbsent && !canPrompt {
		return decisionDenyNotWhitelisted
	}
	if autoApprove {
		return decisionAllow
	}
	if !canPrompt {
		return decisionDenyNonInteractive
	}
	return decisionRequireApproval
}

// The regression the deleted combinatorial test had covered, stated once more
// on its own so a failure names it directly: piped stdin with a whitelist and
// -tool-auto-approve must not run an unlisted command.
func TestWhitelistWithAutoApproveStillDeniesUnlistedWithoutTerminal(t *testing.T) {
	policy := approvalPolicy{
		whitelist:   []commandRule{rule("ls")},
		autoApprove: true,
		canPrompt:   false,
	}
	got := policy.decide(UniversalCommandArgs{Command: []string{"curl", "-sS", "https://example.com"}})
	if got != decisionDenyNotWhitelisted {
		t.Fatalf("decide() = %v, want %v", got, decisionDenyNotWhitelisted)
	}
}

// A whitelist file that yields no rules is the strictest configuration an
// operator can write, and it used to be the weakest thing in the process:
// loadCommandRules returns (nil, nil) for a file that is empty, truncated, or
// all comments, so the rule-count test in decide() skipped step 3 and
// -tool-auto-approve ran the command. Driven through NewExecutor and
// ExecuteParallel because the defect was in how the policy was built, not only
// in how it decides — and once per route to canPrompt=false, because a missing
// approver and -tool-non-interactive close the prompt gate in different places.
func TestConfiguredWhitelistWithNoRulesDeniesUnlistedCommands(t *testing.T) {
	routes := []struct {
		name           string
		nonInteractive bool
		approver       Approver
	}{
		// -tool-auto-approve with no approver: exactly the scripted run the
		// deny-by-default rule exists for.
		{name: "no approver"},
		// -tool-non-interactive with an approver that would say yes: the flag
		// must keep the gate closed before the approver is ever asked.
		{name: "non-interactive flag with an approver", nonInteractive: true, approver: NewTestApprover(true)},
	}

	for _, tt := range []struct {
		name     string
		contents string
	}{
		{name: "empty file", contents: ""},
		{name: "comments and blank lines", contents: "# nothing enabled yet\n\n   \n# TODO: add rules\n"},
	} {
		for _, route := range routes {
			t.Run(tt.name+"/"+route.name, func(t *testing.T) {
				root := t.TempDir()
				whitelistPath := filepath.Join(root, "whitelist.txt")
				if err := os.WriteFile(whitelistPath, []byte(tt.contents), 0o600); err != nil {
					t.Fatal(err)
				}
				sentinel := filepath.Join(root, "UNLISTED-COMMAND-RAN")

				executor, err := NewExecutor(RequestOptions{
					ToolWhitelist:      whitelistPath,
					ToolAutoApprove:    true,
					ToolNonInteractive: route.nonInteractive,
					ToolTimeout:        5 * time.Second,
				}, NewTestLogger(false), route.approver)
				if err != nil {
					t.Fatalf("NewExecutor() error = %v", err)
				}
				if len(executor.policy.whitelist) != 0 {
					t.Fatalf("whitelist rules = %d, want the file to parse to none", len(executor.policy.whitelist))
				}
				if !executor.policy.hasWhitelist() {
					t.Fatal("a configured whitelist file did not register as a whitelist")
				}

				args := UniversalCommandArgs{
					Command: []string{"/bin/sh", "-c", "printf ran > " + sentinel},
					Timeout: 5,
				}
				result := executor.ExecuteParallel(context.Background(), []ToolCall{{
					ID:   "unlisted",
					Name: UniversalCommandToolName,
					Args: marshalCommandArgs(t, args),
				}}, nil)[0]

				if result.Code != errors.ErrCodeDeniedNotWhitelisted {
					t.Fatalf("result = %#v, want %s", result, errors.ErrCodeDeniedNotWhitelisted)
				}
				if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
					t.Fatalf("the unlisted command ran (stat sentinel err = %v)", err)
				}
			})
		}
	}
}

// NewExecutor is where canPrompt is derived, and a nil approver has to close
// the gate as firmly as the flag does.
func TestNewExecutorDerivesCanPromptFromApproverAndFlag(t *testing.T) {
	approver := &recordingLifecycleApprover{recorder: &lifecycleRecorder{}, approve: true}

	tests := []struct {
		name           string
		approver       Approver
		nonInteractive bool
		want           bool
	}{
		{name: "approver and no flag", approver: approver, want: true},
		{name: "approver but flag set", approver: approver, nonInteractive: true},
		{name: "no approver", approver: nil},
		{name: "no approver and flag set", nonInteractive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor, err := NewExecutor(
				RequestOptions{ToolNonInteractive: tt.nonInteractive},
				NewTestLogger(false),
				tt.approver,
			)
			if err != nil {
				t.Fatalf("NewExecutor() error = %v", err)
			}
			if executor.policy.canPrompt != tt.want {
				t.Fatalf("canPrompt = %v, want %v", executor.policy.canPrompt, tt.want)
			}
		})
	}
}
