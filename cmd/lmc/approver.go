package main

import (
	"context"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/ui"
	"lmtools/internal/ui/tools"
	"os"
	"strings"
)

// cliApprover implements the core.Approver interface for CLI interaction
type cliApprover struct {
	notifier core.Notifier
	answers  answerSource
}

// newOperatorToolSurface builds the operator-facing half of tool execution:
// the notifier the command review renders through, and the approver that reads
// the answer. Both halves share one stream, because approving a command the
// operator was never shown defeats the review — whichever stream can carry the
// question must also carry the command it asks about.
//
// The approver is nil when this process cannot hold an approval conversation,
// which is what makes the executor deny by default instead of prompting into
// the void; the review then stays on the ordinary notifier.
//
// A conversation needs both halves. Reading the answer needs a terminal on
// stdin and there is no substitute, since lmc's own prompt arrives there when
// it is piped; input, which owns stdin, reads the answers. Showing the
// question normally uses stderr, but `lmc -tool 2>lmc.log` is a documented
// invocation and redirecting a log should not silently turn approval off, so
// a redirected stderr falls back to the controlling terminal. The descriptor
// lives as long as the process.
func newOperatorToolSurface(notifier core.Notifier, input *inputOwner) (core.Notifier, core.Approver) {
	if !input.terminal {
		return notifier, nil
	}
	promptNotifier := approvalPromptNotifier(notifier)
	if promptNotifier == nil {
		return notifier, nil
	}
	return promptNotifier, newApprover(promptNotifier, input)
}

// newApprover asks its questions on notifier and reads the answers through
// input.
func newApprover(notifier core.Notifier, input *inputOwner) *cliApprover {
	return &cliApprover{notifier: notifier, answers: input.answers()}
}

// approvalPromptNotifier picks where the question is asked. It returns nil only
// when there is nowhere the operator would see it.
func approvalPromptNotifier(notifier core.Notifier) core.Notifier {
	if isTerminalFile(os.Stderr) {
		return notifier
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return nil
	}
	return ui.NewNotifierWithWriter(tty)
}

// newCLIApproverWithReader reads answers from input as a stream.
func newCLIApproverWithReader(notifier core.Notifier, input io.Reader) *cliApprover {
	return &cliApprover{notifier: notifier, answers: newStreamAnswers(input)}
}

// question is what an approval question says: the prompt, and the notices
// for an answer missing because input ended, a question cancelled, and
// input typed before the question that was discarded.
type question struct {
	prompt, eofNotice, cancelNotice, discardedNotice string
}

// toolQuestion is a question under a tool's review block, aligned with the
// block's detail column.
func toolQuestion(prompt, eofNotice, cancelNotice string) question {
	return question{
		prompt:          tools.DetailIndent + prompt,
		eofNotice:       "\n" + tools.DetailIndent + eofNotice + "\n",
		cancelNotice:    "\n" + tools.DetailIndent + cancelNotice + "\n",
		discardedNotice: tools.DetailIndent + discardedInputNotice + "\n",
	}
}

// discardedInputNotice reports a complete line that was typed before a
// question appeared and dropped, so it could not answer the question.
const discardedInputNotice = "Discarded input typed before this question."

func (a *cliApprover) Approve(ctx context.Context, _ core.UniversalCommandArgs) (bool, error) {
	// The tool UI renders the command through this same notifier immediately
	// above this prompt — newOperatorToolSurface hands both halves one stream —
	// so the question and its outcomes line up under that block's detail column.
	return a.ask(ctx, toolQuestion(
		"Allow execution? [y/N]: ",
		"No interactive input available; denying by default.",
		"Approval prompt cancelled.",
	))
}

// ApproveImage asks about the file a view_image call read. The review line
// above it showed the path the model asked for; the question names the type
// and size of what was actually read, which is what will be sent.
func (a *cliApprover) ApproveImage(ctx context.Context, _ core.ViewImageArgs, image core.ImageBlock) (bool, error) {
	return a.ask(ctx, toolQuestion(
		fmt.Sprintf("Send %s to the model? [y/N]: ", core.DescribeImageBlock(image)),
		"No interactive input available; denying by default.",
		"Approval prompt cancelled.",
	))
}

// ApproveMCP asks about a call to an MCP tool. The review line above it
// showed the server, the tool, and the arguments; the question names the
// server and tool the way a rule would.
func (a *cliApprover) ApproveMCP(ctx context.Context, call core.MCPCall) (bool, error) {
	return a.ask(ctx, toolQuestion(
		fmt.Sprintf("Call %s/%s? [y/N]: ", call.Server, call.Tool),
		"No interactive input available; denying by default.",
		"Approval prompt cancelled.",
	))
}

// ApproveRerun asks about a call an earlier run started without recording
// its outcome. The notice above it named the call and its arguments; running
// it again may repeat whatever it already did, which is why the answer
// defaults to no and no policy can give it.
func (a *cliApprover) ApproveRerun(ctx context.Context, call core.ToolCall) (bool, error) {
	return a.ask(ctx, toolQuestion(
		fmt.Sprintf("Run %s again? It may already have run. [y/N]: ", call.Name),
		"No interactive input available; not running it again.",
		"Rerun prompt cancelled.",
	))
}

func (a *cliApprover) ApproveToolRoundLimitReset(ctx context.Context, maxRounds int) (bool, error) {
	return a.ask(ctx, question{
		prompt:          fmt.Sprintf("\nTool-call round limit reached (%d). Reset it and continue? [y/N]: ", maxRounds),
		eofNotice:       "\nNo interactive input available; keeping the tool-call round limit.\n",
		cancelNotice:    "\nTool-call round prompt cancelled.\n",
		discardedNotice: "\n" + discardedInputNotice,
	})
}

// ask holds the yes/no conversation every question shares: refuse a
// question that is already cancelled, show it, and read the answer. The
// answer source shows the question once it has dropped what was typed
// before, and says whether that held a complete line, which the notice
// reports above the question.
func (a *cliApprover) ask(ctx context.Context, q question) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	got, err := a.answers.answer(ctx, func(discardedLine bool) {
		if discardedLine {
			a.notifier.Promptf("%s", q.discardedNotice)
		}
		a.notifier.Promptf("%s", q.prompt)
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			a.notifier.Promptf("%s", q.cancelNotice)
			return false, ctxErr
		}
		return false, errors.WrapError("read response", err)
	}
	if got.eof {
		// An answer and the end of input arrive together when the operator
		// types y and then Ctrl-D; reading only the end would turn their
		// approval into a denial. The absence of an answer is an empty line,
		// which keeps the fail-safe denial the [y/N] default promises.
		if got.text == "" && !got.tooLong {
			a.notifier.Promptf("%s", q.eofNotice)
			return false, nil
		}
		// Nothing terminated the answer, so nothing terminated the echo of
		// it either; close the prompt line the way a typed newline would.
		a.notifier.Promptf("\n")
	}
	// An answer past the bound is refused, whatever it begins with.
	return !got.tooLong && isAffirmativeAnswer(got.text), nil
}

// isAffirmativeAnswer reads one typed answer. Only an explicit yes approves,
// which is what makes the advertised [y/N] default the answer to everything
// else.
func isAffirmativeAnswer(line string) bool {
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
