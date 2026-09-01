package prompts

import (
	"lmtools/internal/constants"
	"strings"
	"testing"
)

func TestToolSystemPromptStatesExecutionContract(t *testing.T) {
	for _, required := range []string{
		"argv array executed directly",
		"no shell is implied",
		"Shell features",
		"Use workdir",
		"Set at most one of stdin and stdin_file",
		"stdin_file must not be the same regular file as either output file",
		"stdout_file and stderr_file replace captured output",
		constants.PermittedCommandDevicesText,
	} {
		if !strings.Contains(ToolSystemPrompt, required) {
			t.Errorf("ToolSystemPrompt does not explain %q", required)
		}
	}
}

// Omitting a redirection captures output; /dev/null is the discard mechanism.
func TestToolSystemPromptNamesTheOneDiscardMechanism(t *testing.T) {
	for _, forbidden := range []string{
		"omit the redirection",
		"/dev/null does not work",
	} {
		if strings.Contains(ToolSystemPrompt, forbidden) {
			t.Errorf("ToolSystemPrompt still says %q, which is not how discarding works", forbidden)
		}
	}

	for _, required := range []string{
		"captured into the tool result",
		"cut off at a byte cap",
		"scratch file",
		"/dev/null to throw it away",
	} {
		if !strings.Contains(ToolSystemPrompt, required) {
			t.Errorf("ToolSystemPrompt does not state %q", required)
		}
	}
}
