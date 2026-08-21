package core

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type approvalDecision int

const (
	decisionAllow approvalDecision = iota
	decisionRequireApproval
	decisionDenyBlacklist
	decisionDenyNotWhitelisted
	decisionDenyNonInteractive
)

type approvalPolicy struct {
	whitelist   []commandRule
	blacklist   []commandRule
	autoApprove bool
	// whitelistConfigured records that the operator pointed at a whitelist,
	// which is not the same as the file having produced rules. See hasWhitelist.
	whitelistConfigured bool
	// nonInteractive records that the operator asked for no prompting, which
	// is only one of the reasons prompting may be impossible.
	nonInteractive bool
	// canPrompt records whether a prompt could actually be answered. A piped
	// stdin cannot answer one whether or not -tool-non-interactive was passed,
	// and a policy that keys deny-by-default on the flag alone silently becomes
	// allow-by-default for every scripted run.
	canPrompt bool
}

// hasWhitelist reports whether a whitelist governs this policy at all, which is
// a property of the configuration and not of how many rules survived parsing.
// loadCommandRules returns (nil, nil) for a file that is empty, truncated, or
// entirely comments, so counting rules read the operator's most restrictive
// configuration — "nothing runs unless I listed it", and I listed nothing — as
// the least, and -tool-whitelist plus -tool-auto-approve ran every command in
// the round. That is the same failure canPrompt exists to prevent, one level
// down: the question is whether unlisted commands need review, and configuring
// a whitelist answers it.
//
// The rule count still counts, because a policy assembled in code carries no
// path to have configured. Either signal is enough; neither may weaken the
// other.
func (p approvalPolicy) hasWhitelist() bool {
	return p.whitelistConfigured || len(p.whitelist) > 0
}

// decide determines if a command is approved for execution.
//
// Precedence order (highest to lowest):
//  1. Blacklist check - If command matches any blacklist entry, DENY
//  2. Whitelist check - If command matches any whitelist entry, ALLOW
//  3. Whitelist present but unmatched, with no way to ask - DENY as unlisted
//  4. Auto-approve mode - If enabled, ALLOW
//  5. No way to ask - DENY
//  6. Otherwise - require the executor to prompt
//
// Steps 3 and 5 turn on whether a human can be asked, not on the
// -tool-non-interactive flag. Configuring a whitelist is a statement that
// unlisted commands need review; when review is unreachable, the answer is no.
// Keying step 3 on the flag instead let `echo task | lmc -tool -tool-whitelist
// wl.txt -tool-auto-approve` fall through to step 4 and run everything, because
// piped stdin sets no flag. Keying it on the rule count let the same command
// line run everything whenever the file happened to parse to no rules.
func (p approvalPolicy) decide(args UniversalCommandArgs) approvalDecision {
	// 1. Check blacklist first - deny if command matches any blacklist entry
	for _, b := range p.blacklist {
		if b.matches(args) {
			return decisionDenyBlacklist
		}
	}

	// 2. Check whitelist - allow if command matches any whitelist entry
	for _, w := range p.whitelist {
		if w.matches(args) {
			return decisionAllow
		}
	}

	// 3. A whitelist that cannot be extended by asking is the whole policy
	if p.hasWhitelist() && !p.canPrompt {
		return decisionDenyNotWhitelisted
	}

	// 4. If auto-approve is on, allow
	if p.autoApprove {
		return decisionAllow
	}

	// 5. Nothing left to fall back on
	if !p.canPrompt {
		return decisionDenyNonInteractive
	}

	// 6. Otherwise, we need to ask the user
	return decisionRequireApproval
}

// ExecLogger defines the minimal logging interface needed by Executor
type ExecLogger interface {
	Debugf(string, ...interface{})
	IsDebugEnabled() bool
}

// Executor handles the execution of tool commands
type Executor struct {
	defaultTimeout time.Duration
	whitelistPath  string // Path to whitelist file for error messages
	maxOutputSize  int64
	maxParallel    int
	policy         approvalPolicy
	log            ExecLogger
	approver       Approver
}

type preparedExecution struct {
	index            int
	id               string
	args             *UniversalCommandArgs
	approvalRequired bool
}

