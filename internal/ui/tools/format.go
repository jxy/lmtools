package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/termwidth"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DetailIndent is the column of everything under a header: details,
	// notes, quoted content, and the approval answer cmd/lmc reads beneath a
	// question.
	DetailIndent = "  "
	// QuotePrefix marks every line of content quoted under a label: command
	// output, and argument values that span lines. A line of output can then
	// never be mistaken for a line of the transcript around it.
	QuotePrefix = "| "
	// ContinuationPrefix starts each row after the first of a line the
	// transcript wrapped, so the row stays in its block and reads as the line
	// going on.
	ContinuationPrefix = DetailIndent + ": "

	toolOutputChunkBytes = 32 * 1024
	// tabStop spaces tabs in quoted content from the content's first column,
	// which keeps a table aligned under QuotePrefix.
	tabStop = 8
	// minWrapCells is the least a continuation row must hold past its prefix
	// for wrapping to help; a narrower terminal gets lines whole.
	minWrapCells = 8

	shortOutputHeadLines = 10
	shortOutputTailLines = 10
	shortOutputLineBytes = 2000
)

// Options adjust a CLIToolUI for a live terminal. The zero value renders
// everything in full, which is what a saved conversation and a log want.
type Options struct {
	// ShortOutput shows output longer than about twenty lines as its first
	// and last lines, with a count of the lines between and MoreHint saying
	// where to read them.
	ShortOutput bool
	MoreHint    string
	// ShowPrograms names the program a bare command name resolves to when it
	// lies outside the standard system directories, where it may surprise.
	ShowPrograms bool
	// Columns, when set, is the terminal's width, within which the transcript
	// wraps its lines; without it lines are written whole.
	Columns func() int
}

// transcript writes the tool transcript a line at a time through a notifier,
// wrapping each line within the terminal's width when it knows one.
type transcript struct {
	notifier core.Notifier
	columns  func() int
}

func newTranscript(notifier core.Notifier, options Options) transcript {
	return transcript{notifier: notifier, columns: options.Columns}
}

func (ui *CLIToolUI) transcript() transcript {
	return newTranscript(ui.notifier, ui.Options)
}

// line writes text, which holds no newline, as one transcript line.
func (t transcript) line(text string) {
	t.notifier.Promptf("%s\n", t.wrap(expandTabs(text)))
}

func (t transcript) blank() {
	t.notifier.Promptf("\n")
}

// lines writes each line of text, the first after firstPrefix and the rest
// after continuationPrefix, with hidden characters spelled out: errors quote
// what the model asked for. Blank lines and text that looks like another
// speaker's label stay under the result they belong to.
func (t transcript) lines(text, firstPrefix, continuationPrefix string) {
	prefix := firstPrefix
	for {
		line, rest, more := strings.Cut(text, "\n")
		t.line(prefix + core.VisibleText(line))
		if !more {
			return
		}
		text = rest
		prefix = continuationPrefix
	}
}

// quoted writes lines under the detail column after QuotePrefix, each as show
// renders it, in writes of a bounded size: Promptf formats each call into a
// buffer of its own, so one call holding a whole large output would duplicate
// it. A positive lineBytes cuts longer lines and says how much was left out;
// it returns how many lines it cut.
func (t transcript) quoted(lines []string, lineBytes int, show func(string) string) (cutLines int) {
	var chunk strings.Builder
	flush := func() {
		if chunk.Len() > 0 {
			t.notifier.Promptf("%s", chunk.String())
			chunk.Reset()
		}
	}
	for _, line := range lines {
		cut := 0
		if lineBytes > 0 && len(line) > lineBytes {
			end := lineBytes
			for end > 0 && !utf8.RuneStart(line[end]) {
				end--
			}
			cut, line = len(line)-end, line[:end]
			cutLines++
		}
		if line == "" && cut == 0 {
			chunk.WriteString(DetailIndent + strings.TrimRight(QuotePrefix, " ") + "\n")
		} else {
			text := DetailIndent + QuotePrefix + show(line)
			if cut > 0 {
				text += fmt.Sprintf(" [%d more bytes]", cut)
			}
			chunk.WriteString(t.wrap(text))
			chunk.WriteByte('\n')
		}
		if chunk.Len() >= toolOutputChunkBytes {
			flush()
		}
	}
	flush()
	return cutLines
}

