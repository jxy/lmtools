// executor_rules.go holds the command-rule language: the on-disk
// whitelist/blacklist wire format, its two match semantics (grants match
// exactly, denials match broadly), and the suggested-rule renderer that
// denials print.

package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ruleMatchMode is how one rule matches the channels a call can carry:
// environ, literal stdin, workdir, and the three file redirections.
type ruleMatchMode uint8

const (
	// matchBareCommandOnly is an array-form grant: the call must carry none of
	// the channels a rule has to name. It must stay first — it is the iota
	// zero value, which commandRule.matches reaches through its default case
	// and zero-valued commandRule literals rely on.
	matchBareCommandOnly ruleMatchMode = iota
	// matchExactChannels is an object-form grant: the channels must match the
	// call exactly.
	matchExactChannels
	// matchNamedChannelSubset is an object-form denial: the channels the rule
	// names must be in the call, which may carry more.
	matchNamedChannelSubset
	// matchAnyCall is an array-form denial: every call matches.
	matchAnyCall
)

// commandRule matches an argv prefix and, for object-form rules, the workdir,
// environ, literal-stdin, and file-redirection shape the rule names.
//
// A grant must not confer more authority than the shape it spells out, and a
// denial must not be escapable by adding a field to the call. That asymmetry is
// the whole point of the four match modes: array grants demand a bare call,
// object grants demand the exact shape, object denials match every call that
// carries the fields they name, and array denials match everything.
//
// Literal stdin is authority, not just data. A bare ["python3"] grant predates
// the stdin channel and means "run the interpreter", not "run a program the
// model writes"; matching it against a call that supplies stdin would turn
// every interpreter already sitting in an operator's whitelist into arbitrary
// code execution.
//
// So is environ, and for the same reason one step further out: it chooses the
// dynamic linker for every executable in the round (LD_PRELOAD, DYLD_*), the
// module path an interpreter imports from, and the proxy a network tool trusts.
// A grant that names a command does not name any of that, so it cannot be read
// as consent to it — ["sort"] means sort the input, not load a library the model
// picked. An object rule opts in by writing the environment down.
//
// Timeout is the one channel deliberately left out. It bounds how long the
// authority a rule already granted is exercised for, it cannot reach anything
// the rest of the call could not, and commandTimeout clamps it to
// MaxCommandTimeoutSeconds — so pinning it in a rule would make grants brittle
// against a number the model picks freely, and buy nothing.
type commandRule struct {
	command []string
	// nil means the rule does not name the field. For a grant that is a
	// requirement that the call not carry it; for a denial it is a wildcard.
	workdir *string
	// environ follows the same nil convention. A non-nil empty map is a rule
	// that names an empty environment, which for a denial is the wildcard again
	// (it demands nothing) and for a grant is what any rule already means.
	environ    map[string]string
	stdin      *bool
	stdinFile  *string
	stdoutFile *string
	stderrFile *string
	matchMode  ruleMatchMode
}

func (r commandRule) matches(args UniversalCommandArgs) bool {
	if !commandHasPrefix(args.Command, r.command) {
		return false
	}

	switch r.matchMode {
	case matchAnyCall:
		return true
	case matchNamedChannelSubset:
		return matchesNamedString(r.workdir, args.Workdir) &&
			matchesNamedEnviron(r.environ, args.Environ) &&
			matchesNamedStdin(r.stdin, args.Stdin) &&
			matchesNamedString(r.stdinFile, args.StdinFile) &&
			matchesNamedString(r.stdoutFile, args.StdoutFile) &&
			matchesNamedString(r.stderrFile, args.StderrFile)
	case matchExactChannels:
		return optionalString(r.workdir) == args.Workdir &&
			allowsEnviron(r.environ, args.Environ) &&
			allowsStdin(r.stdin, args.Stdin) &&
			optionalString(r.stdinFile) == args.StdinFile &&
			optionalString(r.stdoutFile) == args.StdoutFile &&
			optionalString(r.stderrFile) == args.StderrFile
	default:
		return args.isBareCommand()
	}
}