// NewExecutor creates a new tool executor.
func NewExecutor(cfg RequestOptions, log ExecLogger, approver Approver) (*Executor, error) {
	e := &Executor{
		defaultTimeout: cfg.GetToolTimeout(),
		maxOutputSize:  int64(cfg.GetToolMaxOutputBytes()),
		maxParallel:    cfg.GetMaxToolParallel(),
		log:            log,
		approver:       approver,
	}
	var whitelist, blacklist []commandRule

	// Load whitelist
	if whitelistPath := cfg.ToolWhitelist; whitelistPath != "" {
		var err error
		whitelist, err = loadCommandRules(whitelistPath, matchBareCommandOnly)
		if err != nil {
			return nil, fmt.Errorf("failed to load whitelist: %w", err)
		}
		e.whitelistPath = whitelistPath
	}

	// Load blacklist
	if blacklistPath := cfg.ToolBlacklist; blacklistPath != "" {
		var err error
		blacklist, err = loadCommandRules(blacklistPath, matchAnyCall)
		if err != nil {
			return nil, fmt.Errorf("failed to load blacklist: %w", err)
		}
	}

	// Create approval policy once. A nil approver is how the CLI reports that
	// this process has no terminal to prompt on, so it is as binding as the
	// flag.
	e.policy = approvalPolicy{
		whitelist: whitelist,
		blacklist: blacklist,
		// The path, not the rules it yielded: a whitelist that parsed to
		// nothing still says unlisted commands need review.
		whitelistConfigured: e.whitelistPath != "",
		autoApprove:         cfg.ToolAutoApprove,
		nonInteractive:      cfg.ToolNonInteractive,
		canPrompt:           approver != nil && !cfg.ToolNonInteractive,
	}

	return e, nil
}

// ExecuteParallel displays and approves every call before starting any command,
// then executes approved calls in parallel with a concurrency limit. A nil UI
// is valid only when policy does not require interactive approval.
func (e *Executor) ExecuteParallel(ctx context.Context, calls []ToolCall, ui ToolUI) []ToolResult {
	results := make([]ToolResult, len(calls))
	prepared := make([]preparedExecution, 0, min(len(calls), DefaultMaxToolCalls))
	for i, call := range calls {
		var exec preparedExecution
		var result ToolResult
		var ready bool
		if i >= DefaultMaxToolCalls {
			result = ToolResult{
				ID:    call.ID,
				Error: fmt.Sprintf("maximum tool calls per round exceeded (%d)", DefaultMaxToolCalls),
				Code:  errors.ErrCodeInvalidInput,
			}
		} else {
			exec, result, ready = e.prepareSingle(ctx, call)
		}

		if ui != nil {
			ui.ShowCall(i, len(calls), call, exec.args)
		}

		if ready && exec.approvalRequired {
			ready = e.resolveApproval(ctx, *exec.args, ui, &result)
		}

		if !ready {
			// One place says so for every way a call can be refused before it
			// runs, including the ones above that assemble their own result.
			// Leaving each site to remember would put the CLI's old
			// error-code list back, one struct literal at a time.
			result.NotRun = true
			results[i] = result
			continue
		}
		exec.index = i
		prepared = append(prepared, exec)
	}

	prepared = rejectSharedOutputFiles(prepared, results)

	workers := min(effectiveMaxParallel(e.maxParallel), len(prepared))
	if ui != nil {
		ui.BeforeRun(len(calls), len(prepared), workers)
	}

	jobs := make(chan preparedExecution)
	var wg sync.WaitGroup

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for exec := range jobs {
				results[exec.index] = e.executePrepared(ctx, exec)
			}
		}()
	}

	for _, exec := range prepared {
		jobs <- exec
	}
	close(jobs)

	wg.Wait()
	if ui != nil {
		ui.AfterExecute(calls, results)
	}
	return results
}

// effectiveMaxParallel floors the worker count at one. Zero workers is not a
// serial batch, it is a permanent one: the jobs channel is unbuffered, so the
// first send blocks with nobody ranging over it and ExecuteParallel never
// returns. RequestOptions.GetMaxToolParallel clamps a non-positive value before
// NewExecutor sees it, which is why nothing else stands between a policy
// assembled by hand — the case the nil-approver backstop above is also written
// for — and a hung turn.
func effectiveMaxParallel(maxParallel int) int {
	if maxParallel < 1 {
		return 1
	}
	return maxParallel
}