func (t transcript) wrap(text string) string {
	return Wrap(text, t.columns)
}

// Wrap breaks each line of text, which holds no tab, into rows of at most
// the terminal's width less one cell, the last column left empty so a full
// row never leaves the cursor waiting to wrap. Rows after a line's first
// start with ContinuationPrefix. Without a width, or with one too narrow to
// help, text comes back whole.
func Wrap(text string, columns func() int) string {
	if columns == nil {
		return text
	}
	width := columns() - 1
	// A rune takes no more cells than bytes, so a short text fits as it is.
	if len(text) <= width || width < len(ContinuationPrefix)+minWrapCells {
		return text
	}
	var b strings.Builder
	used, start := 0, 0
	for _, r := range text {
		if r == '\n' {
			b.WriteRune(r)
			used, start = 0, 0
			continue
		}
		cells := termwidth.RuneWidth(r)
		if used+cells > width && used > start {
			b.WriteString("\n" + ContinuationPrefix)
			used, start = len(ContinuationPrefix), len(ContinuationPrefix)
		}
		b.WriteRune(r)
		used += cells
	}
	return b.String()
}

// outputText renders a line of tool output: hidden characters spelled out,
// and tabs expanded from the line's start so a table keeps its alignment.
func outputText(line string) string {
	return expandTabs(core.VisibleText(line))
}

// valueText renders a line of a value under review with every byte visible:
// a tab is spelled \u0009 like other control characters, so a script
// indented with tabs cannot look like one indented with spaces.
func valueText(line string) string {
	return strings.ReplaceAll(core.VisibleText(line), "\t", core.EscapeRune('\t'))
}

// expandTabs turns each tab of text into the spaces that reach the next
// multiple of tabStop, counted from the start of text.
func expandTabs(text string) string {
	if !strings.Contains(text, "\t") {
		return text
	}
	var b strings.Builder
	column := 0
	for _, r := range text {
		if r == '\t' {
			n := tabStop - column%tabStop
			b.WriteString(strings.Repeat(" ", n))
			column += n
			continue
		}
		b.WriteRune(r)
		column += termwidth.RuneWidth(r)
	}
	return b.String()
}

// CLIToolUI displays command review, execution, and result batches. Options
// may change between rounds, as the REPL's /tool-output does.
type CLIToolUI struct {
	notifier core.Notifier
	Options  Options
	// streamed records that this round's results began through ResultReady,
	// so AfterExecute only closes the list.
	streamed bool
}

// NewCLIToolUI creates a CLI renderer for tool execution batches.
func NewCLIToolUI(notifier core.Notifier) *CLIToolUI {
	return &CLIToolUI{notifier: notifier}
}

// NewCLIToolUIWithOptions creates a renderer for a live terminal.
func NewCLIToolUIWithOptions(notifier core.Notifier, options Options) *CLIToolUI {
	return &CLIToolUI{notifier: notifier, Options: options}
}

// ShowCall displays one call. The executor shows every call of a round before
// it asks about any of them. args is the executor's already-parsed view of
// the call; calls it never parsed, such as one past the per-round cap, arrive
// nil and are decoded here for display.
func (ui *CLIToolUI) ShowCall(index, total int, call core.ToolCall, args *core.UniversalCommandArgs) {
	t := ui.transcript()
	t.blank()
	if index == 0 {
		t.line(fmt.Sprintf("Tools: %d", total))
	}
	ui.showCallLines(fmt.Sprintf("[%d/%d]", index+1, total), call, args)
}

// ShowRerun displays a call an earlier run may already have executed, and
// why its outcome is unknown, immediately before the question whether to run
// it again. It renders through the notifier the approval question uses, the
// way ShowCall does for a new call.
func (ui *CLIToolUI) ShowRerun(call core.ToolCall, reason string) {
	t := ui.transcript()
	t.blank()
	t.line("Outcome unknown: " + reason)
	ui.showCallLines("[rerun]", call, nil)
}

