package core

// UniversalCommandToolName is the name of the built-in command tool. The
// executor dispatches on it and the CLI renders on it, so a typo degrades
// silently to a generic result rather than failing.
const UniversalCommandToolName = "universal_command"

// outputRedirectDescription is the shared tail of the stdout_file and
// stderr_file schema descriptions: one spelling of the output-redirection
// contract, so the two streams cannot drift apart.
const outputRedirectDescription = " to this file instead of returning it; created or truncated; relative to workdir. Must name a regular file directly, not a symlink, FIFO, or device; /dev/null is rejected. Leaving this unset does not discard the stream: it is captured into the tool result, up to a byte cap, so send a large one to a scratch file and read back the part you need"

// UniversalCommandArgs is the canonical JSON input accepted by
// universal_command and displayed by its UI.
type UniversalCommandArgs struct {
	Command    []string          `json:"command"`
	Environ    map[string]string `json:"environ,omitempty"`
	Workdir    string            `json:"workdir,omitempty"`
	Timeout    int               `json:"timeout,omitempty"`
	Stdin      *string           `json:"stdin,omitempty"`
	StdinFile  string            `json:"stdin_file,omitempty"`
	StdoutFile string            `json:"stdout_file,omitempty"`
	StderrFile string            `json:"stderr_file,omitempty"`
}

// GetBuiltinUniversalCommandTool returns the built-in universal_command tool definition
func GetBuiltinUniversalCommandTool() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        UniversalCommandToolName,
			Description: "Run one argv command directly without a shell, with optional environment, workdir, stdin, timeout, and file redirection",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "string",
						},
						"description": "Argument vector: executable followed by its arguments; shell syntax is not interpreted",
					},
					"environ": map[string]interface{}{
						"type":        "object",
						"description": "Environment variables added to the inherited process environment",
						"additionalProperties": map[string]interface{}{
							"type": "string",
						},
					},
					"workdir": map[string]interface{}{
						"type":        "string",
						"description": "Working directory for the process and relative file paths",
					},
					"stdin": map[string]interface{}{
						"type":        "string",
						"description": "Literal standard input; do not combine with stdin_file",
					},
					"stdin_file": map[string]interface{}{
						"type":        "string",
						"description": "File streamed to standard input; relative to workdir; do not combine with stdin or reuse as an output file. Must name an existing regular file directly, not a symlink, FIFO, or device such as /dev/stdin",
					},
					"stdout_file": map[string]interface{}{
						"type":        "string",
						"description": "Write stdout" + outputRedirectDescription,
					},
					"stderr_file": map[string]interface{}{
						"type":        "string",
						"description": "Write stderr" + outputRedirectDescription,
					},
					"timeout": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum runtime in seconds",
					},
				},
				"required": []string{"command"},
			},
		},
	}
}
