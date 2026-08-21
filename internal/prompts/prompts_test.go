package prompts

import (
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
		"stdin_file must not be the same file as either output file",
		"stdout_file and stderr_file replace captured output",
	} {
		if !strings.Contains(ToolSystemPrompt, required) {
			t.Errorf("ToolSystemPrompt does not explain %q", required)
		}
	}
}

// TestToolSystemPromptDoesNotAdvertiseADiscardMechanism pins the absence of a
// mechanism the prompt used to promise. Omitting a redirection does not throw
// the stream away: runCommandWithIO points the non-redirected stdout and stderr
// at one cappedWriter, whose contents become ToolResult.Output and then the
// model-visible ToolResultBlock.Content. Nor is there another sink, since every
// redirection target must be a regular file and /dev/null is a device. Telling
// the model to omit the redirection "to discard output" sent it looking for a
// suppression it can never get; the honest advice is a scratch file it can read
// back in pieces.
func TestToolSystemPromptDoesNotAdvertiseADiscardMechanism(t *testing.T) {
	for _, forbidden := range []string{
		"omit the redirection",
		"to discard output",
		"to discard a stream",
	} {
		if strings.Contains(ToolSystemPrompt, forbidden) {
			t.Errorf("ToolSystemPrompt still offers %q, but lmc discards no output", forbidden)
		}
	}

	for _, required := range []string{
		// What actually becomes of an unredirected stream.
		"captured into the tool result",
		// The bound on that capture, which is why large output needs a plan.
		"cut off at a byte cap",
		// The only way to keep large output out of the conversation.
		"scratch file",
		// The rejection is real; it is the discard advice that was not.
		"/dev/null does not work",
	} {
		if !strings.Contains(ToolSystemPrompt, required) {
			t.Errorf("ToolSystemPrompt does not state %q", required)
		}
	}
}