// resolveApproval asks the operator about one prepared call and reports whether
// it may run, recording the reason on result when it may not.
func (e *Executor) resolveApproval(ctx context.Context, args UniversalCommandArgs, ui ToolUI, result *ToolResult) bool {
	// The nil-approver case is unreachable through NewExecutor, which refuses to
	// set canPrompt without an approver, so decide never asks for a prompt that
	// cannot happen. It stays as the backstop for a policy assembled by hand.
	switch {
	case e.approver == nil:
		e.denyApprovalUnavailable(result, errors.ErrCodeApprovalError)
		return false
	case ui == nil:
		result.Error = "approval failed: interactive approval requires a reviewed tool UI"
		result.Code = errors.ErrCodeApprovalError
		return false
	}

	approved, err := e.approver.Approve(ctx, args)
	switch {
	case err != nil && ctx.Err() != nil:
		markCancelledBeforeRun(result)
		return false
	case err != nil:
		result.Error = fmt.Sprintf("approval failed: %v", err)
		result.Code = errors.ErrCodeApprovalError
		return false
	case !approved:
		result.Reason = "user denied permission"
		result.Error = "Command was not executed: " + result.Reason + "."
		result.Code = errors.ErrCodeNotApproved
		return false
	}
	return true
}

// markCancelledBeforeRun records a cancellation that stopped a command from
// ever starting, which NotRun is what distinguishes from a running command cut
// short — that one reports its elapsed time instead. It is not folded into
// denyResult: nothing was denied, there is no advice to give, and Error is the
// reason with nothing composed around it.
func markCancelledBeforeRun(result *ToolResult) {
	result.NotRun = true
	result.Reason = "execution cancelled"
	result.Error = result.Reason
	result.Code = errors.ErrCodeCancelled
}

// denyResult composes one policy denial. The operator reads Reason and Hints
// and the model reads Error, and the promise that those are two renderings of
// one refusal used to be a comment on the struct rather than code: the
// not-whitelisted denial folded only its second hint into Error, so "add the
// command to your whitelist file" reached the model without the path of the
// file, which the approval-unavailable denial ten lines away did include. Every
// hint the operator is shown is in the text the model is given, because there
// is one function that writes both.
//
// The two neighbouring refusals stay out. A cancellation denied nothing, and a
// user denial is a person's answer rather than a policy's, with wording of its
// own that TestDeniedApprovalBecomesExplicitModelVisibleToolError pins.
func denyResult(result *ToolResult, code, reason string, hints ...string) {
	result.NotRun = true
	result.Code = code
	result.Reason = reason
	result.Hints = hints
	result.Error = strings.Join(append([]string{"denied: " + reason}, hints...), "\n")
}

// denyApprovalUnavailable records that nobody can approve this command, naming
// the cause that applies and every route that restores approval. It is the one
// spelling of that advice, shared by the policy denial and the nil-approver
// backstop so the two cannot drift.
//
// The whitelist path is deliberately absent: this denial is only reachable
// when no whitelist is configured, because decide turns a configured whitelist
// plus no way to prompt into the not-whitelisted denial, which is the denial
// that names the file. The allow-routes list stays one hint because it reads
// as one list.
func (e *Executor) denyApprovalUnavailable(result *ToolResult, code string) {
	denyResult(result, code, e.approvalUnavailableReason(), fmt.Sprintf(`Allow via one of:
  - %s
  - Use -tool-auto-approve
  - Add the command to a whitelist`, e.restoreApprovalGuidance()))
}

// sharedFileConflict is why one call was pulled out of the round, kept per call
// so each denial names the file that collided rather than the round as a whole.
type sharedFileConflict struct {
	path string
	// reader records that the two calls were a reader and a writer rather than
	// two writers. Both sides of a pair carry the same value, because the
	// sentence describes the file and not the caller's half of it.
	reader bool
}

func (c sharedFileConflict) message() string {
	if c.reader {
		return fmt.Sprintf(
			"%q is read by one command in this round and written by another; run them in separate rounds so the reader cannot be handed a file truncated out from under it",
			c.path)
	}
	return fmt.Sprintf(
		"another command in this round also writes to %q; give each command its own output file or run them in separate rounds",
		c.path)
}

