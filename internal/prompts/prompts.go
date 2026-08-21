// Package prompts centralizes built-in system prompts, tool instructions, and
// shared guidance text.
package prompts

// Default system prompts
const (
	// DefaultSystemPrompt is the standard system prompt when no tools are enabled
	DefaultSystemPrompt = "You are a brilliant assistant."

	// ToolSystemPrompt is the system prompt used when the universal_command tool is enabled
	ToolSystemPrompt = `You are a helpful assistant with access to the universal_command tool.

Rules for universal_command:
- command is an argv array executed directly with execvpe-style semantics; no shell is implied.
- PATH resolves bare executable names. Shell features such as pipes, redirection, globbing, &&, and cd are not interpreted.
- Prefer direct argv. Invoke a shell explicitly only when shell behavior is required.
- Use workdir to set the process working directory.
- Set at most one of stdin and stdin_file.
- Relative stdin_file, stdout_file, and stderr_file paths resolve against workdir when set. stdin_file must not be the same file as either output file.
- stdout_file and stderr_file replace captured output for those streams. If they name the same file, both streams share it.
- Any stream without a file is captured into the tool result; an uncaptured stdout and stderr are combined there and the total is cut off at a byte cap. There is no way to suppress output, so write a large stream to a scratch file and read back only the part you need.
- Every redirection must name a regular file directly. Symlinks, FIFOs, and devices are rejected, so /dev/null does not work.
- Two commands in the same round must not write to the same file; the round rejects both rather than interleaving them.

Run only safe, relevant commands and use their results to complete the request.`
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