// showCallLines renders one call under prefix: its command and the fields
// that shape it, the image it would read, or its tool and arguments.
func (ui *CLIToolUI) showCallLines(prefix string, call core.ToolCall, args *core.UniversalCommandArgs) {
	t := ui.transcript()
	if args == nil {
		args = commandCallArgs(call)
	}

	if args != nil && len(args.Command) > 0 {
		ui.showCommand(prefix, args.Command)
		if args.Workdir != "" {
			t.line(DetailIndent + "Workdir: " + core.MarshalJSONForDisplay(args.Workdir))
		}
		if len(args.Environ) > 0 {
			ui.showJSON(DetailIndent+"Environ", core.MarshalJSONForDisplay(args.Environ))
		}
		if args.Stdin != nil {
			ui.showValue("Stdin", *args.Stdin)
		}
		if args.StdinFile != "" {
			t.line(DetailIndent + "Stdin file: " + core.MarshalJSONForDisplay(args.StdinFile))
		}
		if args.StdoutFile != "" {
			t.line(DetailIndent + "Stdout file: " + core.MarshalJSONForDisplay(args.StdoutFile))
		}
		if args.StderrFile != "" {
			t.line(DetailIndent + "Stderr file: " + core.MarshalJSONForDisplay(args.StderrFile))
		}
		if args.Timeout > 0 {
			t.line(fmt.Sprintf("%sTimeout: %ds", DetailIndent, args.Timeout))
		}
		return
	}

	if image := imageCallArgs(call); image != nil {
		t.line(prefix + " View image: " + core.MarshalJSONForDisplay(image.Path))
		if image.Detail != "" {
			t.line(DetailIndent + "Detail: " + core.VisibleText(image.Detail))
		}
		return
	}

	if call.MCPServer != "" {
		// The executor labelled the call with the server and the server's
		// tool name; the qualified name the model used is what a rule
		// would not name, so the line reads the way a rule does.
		t.line(prefix + " MCP tool: " + core.ToolCallDisplayName(call, nil))
		ui.showJSON(DetailIndent+"Arguments", compactToolArguments(call.Args))
		return
	}

	t.line(prefix + " Tool: " + core.DisplayName(call.Name))
	ui.showJSON(DetailIndent+"Arguments", compactToolArguments(call.Args))
}

// showCommand renders an argument vector. An argument that spans lines, a
// script handed to an interpreter most often, would read as one escaped JSON
// string wrapped by the terminal inside words; it is replaced in the vector by
// <arg N> and shown below as quoted lines, which is still every byte of it.
func (ui *CLIToolUI) showCommand(prefix string, argv []string) {
	multiline := false
	for _, arg := range argv {
		multiline = multiline || strings.Contains(arg, "\n")
	}
	if !multiline {
		ui.showJSON(prefix+" Command", core.MarshalJSONForDisplay(argv))
		ui.showProgram(argv[0])
		return
	}
	parts := make([]string, len(argv))
	for i, arg := range argv {
		if strings.Contains(arg, "\n") {
			parts[i] = fmt.Sprintf("<arg %d>", i+1)
		} else {
			parts[i] = core.MarshalJSONForDisplay(arg)
		}
	}
	ui.transcript().line(prefix + " Command: [" + strings.Join(parts, ",") + "]")
	ui.showProgram(argv[0])
	for i, arg := range argv {
		if strings.Contains(arg, "\n") {
			ui.showValue(fmt.Sprintf("Arg %d", i+1), arg)
		}
	}
}

// showProgram names the program behind the command line, when it may
// surprise.
func (ui *CLIToolUI) showProgram(name string) {
	if !ui.Options.ShowPrograms {
		return
	}
	if program := unusualProgram(name); program != "" {
		ui.transcript().line(DetailIndent + "Program: " + core.VisibleText(program))
	}
}

// ShowCallNote prints a note under a call's review, at the detail column.
func (ui *CLIToolUI) ShowCallNote(_ int, note string) {
	ui.transcript().line(DetailIndent + core.VisibleText(note))
}