// rejectSharedOutputFiles removes calls whose files collide with another call in
// the same batch, recording a result for each one. Two collisions count: two
// calls writing one file, and one call reading a file another call writes.
//
// Two writers open the same path independently, so they hold independent file
// offsets and each one truncates what the other has already written. The batch
// finishes reporting success twice over a file that holds neither command's
// output in full.
//
// A reader loses even more quietly. Whichever order the workers reach the file,
// the writer's O_CREATE|O_TRUNC empties it, so `cat data.txt` returns nothing or
// returns the other command's bytes and is reported as a success — the model is
// told the file was empty. validateCommandIOArgs already refuses that aliasing
// inside one call for exactly this reason; scheduling the two halves as separate
// calls does not make it safe.
//
// There is no ordering that makes either safe, so the batch says no rather than
// picking a winner: the model can rerun them in separate rounds or give each its
// own file, and either way it learns why. Two readers of one file do not
// interfere and are left alone.
func rejectSharedOutputFiles(prepared []preparedExecution, results []ToolResult) []preparedExecution {
	type claim struct {
		index  int
		path   string
		writes bool
	}
	var claims []claim
	conflicts := make(map[int]sharedFileConflict)
	// One cache for the whole batch. Every path is compared against every
	// earlier one, so resolving per comparison costs syscalls per pair; a batch
	// at the DefaultMaxToolCalls ceiling is thousands of them, issued serially
	// before the first command starts and almost all of them on paths that do
	// not exist yet.
	files := commandFileIdentities{}
	note := func(index int, path string, reader bool) {
		// First collision wins: one named file is enough for the model to fix
		// the round, and a later pair must not rewrite the reason.
		if _, seen := conflicts[index]; !seen {
			conflicts[index] = sharedFileConflict{path: path, reader: reader}
		}
	}

	for _, exec := range prepared {
		for _, claimed := range commandFileClaims(exec.args) {
			for _, existing := range claims {
				if existing.index == exec.index || (!existing.writes && !claimed.writes) {
					continue
				}
				if !files.same(existing.path, claimed.path) {
					continue
				}
				reader := !existing.writes || !claimed.writes
				note(exec.index, claimed.path, reader)
				note(existing.index, existing.path, reader)
			}
			claims = append(claims, claim{index: exec.index, path: claimed.path, writes: claimed.writes})
		}
	}

	if len(conflicts) == 0 {
		return prepared
	}

	remaining := prepared[:0]
	for _, exec := range prepared {
		conflict, conflicted := conflicts[exec.index]
		if !conflicted {
			remaining = append(remaining, exec)
			continue
		}
		results[exec.index] = ToolResult{
			ID:     exec.id,
			Error:  conflict.message(),
			Code:   errors.ErrCodeInvalidInput,
			NotRun: true,
		}
	}
	return remaining
}

// commandFileClaim is one file a call will touch, and whether it will write it.
type commandFileClaim struct {
	path   string
	writes bool
}

// commandFileClaims lists every file one call takes a hold on. stdin_file
// belongs here even though nothing about it is destructive: the batch cannot see
// a reader/writer collision it was never told about, and returning only the
// outputs left a call carrying nothing but stdin_file contributing no claim at
// all.
func commandFileClaims(args *UniversalCommandArgs) []commandFileClaim {
	if args == nil {
		return nil
	}
	claims := make([]commandFileClaim, 0, 3)
	for _, file := range []commandFileClaim{
		{path: args.StdinFile},
		{path: args.StdoutFile, writes: true},
		{path: args.StderrFile, writes: true},
	} {
		if file.path == "" {
			continue
		}
		claims = append(claims, commandFileClaim{
			path:   resolveCommandFilePath(file.path, args.Workdir),
			writes: file.writes,
		})
	}
	return claims
}

// commandFileIdentities answers "do these two resolved paths name one file?"
// against one Stat and one normalization per distinct path rather than one per
// question asked. A Stat miss is cached like any other answer, because a
// not-yet-created output file is the normal case and is exactly the path that
// would otherwise be re-statted.
type commandFileIdentities map[string]commandFileIdentity

type commandFileIdentity struct {
	// comparable is the resolved path normalized for comparison only.
	comparable string
	// info is nil when the path does not exist yet, which is the usual state of
	// a file this round is about to create.
	info os.FileInfo
}

func (c commandFileIdentities) identity(path string) commandFileIdentity {
	entry, cached := c[path]
	if !cached {
		entry = commandFileIdentity{comparable: comparableCommandFilePath(path)}
		entry.info, _ = os.Stat(path)
		c[path] = entry
	}
	return entry
}

