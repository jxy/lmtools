package core

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/prompts"
)

// UniversalCommandToolName is the name of the built-in command tool. The
// executor dispatches on it and the CLI renders on it, so a typo degrades
// silently to a generic result rather than failing.
const UniversalCommandToolName = "universal_command"

// ViewImageToolName is the name of the built-in image tool, dispatched and
// rendered on the same way as UniversalCommandToolName.
const ViewImageToolName = "view_image"

// ViewImageArgs is the canonical JSON input accepted by view_image.
type ViewImageArgs struct {
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

// GetBuiltinTools lists the tools -tool advertises: universal_command, and
// view_image on every wire but the legacy Argo one, which has not been shown
// to accept the content arrays an image result needs, less whatever the run
// withholds through ExcludedTools. The image tool names the cap it loads
// under, which is the cap the executor enforces for the same options, so
// the model is told the number it will be held to.
func GetBuiltinTools(cfg RequestOptions) []ToolDefinition {
	var tools []ToolDefinition
	if !cfg.ExcludesTool(UniversalCommandToolName) {
		tools = append(tools, GetBuiltinUniversalCommandTool()...)
	}
	if !cfg.ArgoLegacy && !cfg.ExcludesTool(ViewImageToolName) {
		tools = append(tools, GetBuiltinViewImageTool(ToolImageByteLimit(cfg)))
	}
	return tools
}

// BuiltinToolNames are the built-in tools in the order they are advertised,
// which is the order -tool-include and -tool-exclude report them in.
var BuiltinToolNames = []string{UniversalCommandToolName, ViewImageToolName}

// ToolSystemPromptFor is the system prompt a tool run writes on its own:
// the command prompt, or the shorter one when universal_command is among
// the excluded tools. The config layer picks the effective prompt with it
// and the resume policy decides a fork with it, so the two agree.
func ToolSystemPromptFor(excluded []string) string {
	for _, name := range excluded {
		if name == UniversalCommandToolName {
			return prompts.ToolSystemPromptWithoutCommand
		}
	}
	return prompts.ToolSystemPrompt
}

// ToolImageByteLimit is the cap view_image loads under. Anthropic rejects an
// image over its documented limit on the follow-up request, after the result
// has been committed to the session, so the Anthropic wire caps at that limit
// and every other wire at the -image cap. Either way a file over the cap is
// an error result the model can act on rather than a turn no resume can
// replay.
func ToolImageByteLimit(cfg RequestOptions) int {
	if effectiveResponseProvider(cfg) == constants.ProviderAnthropic {
		return constants.MaxAnthropicImageBytes
	}
	return constants.MaxCLIImageBytes
}

// GetBuiltinViewImageTool returns the built-in view_image tool definition for
// a given byte cap.
func GetBuiltinViewImageTool(maxBytes int) ToolDefinition {
	return ToolDefinition{
		Name: ViewImageToolName,
		Description: "Load an image file from the local filesystem so you can see it; the image is returned with this tool's result." +
			" Use it to inspect a plot, screenshot, or other picture that a command produced or the user named." +
			fmt.Sprintf(" The file must be %s and at most %s; resize or convert a larger or different file with a command first.",
				SupportedImageMediaTypesText, FormatByteCount(maxBytes)),
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Path of the image file, absolute or relative to the working directory; must name a regular file directly, not a symlink",
				},
				"detail": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"auto", "low", "high"},
					"description": "Resolution hint for providers that take one; omit for the default",
				},
			},
			"required": []string{"path"},
		},
	}
}

// outputRedirectDescription is the shared tail of the stdout_file and
// stderr_file schema descriptions: one spelling of the output-redirection
// contract, so the two streams cannot drift apart.
const outputRedirectDescription = " to this file instead of returning it; created or truncated; relative to workdir." +
	" Must name a regular file directly, not a symlink or FIFO. The only non-regular targets accepted are " +
	constants.PermittedCommandDevicesText + "; write to /dev/null to discard the stream." +
	" Leaving this unset does not discard it: it is captured into the tool result, up to a byte cap," +
	" so send a large one to a scratch file and read back the part you need"

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
						"type": "string",
						"description": "File streamed to standard input; relative to workdir; do not combine with stdin, and do not name the same regular file as an output." +
							" Must name an existing regular file directly, not a symlink or FIFO. The only non-regular sources accepted are " +
							constants.PermittedCommandDevicesText + "; /dev/stdin and the like are rejected",
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