// showValue renders a string field the operator approves: as JSON on one
// line, or quoted line by line when it spans lines. Every byte stays visible:
// a carriage return is spelled out like any control, and the label says when
// the value ends with a newline.
func (ui *CLIToolUI) showValue(label, value string) {
	t := ui.transcript()
	if !strings.Contains(value, "\n") {
		t.line(DetailIndent + label + ": " + core.MarshalJSONForDisplay(value))
		return
	}
	body, newline := strings.CutSuffix(value, "\n")
	lines := strings.Split(body, "\n")
	described := core.Plural(len(lines), "line")
	if newline {
		described += ", ending with a newline"
	}
	t.line(fmt.Sprintf("%s%s (%s):", DetailIndent, label, described))
	switch {
	case slices.ContainsFunc(lines, func(line string) bool { return strings.Contains(line, "\t") }):
		t.line(DetailIndent + "Tabs and control characters are shown as \\uXXXX.")
	case slices.ContainsFunc(lines, func(line string) bool { return strings.IndexFunc(line, core.IsHiddenControl) >= 0 }):
		t.line(DetailIndent + "Control characters are shown as \\uXXXX.")
	}
	t.quoted(lines, 0, valueText)
}

// standardProgramDirs are where a bare command name is expected to resolve.
var standardProgramDirs = map[string]bool{
	"/bin": true, "/sbin": true, "/usr/bin": true, "/usr/sbin": true,
	"/usr/local/bin": true, "/usr/local/sbin": true,
}

// unusualProgram returns the program a bare name resolves to through PATH,
// as os/exec resolves it, when it lies outside the standard directories.
func unusualProgram(name string) string {
	if strings.ContainsRune(name, filepath.Separator) {
		return ""
	}
	path, err := exec.LookPath(name)
	if err != nil || standardProgramDirs[filepath.Dir(path)] {
		return ""
	}
	return path
}

// Long objects and argument vectors get one field per line. JSON indentation
// preserves strings and numeric spelling; approval values are never shortened.
// Arguments that are not valid JSON arrive as the model sent them, so hidden
// characters are spelled out here.
func (ui *CLIToolUI) showJSON(label, value string) {
	t := ui.transcript()
	value = core.VisibleText(value)
	if len(label)+len(value) > 100 && (strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")) {
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(value), DetailIndent, "  ") == nil {
			t.line(label + ":")
			for _, line := range strings.Split(DetailIndent+pretty.String(), "\n") {
				t.line(line)
			}
			return
		}
	}
	t.line(label + ": " + value)
}

// imageCallArgs decodes a view_image call's arguments for display, and
// returns nil for any other tool or for arguments that do not decode, which
// then fall through to the generic rendering.
func imageCallArgs(call core.ToolCall) *core.ViewImageArgs {
	if call.Name != core.ViewImageToolName {
		return nil
	}
	var args core.ViewImageArgs
	if json.Unmarshal(call.Args, &args) != nil || args.Path == "" {
		return nil
	}
	return &args
}

// BeforeRun announces execution after every approval has resolved.
func (ui *CLIToolUI) BeforeRun(total, runnable, parallel int) {
	t := ui.transcript()
	t.blank()
	if runnable == 0 {
		t.line("No tools will be run.")
		return
	}

	var count string
	switch {
	case runnable != total:
		count = fmt.Sprintf("%d of %d tools", runnable, total)
	case runnable == 1:
		count = "1 tool"
	default:
		count = fmt.Sprintf("%d tools", runnable)
	}
	parallelText := ""
	if parallel > 1 {
		parallelText = " in parallel"
	}
	t.line(fmt.Sprintf("Running %s%s...", count, parallelText))
}

// CallStarted and CallFinished say nothing: the transcript speaks when a
// call's result is ready.
func (ui *CLIToolUI) CallStarted(int, core.ToolCall) {}
func (ui *CLIToolUI) CallFinished(int)               {}

// ResultReady shows one result as soon as it and every earlier one are known,
// in request order.
func (ui *CLIToolUI) ResultReady(index, total int, call core.ToolCall, result core.ToolResult) {
	t := ui.transcript()
	t.blank()
	if !ui.streamed {
		ui.streamed = true
		t.line("Tool results:")
	}
	formatToolResult(ui.notifier, ui.Options, index, total, call, result)
}

// AfterExecute ends a round's results. The executor has shown them through
// ResultReady as they arrived; a saved round arrives here whole and is shown
// the same way.
func (ui *CLIToolUI) AfterExecute(calls []core.ToolCall, results []core.ToolResult) {
	if !ui.streamed {
		for i, result := range results {
			ui.ResultReady(i, len(calls), calls[i], result)
		}
	}
	ui.streamed = false
	ui.transcript().blank()
}

