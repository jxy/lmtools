// executor_rules.go holds the rule language: the on-disk whitelist/blacklist
// wire format for commands and for view_image, its two match semantics
// (grants match exactly, denials match broadly), and the suggested-rule
// renderers that denials print.

package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
//
// A rule that names a tool is an image rule: it matches view_image calls and
// no command, and its one channel is the path. The path is authority in the
// sense stdin is — it chooses whose bytes leave the machine — and a call
// always carries one, so a grant that names a path admits the calls inside
// it and a grant that names none admits every image. Detail is a rendering
// hint and, like timeout, takes no part.
type commandRule struct {
	// tool is set on an image rule and empty on a command rule. matches
	// refuses the one and matchesImage refuses the other, so the two halves
	// of one file cannot grant each other's calls.
	tool string
	// path is the file or directory an image rule names; nil names every
	// image.
	path *string
	// mcp is set on an MCP rule: the server it names, with tool holding the
	// server's tool name or empty for every tool of that server. matches
	// and matchesImage refuse such a rule and matchesMCP refuses every
	// other.
	mcp     string
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
	if r.tool != "" || r.mcp != "" || !commandHasPrefix(args.Command, r.command) {
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

// matchesImage is the image half of matching. Both halves of a rule file use
// the one containment test, because path is a channel every call carries: a
// grant naming a directory admits what is inside it, a denial naming a
// directory refuses what is inside it, and neither can be widened or escaped
// by a field the call adds, since the call has no other.
func (r commandRule) matchesImage(args ViewImageArgs) bool {
	if r.mcp != "" || r.tool != ViewImageToolName {
		return false
	}
	return r.path == nil || imagePathWithin(*r.path, args.Path)
}

// matchesMCP is the MCP half of matching: the rule names a server, and a
// tool or every tool of it. Both halves of a rule file read it the same
// way, because a call carries no channel a rule could be widened or
// escaped through; the arguments are the model's and no rule names them.
func (r commandRule) matchesMCP(server, tool string) bool {
	if r.mcp == "" || r.mcp != server {
		return false
	}
	return r.tool == "" || r.tool == tool
}

// imagePathWithin reports whether call names rule itself or a path inside the
// directory rule names. Both are resolved against the process working
// directory and cleaned, which is how the call's path will be opened, so
// "plots/../secret.png" is judged by where it lands and not by the prefix it
// spells. Command rules compare their paths as written because a command's
// paths resolve against a workdir the rule names too; an image call has no
// workdir, and a prefix compared as written is a grant to walk out of.
// Symbolic links along the way are not resolved, which is as far as the
// redirection checks go as well.
func imagePathWithin(rulePath, callPath string) bool {
	rule, err := filepath.Abs(rulePath)
	if err != nil {
		return false
	}
	call, err := filepath.Abs(callPath)
	if err != nil {
		return false
	}
	if call == rule {
		return true
	}
	if !strings.HasSuffix(rule, string(filepath.Separator)) {
		rule += string(filepath.Separator)
	}
	return strings.HasPrefix(call, rule)
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
	// MCP names the server of an MCP rule and leads so a suggested MCP rule
	// reads mcp first. Tool and Path are the image rule, and Tool doubles
	// as the tool of an MCP rule; they lead the command fields so a
	// suggested image rule reads tool first, the way a suggested command
	// rule reads command first.
	MCP        string            `json:"mcp,omitempty"`
	Tool       string            `json:"tool,omitempty"`
	Path       *string           `json:"path,omitempty"`
	Command    []string          `json:"command,omitempty"`
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
	if object.MCP != "" {
		return parseMCPRule(object, arrayRuleMode)
	}
	if object.Tool != "" {
		return parseImageRule(object, arrayRuleMode)
	}
	if object.Path != nil {
		return commandRule{}, fmt.Errorf(`command object field "path" belongs to a rule that names "tool"`)
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

// parseImageRule reads an object rule that names a tool. Only view_image takes
// one, and it takes path alone: every command channel is refused so a rule
// cannot be read two ways, and an empty path is refused the way an empty file
// field is. There is no array form, because an array is an argv prefix.
func parseImageRule(object commandRuleJSON, arrayRuleMode ruleMatchMode) (commandRule, error) {
	if object.Tool != ViewImageToolName {
		return commandRule{}, fmt.Errorf("unknown tool %q: only %s takes a tool rule", object.Tool, ViewImageToolName)
	}
	if len(object.Command) > 0 || object.Environ != nil || object.Workdir != nil || object.Stdin != nil ||
		object.StdinFile != nil || object.StdoutFile != nil || object.StderrFile != nil {
		return commandRule{}, fmt.Errorf("a %s rule accepts tool and path alone", ViewImageToolName)
	}
	if object.Path != nil && *object.Path == "" {
		return commandRule{}, fmt.Errorf(`command object field "path" cannot be empty`)
	}
	return commandRule{
		tool:      ViewImageToolName,
		path:      object.Path,
		matchMode: objectRuleMode(arrayRuleMode),
	}, nil
}

// suggestedImageRuleJSON renders the narrowest whitelist rule that would admit
// this call: the file itself, as the model wrote it. Widening it to the
// directory is the operator's decision to make by hand.
func suggestedImageRuleJSON(args ViewImageArgs) string {
	path := args.Path
	return MarshalJSONForDisplay(commandRuleJSON{Tool: ViewImageToolName, Path: &path})
}

// parseMCPRule reads an object rule that names an MCP server. It takes the
// server and, optionally, one of its tools, and nothing else: every command
// channel and the image path are refused so a rule cannot be read two
// ways. There is no array form, because an array is an argv prefix.
func parseMCPRule(object commandRuleJSON, arrayRuleMode ruleMatchMode) (commandRule, error) {
	if len(object.Command) > 0 || object.Environ != nil || object.Workdir != nil || object.Stdin != nil ||
		object.StdinFile != nil || object.StdoutFile != nil || object.StderrFile != nil || object.Path != nil {
		return commandRule{}, fmt.Errorf("an mcp rule accepts mcp and tool alone")
	}
	if strings.TrimSpace(object.MCP) != object.MCP {
		return commandRule{}, fmt.Errorf(`command object field "mcp" has surrounding whitespace`)
	}
	return commandRule{
		mcp:       object.MCP,
		tool:      object.Tool,
		matchMode: objectRuleMode(arrayRuleMode),
	}, nil
}

// suggestedMCPRuleJSON renders the narrowest whitelist rule that would
// admit this call: the one tool of the one server. Widening it to the
// whole server is the operator's decision to make by hand.
func suggestedMCPRuleJSON(server, tool string) string {
	return MarshalJSONForDisplay(commandRuleJSON{MCP: server, Tool: tool})
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
