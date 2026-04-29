package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/errors"
	"lmtools/internal/prompts"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ApprovalDecision represents the decision made by the approval policy
type ApprovalDecision int

const (
	// DecisionAllow means the command is approved for execution
	DecisionAllow ApprovalDecision = iota
	// DecisionRequireApproval means the command needs user approval
	DecisionRequireApproval
	// DecisionDenyBlacklist means the command is denied due to blacklist
	DecisionDenyBlacklist
	// DecisionDenyNotWhitelisted means the command is denied due to not being in whitelist
	DecisionDenyNotWhitelisted
	// DecisionDenyNonInteractive means the command is denied in non-interactive mode
	DecisionDenyNonInteractive
)

// ApprovalPolicy encapsulates command approval logic
type ApprovalPolicy struct {
	Whitelist   [][]string
	Blacklist   [][]string
	AutoApprove bool
	Interactive bool
}

// Decide determines if a command is approved for execution.
//
// Precedence order (highest to lowest):
// 1. Blacklist check - If command matches any blacklist entry, DENY
// 2. Whitelist check - If command matches any whitelist entry, ALLOW
// 3. Non-interactive mode with whitelist - If whitelist exists but no match, DENY
// 4. Auto-approve mode - If enabled, ALLOW
// 5. Non-interactive mode - DENY (no user prompt available)
// 6. Interactive mode - Return DecisionRequireApproval for user prompt
//
// This ensures that dangerous commands are always blocked (blacklist),
// trusted commands are always allowed (whitelist), and user control
// is maintained through auto-approve and interactive settings.
func (p ApprovalPolicy) Decide(cmd []string) ApprovalDecision {
	// 1. Check blacklist first - deny if command matches any blacklist entry
	for _, b := range p.Blacklist {
		if commandHasPrefix(cmd, b) {
			return DecisionDenyBlacklist
		}
	}

	// 2. Check whitelist - allow if command matches any whitelist entry
	for _, w := range p.Whitelist {
		if commandHasPrefix(cmd, w) {
			return DecisionAllow
		}
	}

	// 3. If we have a whitelist but no match in non-interactive mode
	if len(p.Whitelist) > 0 && !p.Interactive {
		return DecisionDenyNotWhitelisted
	}

	// 4. If auto-approve is on, allow
	if p.AutoApprove {
		return DecisionAllow
	}

	// 5. If not interactive, deny
	if !p.Interactive {
		return DecisionDenyNonInteractive
	}

	// 6. Otherwise, we need to ask the user
	return DecisionRequireApproval
}

// ExecLogger defines the minimal logging interface needed by Executor
type ExecLogger interface {
	Debugf(string, ...interface{})
	IsDebugEnabled() bool
}

// Executor handles the execution of tool commands
type Executor struct {
	defaultTimeout time.Duration
	whitelist      [][]string // Command prefix whitelist
	blacklist      [][]string // Command prefix blacklist
	whitelistPath  string     // Path to whitelist file for error messages
	blacklistPath  string     // Path to blacklist file for error messages
	autoApprove    bool
	nonInteractive bool
	maxOutputSize  int64
	maxParallel    int
	policy         ApprovalPolicy
	log            ExecLogger
	notifier       Notifier
	approver       Approver
}

// commandArgs holds parsed command arguments
type commandArgs struct {
	Command []string
	Environ map[string]string
	Workdir string
	Timeout int
}

type preparedExecution struct {
	index int
	call  ToolCall
	args  *commandArgs
}

// NewExecutor creates a new tool executor.
func NewExecutor(cfg ToolConfig, log ExecLogger, notifier Notifier, approver Approver) (*Executor, error) {
	e := &Executor{
		defaultTimeout: cfg.GetToolTimeout(),
		autoApprove:    cfg.GetToolAutoApprove(),
		nonInteractive: cfg.GetToolNonInteractive(),
		maxOutputSize:  int64(cfg.GetToolMaxOutputBytes()),
		maxParallel:    cfg.GetMaxToolParallel(),
		log:            log,
		notifier:       notifier,
		approver:       approver,
	}

	// Load whitelist
	if whitelistPath := cfg.GetToolWhitelist(); whitelistPath != "" {
		whitelist, err := loadList(whitelistPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load whitelist: %w", err)
		}
		e.whitelist = whitelist
		e.whitelistPath = whitelistPath
	}

	// Load blacklist
	if blacklistPath := cfg.GetToolBlacklist(); blacklistPath != "" {
		blacklist, err := loadList(blacklistPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load blacklist: %w", err)
		}
		e.blacklist = blacklist
		e.blacklistPath = blacklistPath
	}

	// Create approval policy once
	e.policy = ApprovalPolicy{
		Whitelist:   e.whitelist,
		Blacklist:   e.blacklist,
		AutoApprove: e.autoApprove,
		Interactive: !e.nonInteractive && isInteractive(),
	}

	return e, nil
}