func formatToolResult(notifier core.Notifier, options Options, index, total int, call core.ToolCall, result core.ToolResult) {
	t := newTranscript(notifier, options)
	args := commandCallArgs(call)
	prefix := fmt.Sprintf("[%d/%d]", index+1, total)
	if call.Name != "" || call.MCPServer != "" {
		prefix += " " + core.ToolCallDisplayName(call, args) + ":"
	}
	if call.Name == core.ViewImageToolName && result.Error == "" && len(result.Images) > 0 {
		// The question named the file, type, and size; the result only has
		// to say it went.
		descriptions := make([]string, len(result.Images))
		for i, image := range result.Images {
			descriptions[i] = core.DescribeImageBlock(image)
		}
		t.line(prefix + " Sent " + strings.Join(descriptions, ", "))
		return
	}
	status, hints := toolResultStatus(result)
	details := toolResultDetails(args)
	if call.MCPServer != "" && len(result.Images) > 0 {
		// An MCP result's text says nothing about the images beside it, the
		// way a view_image result's text does, so the count is said here.
		details = append(details, fmt.Sprintf("%d image(s) attached", len(result.Images)))
	}
	// A redirected stream landed in a file, so capturing nothing is what was
	// asked for rather than something to report.
	redirected := args != nil && (args.StdoutFile != "" || args.StderrFile != "")
	if result.Error == "" && result.Output == "" && !redirected {
		details = append(details, "no output")
	}
	if result.Truncated {
		details = append(details, "output truncated at "+core.FormatByteCount(result.TruncatedTo))
	}
	if len(details) > 0 {
		status += " (" + strings.Join(details, "; ") + ")"
	}
	t.lines(status, prefix+" ", DetailIndent)
	for _, hint := range hints {
		// Continuation lines are indented by len("Hint: ") so a multi-line hint
		// reads as one hint under the detail column rather than as stray
		// unindented output.
		t.lines(hint, DetailIndent+"Hint: ", DetailIndent+"      ")
	}

	if result.Output != "" {
		label := "Output"
		if result.Code == errors.ErrCodeTimeout || result.Code == errors.ErrCodeCancelled {
			// Only a command stopped from outside left output unfinished. One
			// that exited with an error status said everything it had to.
			label = "Partial output"
		}
		writeToolOutput(notifier, label, outputStreams(args), result.Output, options)
	}
	for _, image := range result.Images {
		t.line(DetailIndent + "[image: " + core.DescribeImageBlock(image) + "]")
	}
}

// outputStreams names what a command's captured output holds: both streams
// interleaved unless a redirection took one of them.
func outputStreams(args *core.UniversalCommandArgs) string {
	switch {
	case args == nil:
		return ""
	case args.StdoutFile != "" && args.StderrFile == "":
		return "stderr"
	case args.StderrFile != "" && args.StdoutFile == "":
		return "stdout"
	}
	return "stdout+stderr"
}

// commandCallArgs decodes a universal_command call's arguments, and returns nil
// for any other tool or for arguments that do not decode.
func commandCallArgs(call core.ToolCall) *core.UniversalCommandArgs {
	if call.Name != core.UniversalCommandToolName {
		return nil
	}
	var args core.UniversalCommandArgs
	if json.Unmarshal(call.Args, &args) != nil {
		return nil
	}
	return &args
}

func toolResultDetails(args *core.UniversalCommandArgs) []string {
	if args == nil {
		return nil
	}

	details := make([]string, 0, 3)
	if args.Stdin != nil {
		details = append(details, "stdin provided")
	} else if args.StdinFile != "" {
		details = append(details, fmt.Sprintf("stdin file %s", core.MarshalJSONForDisplay(args.StdinFile)))
	}

	if args.StdoutFile != "" {
		details = append(details, fmt.Sprintf("stdout file %s", core.MarshalJSONForDisplay(args.StdoutFile)))
	}
	if args.StderrFile != "" {
		details = append(details, fmt.Sprintf("stderr file %s", core.MarshalJSONForDisplay(args.StderrFile)))
	}
	return details
}