// isBareCommand reports whether the call carries none of the channels a rule
// has to name before a grant covers it: environ, literal stdin, and the three
// file redirections. An array rule grants a call only when this holds, and
// suggestedCommandRuleJSON uses the same answer to decide whether an array rule
// can express the call at all. Those two have to agree — a denial that printed
// an array rule for a call an array rule does not admit would be advice that
// leads nowhere — and the way to keep them agreeing while the channel list
// grows is for there to be one of them.
//
// Workdir and timeout are outside it, for different reasons. Timeout is not
// authority (see commandRule). Workdir is, but parseCommandRule requires an
// object rule to name one of the channels above, so a workdir-only rule cannot
// be written down; counting workdir here would leave a call that carries
// nothing else with no rule that admits it and no suggestion worth printing.
func (a UniversalCommandArgs) isBareCommand() bool {
	return len(a.Environ) == 0 &&
		a.Stdin == nil &&
		a.StdinFile == "" &&
		a.StdoutFile == "" &&
		a.StderrFile == ""
}

// matchesNamedString is the denial rule: a field the rule does not name places
// no constraint on the call.
func matchesNamedString(ruleValue *string, argValue string) bool {
	return ruleValue == nil || *ruleValue == argValue
}

func matchesNamedStdin(ruleValue *bool, argValue *string) bool {
	return ruleValue == nil || *ruleValue == (argValue != nil)
}

// matchesNamedEnviron is the denial rule for a map: every variable the rule
// wrote down has to be in the call, and the call may carry others. Demanding
// the whole environment be equal would be grant semantics on a denial list —
// {"command":["python3"],"environ":{"LD_PRELOAD":"/tmp/evil.so"}} would stop
// exactly that call and be escaped by adding one unrelated variable beside it.
func matchesNamedEnviron(ruleValue, argValue map[string]string) bool {
	for name, value := range ruleValue {
		if got, ok := argValue[name]; !ok || got != value {
			return false
		}
	}
	return true
}

// allowsEnviron is the grant rule: the call's environment additions must be
// exactly the ones the rule wrote down, so a grant confers no variable nobody
// reviewed. A rule that names no environ therefore admits only a call that adds
// none, and an empty map is no addition at all — {"environ":{}} in a call asks
// for nothing and is not a channel it carried.
func allowsEnviron(ruleValue, argValue map[string]string) bool {
	return len(ruleValue) == len(argValue) && matchesNamedEnviron(ruleValue, argValue)
}

// allowsStdin is the grant rule: literal stdin passes only when the rule opted
// in with "stdin": true.
func allowsStdin(ruleValue *bool, argValue *string) bool {
	if ruleValue != nil && *ruleValue {
		return true
	}
	return argValue == nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// loadCommandRules loads one JSON argv array or exact command-rule object per
// non-comment line. arrayRuleMode is the mode array-form entries match with:
// whitelists pass matchBareCommandOnly so pre-existing grants stay narrow,
// while blacklists pass matchAnyCall so pre-existing denials stay broad.
// Object-form entries derive their mode from it via objectRuleMode.
func loadCommandRules(path string, arrayRuleMode ruleMatchMode) ([]commandRule, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var rules []commandRule
	scanner := bufio.NewScanner(file)
	// Set buffer limits to prevent pathologically large lines
	// Initial buffer: 64KB, max line size: 1MB
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule, err := parseCommandRule(line, arrayRuleMode)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		rules = append(rules, rule)
	}

	return rules, scanner.Err()
}

// commandRuleJSON is the on-disk shape of an object-form rule, and the only
// place the wire format is spelled. The loader decodes it and
// suggestedCommandRuleJSON encodes it, so a channel that reaches this struct
// cannot be understood by one and dropped by the other: a suggested rule that
// omitted a field the call carried would not match the call it was generated
// from, since grants are exact.
//
// What that arrangement does not do on its own is notice a channel that never
// reaches the struct at all. environ was in UniversalCommandArgs and in neither
// half of the rule format, so DisallowUnknownFields refused to let an operator
// write it down while the matcher ignored it — the field was invisible in the
// same direction on both sides, which reads as agreement and is not.
// TestRuleFormatNamesEveryChannelACallCanCarry is the check that fails when the
// next field lands here, and TestSuggestedRuleMatchesTheCallItWasGeneratedFrom
// pins the property this comment claims.
type commandRuleJSON struct {
	Command    []string          `json:"command"`
	Environ    map[string]string `json:"environ,omitempty"`
	Workdir    *string           `json:"workdir,omitempty"`
	Stdin      *bool             `json:"stdin,omitempty"`
	StdinFile  *string           `json:"stdin_file,omitempty"`
	StdoutFile *string           `json:"stdout_file,omitempty"`
	StderrFile *string           `json:"stderr_file,omitempty"`
}