// loadList loads a list of command prefixes from a file
// Each line must be a JSON array of strings
func loadList(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var commands [][]string
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

		// Parse as JSON array
		var cmdArray []string
		if err := json.Unmarshal([]byte(line), &cmdArray); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON array: %w", lineNum, err)
		}

		if len(cmdArray) == 0 {
			return nil, fmt.Errorf("line %d: empty command array", lineNum)
		}

		commands = append(commands, cmdArray)
	}

	return commands, scanner.Err()
}

// ExecuteParallel executes multiple tool calls in parallel with a concurrency limit
func (e *Executor) ExecuteParallel(ctx context.Context, calls []ToolCall) []ToolResult {
	results := make([]ToolResult, len(calls))
	prepared := make([]preparedExecution, 0, minInt(len(calls), DefaultMaxToolCalls))

	for i, call := range calls {
		if i >= DefaultMaxToolCalls {
			results[i] = ToolResult{
				ID:    call.ID,
				Error: fmt.Sprintf("maximum tool calls per round exceeded (%d)", DefaultMaxToolCalls),
				Code:  errors.ErrCodeInvalidInput,
			}
			continue
		}

		exec, result, ok := e.prepareSingle(ctx, call)
		if !ok {
			results[i] = result
			continue
		}
		prepared = append(prepared, preparedExecution{
			index: i,
			call:  exec.call,
			args:  exec.args,
		})
	}

	if len(prepared) == 0 {
		return results
	}

	maxParallel := e.effectiveMaxParallel()
	workers := minInt(maxParallel, len(prepared))
	jobs := make(chan int)
	var wg sync.WaitGroup

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				exec := prepared[idx]
				results[exec.index] = e.executePrepared(ctx, exec)
			}
		}()
	}

	for idx := range prepared {
		jobs <- idx
	}
	close(jobs)

	wg.Wait()
	return results
}

func (e *Executor) effectiveMaxParallel() int {
	if e.maxParallel > 0 {
		return e.maxParallel
	}
	return DefaultMaxToolParallel
}

// parseCommandArgs extracts and validates command arguments from tool call
func (e *Executor) parseCommandArgs(call ToolCall) (*commandArgs, error) {
	var cmdArgs commandArgs
	if err := json.Unmarshal(call.Args, &cmdArgs); err != nil {
		return nil, fmt.Errorf("invalid command format: %v", err)
	}
	if len(cmdArgs.Command) == 0 {
		return nil, fmt.Errorf("command array cannot be empty")
	}
	return &cmdArgs, nil
}

// executeCommand runs the command with timeout and captures output
func (e *Executor) executeCommand(ctx context.Context, cmdArgs *commandArgs) (string, bool, error) {
	timeout := e.defaultTimeout
	if cmdArgs.Timeout > 0 {
		timeout = time.Duration(cmdArgs.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cmdArgs.Command[0], cmdArgs.Command[1:]...)

	// Set working directory
	if cmdArgs.Workdir != "" {
		cmd.Dir = cmdArgs.Workdir
	}

	// Setup environment (inherit current + additions)
	cmd.Env = os.Environ()
	for k, v := range cmdArgs.Environ {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Capture output with size limit
	output, truncated, err := e.runWithOutputLimit(cmd, e.maxOutputSize)
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return output, truncated, fmt.Errorf("command timed out after %v", timeout)
	}
	return output, truncated, err
}

// executeSingle executes a single tool call
func (e *Executor) executeSingle(ctx context.Context, call ToolCall) ToolResult {
	exec, result, ok := e.prepareSingle(ctx, call)
	if !ok {
		return result
	}
	return e.executePrepared(ctx, exec)
}

func (e *Executor) prepareSingle(ctx context.Context, call ToolCall) (preparedExecution, ToolResult, bool) {
	result := ToolResult{ID: call.ID}

	if ctx.Err() != nil {
		result.Error = "execution cancelled"
		result.Code = "CANCELLED"
		return preparedExecution{}, result, false
	}

	if call.Name != "universal_command" {
		result.Error = fmt.Sprintf("unsupported tool: %s", call.Name)
		result.Code = errors.ErrCodeInvalidInput
		return preparedExecution{}, result, false
	}

	cmdArgs, err := e.parseCommandArgs(call)
	if err != nil {
		result.Error = err.Error()
		result.Code = errors.ErrCodeInvalidInput
		return preparedExecution{}, result, false
	}

	decision := e.policy.Decide(cmdArgs.Command)
	switch decision {
	case DecisionAllow:
		return preparedExecution{call: call, args: cmdArgs}, result, true
	case DecisionRequireApproval:
		approved, err := e.promptUserApproval(ctx, cmdArgs.Command)
		if err != nil {
			if ctx.Err() != nil {
				result.Error = "execution cancelled"
				result.Code = "CANCELLED"
				return preparedExecution{}, result, false
			}
			result.Error = fmt.Sprintf("approval error: %v", err)
			result.Code = errors.ErrCodeNotApproved
			return preparedExecution{}, result, false
		}
		if !approved {
			result.Error = "command not approved"
			result.Code = errors.ErrCodeNotApproved
			return preparedExecution{}, result, false
		}
		return preparedExecution{call: call, args: cmdArgs}, result, true
	case DecisionDenyBlacklist:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Command rejected: %s | Reason: blacklisted", cmdArgs.Command)
		}
		result.Error = "denied: blacklisted"
		result.Code = errors.ErrCodeDeniedBlacklist
		return preparedExecution{}, result, false
	case DecisionDenyNotWhitelisted:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Command rejected: %s | Reason: not in whitelist", cmdArgs.Command)
		}
		cmdJSON, _ := json.Marshal(cmdArgs.Command)
		result.Error = fmt.Sprintf("denied: not in whitelist\n%s", fmt.Sprintf(prompts.NotInWhitelistGuidance, string(cmdJSON)))
		result.Code = errors.ErrCodeDeniedNotWhitelisted
		return preparedExecution{}, result, false
	case DecisionDenyNonInteractive:
		if e.log != nil && e.log.IsDebugEnabled() {
			e.log.Debugf("Command rejected: %s | Reason: non-interactive mode", cmdArgs.Command)
		}
		if e.whitelistPath != "" {
			result.Error = fmt.Sprintf("command denied: non-interactive mode\nWhitelist: %s\n%s", e.whitelistPath, prompts.NonInteractiveDenialGuidance)
		} else {
			result.Error = fmt.Sprintf("command denied: non-interactive mode\n%s", prompts.NonInteractiveDenialGuidance)
		}
		result.Code = errors.ErrCodeDeniedNonInteractive
		return preparedExecution{}, result, false
	}

	result.Error = "unsupported approval decision"
	result.Code = errors.ErrCodeInvalidInput
	return preparedExecution{}, result, false
}