// toolResultStatus renders the executor's verdict. Whether the command ever
// ran is read off the result rather than reconstructed from its error code:
// this switch used to carry its own list of the six codes that meant "never
// ran", plus "Reason is set" to split a pre-run cancellation from an
// interrupted one, and nothing tied either to the executor. Nine untyped
// string constants, no exhaustiveness check, and one changeset that added two
// codes was enough for the list to be wrong — a code missing from it printed
// "Failed in 0ms" and dropped the executor's hints, and every command os/exec
// declined to start printed that too, having no denial code to be listed under.
//
// What remains keyed on Code is the two ways a command that did run was
// stopped, which is a distinction about the run and not about whether there
// was one.
func toolResultStatus(result core.ToolResult) (string, []string) {
	if result.NotRun {
		// The executor owns why the command never ran, and says so in the
		// result's structured reason and hints when it recorded them, or in its
		// error text otherwise. Restating either here would send half these
		// readers after a flag they never passed.
		if given, denied := core.UserDenial(result.Reason); denied {
			// The operator wrote the reason; the model reads the sentence
			// attributing it, which the result keeps.
			if given == "" {
				return "Denied by you", result.Hints
			}
			return "Denied by you: " + given, result.Hints
		}
		if result.Reason != "" {
			return "Not run: " + result.Reason, result.Hints
		}
		return "Not run: " + result.Error, result.Hints
	}

	if result.Error == "" {
		return fmt.Sprintf("Completed in %s", toolDuration(result.Elapsed)), nil
	}

	switch result.Code {
	case errors.ErrCodeTimeout:
		return fmt.Sprintf("Timed out in %s", toolDuration(result.Elapsed)), nil
	case errors.ErrCodeCancelled:
		return fmt.Sprintf("Cancelled in %s", toolDuration(result.Elapsed)), nil
	}
	return fmt.Sprintf("Failed in %s: %s", toolDuration(result.Elapsed), result.Error), nil
}

func toolDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

func compactToolArguments(args json.RawMessage) string {
	if len(args) == 0 {
		return "{}"
	}
	var compact bytes.Buffer
	if json.Compact(&compact, args) == nil {
		return compact.String()
	}
	return string(args)
}

// writeToolOutput shows the executor's already-bounded output under its label,
// every line quoted. With ShortOutput a long output shows its first and last
// lines and says how many lay between and where to read them, and a long line
// is cut.
func writeToolOutput(notifier core.Notifier, label, streams, output string, options Options) {
	t := newTranscript(notifier, options)
	lines := splitDisplayLines(output)
	described := core.Plural(len(lines), "line")
	if streams != "" {
		described = streams + ", " + described
	}
	hidden := 0
	if options.ShortOutput && len(lines) > shortOutputHeadLines+shortOutputTailLines+3 {
		hidden = len(lines) - shortOutputHeadLines - shortOutputTailLines
		described += fmt.Sprintf("; showing the first %d and last %d", shortOutputHeadLines, shortOutputTailLines)
	}
	t.line(fmt.Sprintf("%s%s (%s):", DetailIndent, label, described))
	if hasControlCharacters(output) {
		t.line(DetailIndent + "Control characters are shown as \\uXXXX.")
	}
	if hidden == 0 {
		lineBytes := 0
		if options.ShortOutput {
			lineBytes = shortOutputLineBytes
		}
		if cut := t.quoted(lines, lineBytes, outputText); cut > 0 && options.MoreHint != "" {
			t.line(fmt.Sprintf("%s[%s cut; %s]", DetailIndent, core.Plural(cut, "line"), options.MoreHint))
		}
		return
	}
	t.quoted(lines[:shortOutputHeadLines], shortOutputLineBytes, outputText)
	more := fmt.Sprintf("%s[%s not shown", DetailIndent, core.Plural(hidden, "line"))
	if options.MoreHint != "" {
		more += "; " + options.MoreHint
	}
	t.line(more + "]")
	t.quoted(lines[len(lines)-shortOutputTailLines:], shortOutputLineBytes, outputText)
}

// splitDisplayLines splits text into the lines a reader sees: a final newline
// ends the last line rather than starting an empty one, and CRLF is one line
// end.
func splitDisplayLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func hasControlCharacters(text string) bool {
	for _, line := range splitDisplayLines(text) {
		if strings.IndexFunc(line, core.IsHiddenControl) >= 0 {
			return true
		}
	}
	return false
}
