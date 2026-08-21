package core

import (
	"context"
	"lmtools/internal/errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// environ is authority: it chooses the dynamic linker, the interpreter's module
// path, and the proxy every network tool in the round will use. An array grant
// written for a command names none of that, so it cannot be read as consent to
// it — the same reasoning that already keeps literal stdin out of ["python3"].
func TestArrayWhitelistDoesNotGrantEnviron(t *testing.T) {
	grant, err := parseCommandRule(`["sort"]`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{whitelist: []commandRule{grant}, canPrompt: false}

	if got := policy.decide(UniversalCommandArgs{Command: []string{"sort"}}); got != decisionAllow {
		t.Fatalf("decide() without environ = %v, want allow", got)
	}
	injected := UniversalCommandArgs{
		Command: []string{"sort"},
		Environ: map[string]string{"LD_PRELOAD": "/tmp/evil.so"},
	}
	if got := policy.decide(injected); got != decisionDenyNotWhitelisted {
		t.Fatalf("decide() with environ = %v, want %v", got, decisionDenyNotWhitelisted)
	}
}

// An empty environ object adds no variable to the process, so it is not a
// channel the call carried. Reading the map's presence rather than its contents
// would deny a call that asks for nothing.
func TestEmptyEnvironIsNotACarriedChannel(t *testing.T) {
	grant, err := parseCommandRule(`["sort"]`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{whitelist: []commandRule{grant}, canPrompt: false}

	got := policy.decide(UniversalCommandArgs{
		Command: []string{"sort"},
		Environ: map[string]string{},
	})
	if got != decisionAllow {
		t.Fatalf("decide() with an empty environ = %v, want allow", got)
	}
}

// An operator who does want the call to set variables says so, and the grant
// covers that exact environment and no other.
func TestObjectRuleGrantsExactEnviron(t *testing.T) {
	grant, err := parseCommandRule(
		`{"command":["make"],"environ":{"CC":"clang","LC_ALL":"C"}}`,
		matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{whitelist: []commandRule{grant}, canPrompt: false}

	allowed := UniversalCommandArgs{
		Command: []string{"make", "build"},
		Environ: map[string]string{"CC": "clang", "LC_ALL": "C"},
	}
	if got := policy.decide(allowed); got != decisionAllow {
		t.Fatalf("decide() on the granted environment = %v, want allow", got)
	}

	for name, environ := range map[string]map[string]string{
		"different value":   {"CC": "/tmp/evil", "LC_ALL": "C"},
		"extra variable":    {"CC": "clang", "LC_ALL": "C", "LD_PRELOAD": "/tmp/evil.so"},
		"missing variable":  {"CC": "clang"},
		"no environ at all": nil,
	} {
		t.Run(name, func(t *testing.T) {
			got := policy.decide(UniversalCommandArgs{Command: []string{"make"}, Environ: environ})
			if got != decisionDenyNotWhitelisted {
				t.Fatalf("decide() = %v, want %v", got, decisionDenyNotWhitelisted)
			}
		})
	}
}

// A grant is exact in both directions: naming environ does not also authorize a
// redirection the rule never wrote down.
func TestEnvironGrantDoesNotWidenTheOtherChannels(t *testing.T) {
	grant, err := parseCommandRule(`{"command":["make"],"environ":{"CC":"clang"}}`, matchBareCommandOnly)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{whitelist: []commandRule{grant}, canPrompt: false}

	got := policy.decide(UniversalCommandArgs{
		Command:    []string{"make"},
		Environ:    map[string]string{"CC": "clang"},
		StdoutFile: "build.log",
	})
	if got != decisionDenyNotWhitelisted {
		t.Fatalf("decide() with an unnamed redirection = %v, want %v", got, decisionDenyNotWhitelisted)
	}
}

// A denial must not be escapable by adding a variable it never mentioned, the
// way the file fields already are not. Requiring the whole map to be equal
// would have made a LD_PRELOAD denial evadable with one unrelated variable.
func TestObjectBlacklistRuleDeniesEnvironSupersets(t *testing.T) {
	denial, err := parseCommandRule(
		`{"command":["python3"],"environ":{"LD_PRELOAD":"/tmp/evil.so"}}`,
		matchAnyCall)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{blacklist: []commandRule{denial}, autoApprove: true}

	denied := []UniversalCommandArgs{
		{Command: []string{"python3"}, Environ: map[string]string{"LD_PRELOAD": "/tmp/evil.so"}},
		{Command: []string{"python3"}, Environ: map[string]string{"LD_PRELOAD": "/tmp/evil.so", "PATH": "/tmp"}},
		{
			Command:    []string{"python3", "-c", "print(1)"},
			Environ:    map[string]string{"LD_PRELOAD": "/tmp/evil.so"},
			Workdir:    "/tmp",
			StdoutFile: "out.txt",
		},
	}
	for i, args := range denied {
		if got := policy.decide(args); got != decisionDenyBlacklist {
			t.Fatalf("denied[%d] decision = %v, want blacklist denial", i, got)
		}
	}

	// The denial narrows on what it names; a different library is a different
	// call and is not what this rule denies.
	allowed := []UniversalCommandArgs{
		{Command: []string{"python3"}, Environ: map[string]string{"LD_PRELOAD": "/usr/lib/ok.so"}},
		{Command: []string{"python3"}, Environ: map[string]string{"PATH": "/tmp"}},
		{Command: []string{"python3"}},
	}
	for i, args := range allowed {
		if got := policy.decide(args); got != decisionAllow {
			t.Fatalf("allowed[%d] decision = %v, want allow", i, got)
		}
	}
}

// An array denial still covers every shape of its prefix, environ included.
func TestArrayBlacklistRuleDeniesEnvironBearingCalls(t *testing.T) {
	denial, err := parseCommandRule(`["python3"]`, matchAnyCall)
	if err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	policy := approvalPolicy{blacklist: []commandRule{denial}, autoApprove: true}

	got := policy.decide(UniversalCommandArgs{
		Command: []string{"python3"},
		Environ: map[string]string{"PYTHONPATH": "/tmp"},
	})
	if got != decisionDenyBlacklist {
		t.Fatalf("decide() = %v, want blacklist denial", got)
	}
}

// environ alone has to be enough to write a rule, or a call carrying only
// environ has no rule that admits it and the suggested remediation would not
// load. workdir alone still is not, which is the constraint that keeps it out
// of the bare-call predicate.
func TestObjectRuleMayNameOnlyEnviron(t *testing.T) {
	if _, err := parseCommandRule(`{"command":["make"],"environ":{"CC":"clang"}}`, matchBareCommandOnly); err != nil {
		t.Fatalf("parse rule: %v", err)
	}
	_, err := parseCommandRule(`{"command":["make"],"workdir":"/tmp"}`, matchBareCommandOnly)
	if err == nil {
		t.Fatal("a rule naming only command and workdir was accepted, want the at-least-one-of rejection")
	}
	if !strings.Contains(err.Error(), "environ") {
		t.Fatalf("rejection = %q, want it to offer environ as one of the fields that qualify", err)
	}
}

// The property the paste-ready denial text depends on: the rule a denial prints
// must load, and must admit the exact call it was printed for. A channel the
// suggestion drops makes the advice a dead end — that is what environ was.
func TestSuggestedRuleMatchesTheCallItWasGeneratedFrom(t *testing.T) {
	stdin := "payload"
	calls := map[string]UniversalCommandArgs{
		"bare":                {Command: []string{"ls"}},
		"workdir only":        {Command: []string{"ls"}, Workdir: "/tmp"},
		"timeout only":        {Command: []string{"ls"}, Timeout: 30},
		"environ only":        {Command: []string{"make"}, Environ: map[string]string{"CC": "clang"}},
		"environ and workdir": {Command: []string{"make"}, Environ: map[string]string{"CC": "clang"}, Workdir: "/tmp"},
		"environ and stdin":   {Command: []string{"python3"}, Environ: map[string]string{"PYTHONPATH": "/tmp"}, Stdin: &stdin},
		"environ and stdout":  {Command: []string{"sort"}, Environ: map[string]string{"LC_ALL": "C"}, StdoutFile: "out.txt"},
		"stdin file only":     {Command: []string{"sort"}, StdinFile: "in.txt"},
		"empty environ":       {Command: []string{"ls"}, Environ: map[string]string{}},
		// stdin and stdin_file are mutually exclusive in a call, so "every
		// channel" carries the literal one.
		"every channel": {
			Command:    []string{"sort", "-u"},
			Environ:    map[string]string{"LC_ALL": "C", "TMPDIR": "/tmp"},
			Workdir:    "/tmp/job",
			Stdin:      &stdin,
			StdoutFile: "out.txt",
			StderrFile: "err.txt",
			Timeout:    30,
		},
	}

	for name, args := range calls {
		args := args
		t.Run(name, func(t *testing.T) {
			suggestion := suggestedCommandRuleJSON(&args)
			rule, err := parseCommandRule(suggestion, matchBareCommandOnly)
			if err != nil {
				t.Fatalf("suggested rule %s does not load: %v", suggestion, err)
			}
			if !rule.matches(args) {
				t.Fatalf("suggested rule %s does not match the call it was generated from (%#v)", suggestion, args)
			}
			policy := approvalPolicy{whitelist: []commandRule{rule}, canPrompt: false}
			if got := policy.decide(args); got != decisionAllow {
				t.Fatalf("a policy holding only %s decided %v for the call it was suggested for, want allow",
					suggestion, got)
			}

			// And no wider than that call. A suggestion is written down as a
			// standing grant, so a channel it silently tolerates is authority
			// the operator never agreed to.
			wider := args
			wider.Environ = map[string]string{"LD_PRELOAD": "/tmp/evil.so"}
			for name, value := range args.Environ {
				wider.Environ[name] = value
			}
			if rule.matches(wider) {
				t.Fatalf("suggested rule %s also grants the same call with LD_PRELOAD added", suggestion)
			}
		})
	}
}

// The suggestion is the operator-facing half of the tightening, so it has to
// name environ literally rather than approximate it.
func TestSuggestedRuleNamesEnviron(t *testing.T) {
	suggestion := suggestedCommandRuleJSON(&UniversalCommandArgs{
		Command: []string{"make"},
		Environ: map[string]string{"CC": "clang"},
	})
	if want := `{"command":["make"],"environ":{"CC":"clang"}}`; suggestion != want {
		t.Fatalf("suggested rule = %s, want %s", suggestion, want)
	}
}

// End to end through the real policy: a grant written for a redirected command
// used to admit the same call with an attacker-chosen environment, and the
// injected variable reached the process. The denial that replaces it prints a
// rule that does admit the call, which is the only way the operator gets from
// one to the other.
func TestWhitelistedRedirectionDoesNotSmuggleEnviron(t *testing.T) {
	const injected = "INJECTED_BY_THE_MODEL"

	// environSmugglingCall is one call in two spellings; workdir is rebuilt per
	// subtest so the rule and the call agree on it, which an exact grant needs.
	environSmugglingCall := func(root string) UniversalCommandArgs {
		return UniversalCommandArgs{
			Command:    []string{"/bin/sh", "-c", `printf %s "$SMUGGLED"`},
			Environ:    map[string]string{"SMUGGLED": injected},
			Workdir:    root,
			StdoutFile: "out.txt",
			Timeout:    5,
		}
	}

	run := func(t *testing.T, root, rule string, call UniversalCommandArgs) (ToolResult, string) {
		t.Helper()
		whitelistPath := filepath.Join(root, "whitelist.txt")
		if err := os.WriteFile(whitelistPath, []byte(rule+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		executor, err := NewExecutor(RequestOptions{
			ToolTimeout:        5 * time.Second,
			ToolWhitelist:      whitelistPath,
			ToolNonInteractive: true,
			ToolAutoApprove:    true,
		}, NewTestLogger(false), nil)
		if err != nil {
			t.Fatalf("NewExecutor() error = %v", err)
		}

		result := executor.ExecuteParallel(context.Background(), []ToolCall{{
			ID:   "environ",
			Name: UniversalCommandToolName,
			Args: marshalCommandArgs(t, call),
		}}, nil)[0]

		written, err := os.ReadFile(filepath.Join(root, "out.txt"))
		if err != nil {
			return result, ""
		}
		return result, string(written)
	}

	t.Run("grant that names no environ", func(t *testing.T) {
		root := t.TempDir()
		call := environSmugglingCall(root)
		// The rule an operator would have written before environ existed:
		// exactly this call, minus the environment.
		bare := call
		bare.Environ = nil

		result, written := run(t, root, suggestedCommandRuleJSON(&bare), call)
		if written == injected {
			t.Fatalf("the injected environment reached the process: out.txt = %q", written)
		}
		if written != "" {
			t.Fatalf("out.txt = %q, want the denied command to have written nothing", written)
		}
		if result.Code != errors.ErrCodeDeniedNotWhitelisted {
			t.Fatalf("code = %q, want %s (result %#v)", result.Code, errors.ErrCodeDeniedNotWhitelisted, result)
		}
	})

	t.Run("grant printed by the denial", func(t *testing.T) {
		// suggestedCommandRuleJSON is what the denial above tells the operator
		// to paste, so it has to turn that denial into an allow.
		root := t.TempDir()
		call := environSmugglingCall(root)

		result, written := run(t, root, suggestedCommandRuleJSON(&call), call)
		if result.Error != "" {
			t.Fatalf("result = %#v, want the granted call to run", result)
		}
		if written != injected {
			t.Fatalf("out.txt = %q, want %q", written, injected)
		}
	})
}

// The loader and the suggestion share commandRuleJSON so a channel cannot be
// understood by one and dropped by the other. That only holds while every
// channel a call can carry appears in the struct, which is the thing that
// broke: environ was in UniversalCommandArgs and in neither half of the rule
// format, so no rule could name it and every rule already written covered it.
func TestRuleFormatNamesEveryChannelACallCanCarry(t *testing.T) {
	ruleFields := jsonFieldNames(t, commandRuleJSON{})
	callFields := jsonFieldNames(t, UniversalCommandArgs{})

	for _, field := range callFields {
		// timeout is the deliberate exception; see commandRule.
		if field == "timeout" {
			continue
		}
		if !slices.Contains(ruleFields, field) {
			t.Errorf("universal_command accepts %q but no rule can name it, so every grant already written down covers it silently",
				field)
		}
	}
}

func jsonFieldNames(t *testing.T, value interface{}) []string {
	t.Helper()
	typ := reflect.TypeOf(value)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%T is not a struct", value)
	}
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names
}
