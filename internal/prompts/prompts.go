// Package prompts contains all system prompts and text constants used throughout lmtools.
// This centralized location makes it easy to maintain and update all user-facing text.
//
// To modify any prompts or messages:
// 1. Find the constant in this file
// 2. Update the text as needed
// 3. Rebuild with 'make build'
//
// All prompts, tool descriptions, error messages, and warnings are defined here.
package prompts

// Default system prompts
const (
	// DefaultSystemPrompt is the standard system prompt when no tools are enabled
	DefaultSystemPrompt = "You are a brilliant assistant."

	// ToolSystemPrompt is the system prompt used when the universal_command tool is enabled
	ToolSystemPrompt = `You are a brilliant assistant with access to system commands via the universal_command tool.

Important notes about command execution:
- Commands are executed directly via execvpe, not through a shell
- Executables are found using the PATH environment variable
- No shell features are available: no pipes (|), no redirections (>, <), no globbing (*)
- Always run executables directly by name (e.g., ["ls"], not ["/bin/ls"] or ["sh", "-c", "ls"])

You can use standard tools like:
- cat, head, tail for reading files
- ed for editing files
- grep, find, sed, awk for searching and processing
- ls, pwd for navigation
- Note: cd changes directory only for that single command execution, not subsequent commands
  Use the "workdir" parameter in the tool input to run commands in a specific directory

Only as a last resort, if you absolutely need shell features like pipes or redirections:
- Use ["bash", "-c", "command | pipe > redirect"]
- Or use scripting languages like ["python", "-c", "code"]

Always ensure commands are safe and appropriate for the user's request.`
)

// Tool descriptions
const (
	// UniversalCommandDescription describes the universal_command tool
	UniversalCommandDescription = "Execute system commands with environment control, similar to execvpe"

	// UniversalCommandParamDescription describes the command parameter
	UniversalCommandParamDescription = "Command and arguments array [executable, arg1, arg2, ...] (e.g., via execvpe, such as [\"ls\",\"-al\"], [\"grep\",\"regex\",\"filepath\"], or [\"sed\",\"-n\",\"300,600p\",\"filepath\"])"

	// UniversalCommandEnvDescription describes the environ parameter
	UniversalCommandEnvDescription = "Additional environment variables to set (e.g., {\"PATH\": \"/custom/path\", \"DEBUG\": \"1\"})"

	// UniversalCommandWorkdirDescription describes the workdir parameter
	UniversalCommandWorkdirDescription = "Working directory for command execution"

	// UniversalCommandTimeoutDescription describes the timeout parameter
	UniversalCommandTimeoutDescription = "Maximum runtime in seconds (default: 30)"
)

// Error messages
const (
	// ErrEmbedWithTool is shown when trying to use embed mode with tools
	ErrEmbedWithTool = "invalid flag combination: embed mode cannot be used with tool"

	// ErrEmbedWithStream is shown when trying to use embed mode with streaming
	ErrEmbedWithStream = "invalid flag combination: embed mode cannot be used with stream"

	// ErrEmbedWithSession is shown when trying to use embed mode with session flags
	ErrEmbedWithSession = "invalid flag combination: embed mode cannot be used with session flags (-resume, -branch)"
)

// Warning messages
const (
// Placeholder for future warning messages
)

// Command approval messages
const (
	// NonInteractiveDenialGuidance provides guidance when a command is denied in non-interactive mode
	NonInteractiveDenialGuidance = `Allow via one of:
  - Run interactively (remove -tool-non-interactive)
  - Use -tool-auto-approve
  - Add to whitelist (-tool-whitelist <file>)`

	// NotInWhitelistGuidance provides guidance when a command is not in the whitelist
	NotInWhitelistGuidance = `To allow this command, either:
  1. Add %s to your whitelist file and use -tool-whitelist <file>
  2. Run interactively (without -tool-non-interactive)
  3. Use -tool-auto-approve (allows all non-blacklisted commands)`
)