func (e *Executor) executePrepared(ctx context.Context, exec preparedExecution) ToolResult {
	result := ToolResult{ID: exec.call.ID}
	cmdArgs := exec.args

	if ctx.Err() != nil {
		result.Error = "execution cancelled"
		result.Code = "CANCELLED"
		return result
	}

	// Start execution timer
	start := time.Now()

	// Log execution start
	if e.log != nil && e.log.IsDebugEnabled() {
		e.log.Debugf("Executing command: %s | Args: %v | Dir: %s | Timeout: %v",
			cmdArgs.Command[0], cmdArgs.Command[1:], cmdArgs.Workdir,
			time.Duration(cmdArgs.Timeout)*time.Second)
	}

	// Execute the command
	output, truncated, err := e.executeCommand(ctx, cmdArgs)

	// Record results
	result.Elapsed = int64(time.Since(start).Milliseconds())
	result.Output = output
	result.Truncated = truncated
	if err != nil {
		result.Error = err.Error()
		// Set error code based on error type
		if strings.Contains(err.Error(), "timed out") {
			result.Code = errors.ErrCodeTimeout
		} else {
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

	// Display result - moved to CLI layer

	return result
}

// cappedWriter is a simple writer that captures output up to a maximum size
type cappedWriter struct {
	buf       bytes.Buffer
	maxSize   int64
	truncated bool
}

// Write implements io.Writer, capturing up to maxSize bytes
func (w *cappedWriter) Write(p []byte) (int, error) {
	// If we've already hit the limit, just pretend we wrote it
	if int64(w.buf.Len()) >= w.maxSize {
		w.truncated = true
		return len(p), nil
	}

	// Calculate how much we can write
	remaining := w.maxSize - int64(w.buf.Len())
	if int64(len(p)) > remaining {
		// Write what we can and mark as truncated
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil // Pretend we wrote it all to avoid blocking
	}

	// Write the full chunk
	return w.buf.Write(p)
}

// runWithOutputLimit runs a command and captures output up to maxSize bytes
func (e *Executor) runWithOutputLimit(cmd *exec.Cmd, maxSize int64) (string, bool, error) {
	// Create a capped writer for combined stdout/stderr
	cw := &cappedWriter{maxSize: maxSize}

	// Direct both stdout and stderr to our capped writer
	cmd.Stdout = cw
	cmd.Stderr = cw

	// Run the command
	err := cmd.Run()

	// Return the captured output, truncation status, and any error
	return cw.buf.String(), cw.truncated, err
}

// promptUserApproval prompts the user for approval when required
func (e *Executor) promptUserApproval(ctx context.Context, command []string) (bool, error) {
	if e.approver == nil {
		return false, fmt.Errorf("no approver configured")
	}
	return e.approver.Approve(ctx, command)
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isInteractive checks if the current environment supports interactive prompts
func isInteractive() bool {
	// Check if stdin is connected to a terminal
	if fileInfo, err := os.Stdin.Stat(); err == nil {
		return (fileInfo.Mode() & os.ModeCharDevice) != 0
	}
	return false
}