// same compares already-resolved paths. Identical text is the common case; a
// normalized comparison catches the aliases that differ only in spelling, and
// os.SameFile catches the rest, but only for files that exist — so two calls
// that will each create one not-yet-existing file are caught by the first two
// alone.
func (c commandFileIdentities) same(first, second string) bool {
	if first == second {
		return true
	}
	firstID, secondID := c.identity(first), c.identity(second)
	if firstID.comparable == secondID.comparable {
		return true
	}
	return firstID.info != nil && secondID.info != nil && os.SameFile(firstID.info, secondID.info)
}

// comparableCommandFilePath normalizes a resolved path for comparison and for
// nothing else. resolveCommandFilePath deliberately never cleans, because the OS
// resolves components in order and "link/../out.log" opened as written is not
// the file filepath.Clean would name; the open has to keep using the path the
// operator was shown on the approval line.
//
// Comparison has the opposite requirement. "out.log" and "./out.log" under one
// workdir are one file, and a file the round is about to create is a file
// os.SameFile has nothing to compare, so string equality on the uncleaned paths
// was the only check standing and both calls ran. Normalizing here can only make
// two paths compare equal that a stricter reading would have kept apart, which
// is the safe direction for a check whose answer is "deny both": it may reject
// more, never fewer.
func comparableCommandFilePath(path string) string {
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	// A relative path resolved against no workdir is relative to the process
	// working directory, which is where the command would run.
	if absolute, err := filepath.Abs(cleaned); err == nil {
		return absolute
	}
	return cleaned
}

// parseCommandArgs extracts and validates command arguments from tool call
func parseCommandArgs(call ToolCall) (*UniversalCommandArgs, error) {
	var cmdArgs UniversalCommandArgs
	if err := json.Unmarshal(call.Args, &cmdArgs); err != nil {
		return nil, fmt.Errorf("invalid command format: %v", err)
	}
	if len(cmdArgs.Command) == 0 {
		return nil, fmt.Errorf("command array cannot be empty")
	}
	if err := validateCommandIOArgs(&cmdArgs); err != nil {
		return nil, err
	}
	return &cmdArgs, nil
}

// errStdinAliasesOutput is the one spelling of the stdin/output aliasing
// rejection. The pre-approval path check below and the post-open descriptor
// check in configureCommandFiles are deliberately separate layers, but they
// refuse for one reason and must describe it in one voice.
func errStdinAliasesOutput(stream string) error {
	return fmt.Errorf("stdin_file cannot refer to the same file as %s_file", stream)
}

func validateCommandIOArgs(args *UniversalCommandArgs) error {
	if args.Stdin != nil && args.StdinFile != "" {
		return fmt.Errorf("stdin and stdin_file cannot be used together")
	}
	if args.StdinFile == "" {
		return nil
	}

	files := commandFileIdentities{}
	stdinPath := resolveCommandFilePath(args.StdinFile, args.Workdir)
	for _, output := range []struct{ stream, path string }{
		{stream: "stdout", path: args.StdoutFile},
		{stream: "stderr", path: args.StderrFile},
	} {
		if output.path == "" {
			continue
		}
		if files.same(stdinPath, resolveCommandFilePath(output.path, args.Workdir)) {
			return errStdinAliasesOutput(output.stream)
		}
	}
	return nil
}