func parseCommandRule(line string, arrayRuleMode ruleMatchMode) (commandRule, error) {
	if strings.HasPrefix(line, "[") {
		var command []string
		if err := json.Unmarshal([]byte(line), &command); err != nil {
			return commandRule{}, fmt.Errorf("invalid JSON command array: %w", err)
		}
		if len(command) == 0 {
			return commandRule{}, fmt.Errorf("empty command array")
		}
		return commandRule{command: command, matchMode: arrayRuleMode}, nil
	}

	if !strings.HasPrefix(line, "{") {
		return commandRule{}, fmt.Errorf("rule must be a JSON command array or object")
	}

	var object commandRuleJSON
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&object); err != nil {
		return commandRule{}, fmt.Errorf("invalid JSON command object: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return commandRule{}, fmt.Errorf("command object must contain one JSON value")
		}
		return commandRule{}, fmt.Errorf("invalid JSON command object: %w", err)
	}
	if len(object.Command) == 0 {
		return commandRule{}, fmt.Errorf("command object has an empty command array")
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{name: "stdin_file", value: object.StdinFile},
		{name: "stdout_file", value: object.StdoutFile},
		{name: "stderr_file", value: object.StderrFile},
	} {
		if field.value != nil && *field.value == "" {
			return commandRule{}, fmt.Errorf("command object field %q cannot be empty", field.name)
		}
	}
	// An object rule has to narrow something an array rule cannot say, or it is
	// an array rule with extra syntax. environ counts: a call may carry it and
	// nothing else, and that call needs a rule that admits it.
	if object.Environ == nil && object.Stdin == nil && object.StdinFile == nil &&
		object.StdoutFile == nil && object.StderrFile == nil {
		return commandRule{}, fmt.Errorf("command object must include environ, stdin, stdin_file, stdout_file, or stderr_file")
	}

	return commandRule{
		command:    object.Command,
		workdir:    object.Workdir,
		environ:    object.Environ,
		stdin:      object.Stdin,
		stdinFile:  object.StdinFile,
		stdoutFile: object.StdoutFile,
		stderrFile: object.StderrFile,
		matchMode:  objectRuleMode(arrayRuleMode),
	}, nil
}

// objectRuleMode carries the list's breadth into object rules. A denial list
// asks for matchAnyCall, which is the right answer for an array entry that
// names no fields but far too coarse for an object that does;
// matchNamedChannelSubset is the object-form spelling of the same intent. A
// grant list stays matchExactChannels, because a grant that ignored an unnamed
// field would authorize a redirection nobody wrote down.
func objectRuleMode(arrayRuleMode ruleMatchMode) ruleMatchMode {
	if arrayRuleMode == matchAnyCall {
		return matchNamedChannelSubset
	}
	return matchExactChannels
}

// suggestedCommandRuleJSON renders the narrowest whitelist rule that would
// admit this exact call, so a denial can be pasted rather than translated. A
// call that carries none of the channels a rule has to name still gets the
// array form, because that is what most whitelists are made of — the same
// isBareCommand the array grant itself is matched on, so the rule printed here
// is a rule that admits the call printed above it. It renders through
// MarshalJSONForDisplay so `&&` in a denied command reads as written, exactly
// as the approval line above it did.
func suggestedCommandRuleJSON(args *UniversalCommandArgs) string {
	if args.isBareCommand() {
		return MarshalJSONForDisplay(args.Command)
	}

	// stdin is a boolean in a rule and a string in a call: the rule grants the
	// channel, it does not pin the bytes sent through it. environ is the
	// opposite — the variables are the authority, so the rule names them — and
	// only the fields the call actually carries are named, so the rule stays as
	// narrow as the call.
	object := commandRuleJSON{Command: args.Command}
	if len(args.Environ) > 0 {
		object.Environ = args.Environ
	}
	if args.Workdir != "" {
		object.Workdir = &args.Workdir
	}
	if args.Stdin != nil {
		stdin := true
		object.Stdin = &stdin
	}
	if args.StdinFile != "" {
		object.StdinFile = &args.StdinFile
	}
	if args.StdoutFile != "" {
		object.StdoutFile = &args.StdoutFile
	}
	if args.StderrFile != "" {
		object.StderrFile = &args.StderrFile
	}
	return MarshalJSONForDisplay(object)
}

// commandHasPrefix checks if cmd starts with all elements in pattern
func commandHasPrefix(cmd, pattern []string) bool {
	if len(pattern) == 0 {
		return false
	}

	if len(cmd) < len(pattern) {
		return false
	}

	for i := 0; i < len(pattern); i++ {
		if cmd[i] != pattern[i] {
			return false
		}
	}

	return true
}