// commandTimeout turns a model-supplied timeout in seconds into a duration,
// bounded by MaxCommandTimeoutSeconds.
//
// time.Duration is an int64 count of nanoseconds, so multiplying by time.Second
// overflows above roughly 9.2e9 seconds and the deadline lands in the past:
// {"timeout":10000000000} produced a -2346317h deadline, so context.WithTimeout
// was born expired and the command was reported as having timed out without
// ever starting. The wrap is not something the result can be tested for either —
// 20000000000 wraps back to a positive duration — so the bound has to apply to
// the seconds, ahead of the multiply.
//
// Out-of-range asks are clamped rather than rejected, because a large number is
// how a model spells "no limit" and the ceiling is what it meant. The clamp is
// visible in the timeout message if the command actually reaches it.
func commandTimeout(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	if seconds > MaxCommandTimeoutSeconds {
		seconds = MaxCommandTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// executeCommand runs the command with its timeout and standard I/O settings.
//
// started answers whether a process was ever created, and this is the only
// layer that can: os/exec assigns Cmd.Process once StartProcess has returned,
// so everything refused ahead of it — an executable that does not resolve, a
// workdir the child cannot enter, a redirection that could not be opened — is a
// command that never ran. Reported as an ordinary failure it read as "Failed in
// 0ms: chdir /no/such/dir", which describes a command that ran instantly and
// says nothing about the zero bytes it did not write.
func (e *Executor) executeCommand(ctx context.Context, cmdArgs *UniversalCommandArgs) (output string, truncated, started bool, err error) {
	timeout := commandTimeout(cmdArgs.Timeout, e.defaultTimeout)
	if cmdArgs.Timeout > MaxCommandTimeoutSeconds && e.log != nil && e.log.IsDebugEnabled() {
		e.log.Debugf("Command timeout %ds exceeds the %ds maximum; using %v",
			cmdArgs.Timeout, MaxCommandTimeoutSeconds, timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdArgs.Command[0], cmdArgs.Command[1:]...)

	// A descendant that inherits the captured output pipe keeps it open after
	// the direct child exits, and Wait blocks on the pipe rather than on the
	// process. Without a bound, `sleep 20 & echo hi` under a 2s timeout returns
	// after 20 seconds and reports success, because nothing in that path is
	// watching the deadline. WaitDelay caps the wait and makes the overrun an
	// error instead of a silent one.
	cmd.WaitDelay = CommandWaitDelay

	// Set working directory
	if cmdArgs.Workdir != "" {
		cmd.Dir = cmdArgs.Workdir
	}

	// Setup environment (inherit current + additions)
	cmd.Env = os.Environ()
	for k, v := range cmdArgs.Environ {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Capture streams that were not redirected to files.
	output, truncated, err = runCommandWithIO(ctx, cmd, cmdArgs, e.maxOutputSize)
	return output, truncated, cmd.Process != nil, classifyCommandOutcome(err, ctx.Err(), timeout)
}

// classifyCommandOutcome decides what actually ended the command.
//
// ctx.Err() at return time describes the deadline, not the command. The two
// come apart whenever Wait outlives the process: `sleep 5 & exit 3` under a 1s
// timeout exits 3 immediately and is still being waited on when the deadline
// passes, so reading the context alone reports a timeout for a command that
// chose its own status a second earlier. Cancellation has the mirror problem —
// a Ctrl-C mid-batch would rewrite every sibling's real failure as
// "context canceled" and throw away the diagnostic.
//
// So the command's own error wins. Only two outcomes defer to the context: a
// process killed by a signal, which is how a deadline actually stops one, and
// an error that is already a context error.
func classifyCommandOutcome(err, ctxErr error, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	if !commandKilledBySignal(err) &&
		!stdErrors.Is(err, context.DeadlineExceeded) &&
		!stdErrors.Is(err, context.Canceled) {
		return err
	}

	switch {
	case stdErrors.Is(ctxErr, context.DeadlineExceeded):
		return fmt.Errorf("command timed out after %v: %w", timeout, ctxErr)
	case ctxErr != nil:
		return fmt.Errorf("command cancelled: %w", ctxErr)
	}
	return err
}

// commandKilledBySignal separates the process being stopped from the process
// deciding to stop. A deadline kills, so Exited() is false; a command that
// returns 3 exited on its own terms no matter what the clock was doing.
func commandKilledBySignal(err error) bool {
	var exitErr *exec.ExitError
	return stdErrors.As(err, &exitErr) && !exitErr.Exited()
}

// prepareSingle parses and policy-checks one call. A denied call still returns
// its parsed args so the UI can render what was denied without re-decoding.
func (e *Executor) prepareSingle(ctx context.Context, call ToolCall) (preparedExecution, ToolResult, bool) {
	result := ToolResult{ID: call.ID}

	if ctx.Err() != nil {
		markCancelledBeforeRun(&result)
		return preparedExecution{}, result, false
	}

	if call.Name != UniversalCommandToolName {
		result.Error = fmt.Sprintf("unsupported tool: %s", call.Name)
		result.Code = errors.ErrCodeInvalidInput
		return preparedExecution{}, result, false
	}

	cmdArgs, err := parseCommandArgs(call)
	if err != nil {
		result.Error = err.Error()
		result.Code = errors.ErrCodeInvalidInput
		return preparedExecution{}, result, false
	}

	decision := e.policy.decide(*cmdArgs)
	switch decision {
	case decisionAllow:
		return preparedExecution{id: call.ID, args: cmdArgs}, result, true
	case decisionRequireApproval:
		return preparedExecution{id: call.ID, args: cmdArgs, approvalRequired: true}, result, true
	case decisionDenyBlacklist:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Command rejected: %s | Reason: blacklisted", cmdArgs.Command)
		}
		denyResult(&result, errors.ErrCodeDeniedBlacklist, "blacklisted")
		return preparedExecution{args: cmdArgs}, result, false
	case decisionDenyNotWhitelisted:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Command rejected: %s | Reason: not in whitelist", cmdArgs.Command)
		}
		guidance := fmt.Sprintf(`To allow this command, either:
  1. Add %s to your whitelist file and use -tool-whitelist <file>
  2. %s`, suggestedCommandRuleJSON(cmdArgs), e.restoreApprovalGuidance())
		denyResult(&result, errors.ErrCodeDeniedNotWhitelisted, "not in whitelist",
			"Whitelist file: "+e.whitelistPath, guidance)
		return preparedExecution{args: cmdArgs}, result, false
	case decisionDenyNonInteractive:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Command rejected: %s | Reason: approval unavailable", cmdArgs.Command)
		}
		e.denyApprovalUnavailable(&result, errors.ErrCodeDeniedNonInteractive)
		return preparedExecution{args: cmdArgs}, result, false
	}

	result.Error = "unsupported approval decision"
	result.Code = errors.ErrCodeInvalidInput
	return preparedExecution{}, result, false
}

// approvalUnavailableReason names why nobody can be asked. Telling a script
// that approval is "disabled by -tool-non-interactive" when the flag was never
// passed sends the reader looking for a flag they did not set.
func (e *Executor) approvalUnavailableReason() string {
	if e.policy.nonInteractive {
		return "approval disabled by -tool-non-interactive"
	}
	return "no terminal available for approval"
}

// restoreApprovalGuidance is the counterpart advice, and has the same problem
// in reverse: telling an interactive user to drop a flag they did pass is
// actionable, telling a cron job to "run interactively" is not.
func (e *Executor) restoreApprovalGuidance() string {
	if e.policy.nonInteractive {
		return "Run interactively without -tool-non-interactive"
	}
	return "Run from a terminal so commands can be approved"
}

func (e *Executor) executePrepared(ctx context.Context, exec preparedExecution) ToolResult {
	result := ToolResult{ID: exec.id}
	cmdArgs := exec.args

	if ctx.Err() != nil {
		markCancelledBeforeRun(&result)
		return result
	}

	// Start execution timer
	start := time.Now()

	// Log execution start
	if e.log != nil && e.log.IsDebugEnabled() {
		e.log.Debugf("Executing command: %s | Args: %v | Dir: %s | Timeout: %v",
			cmdArgs.Command[0], cmdArgs.Command[1:], cmdArgs.Workdir,
			commandTimeout(cmdArgs.Timeout, e.defaultTimeout))
	}

	// Execute the command
	output, truncated, started, err := e.executeCommand(ctx, cmdArgs)

	// Record results
	result.Elapsed = int64(time.Since(start).Milliseconds())
	result.Output = output
	result.Truncated = truncated
	if truncated {
		result.TruncatedTo = int(e.maxOutputSize)
	}
	if err != nil {
		result.Error = err.Error()
		result.NotRun = !started
		switch {
		case stdErrors.Is(err, context.DeadlineExceeded):
			result.Code = errors.ErrCodeTimeout
		case stdErrors.Is(err, context.Canceled):
			result.Code = errors.ErrCodeCancelled
		default:
			result.Code = errors.ErrCodeExecError
		}
	}

	// Log execution result
	if e.log != nil && e.log.IsDebugEnabled() {
		if result.Error != "" {
			e.log.Debugf("Command failed | Error: %s | Duration: %dms | Output size: %d bytes",
				result.Error, result.Elapsed, len(result.Output))
		} else {
			e.log.Debugf("Command completed | Duration: %dms | Output size: %d bytes | Truncated: %v",
				result.Elapsed, len(result.Output), result.Truncated)
		}
	}

	return result
}
