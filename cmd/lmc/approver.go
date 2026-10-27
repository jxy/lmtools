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
	history  *replHistory
	stop     context.CancelCauseFunc
	// quietCancel leaves an interrupted question unannounced: the REPL
	// reports the cancelled turn once, in words of its own.
	quietCancel bool
	// bell, when set, is the REPL's /bell setting: on, the terminal rings
	// as a question appears.
	bell *bool
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
// input typed before the question that was discarded. remaining counts the
// later calls an answer of "a" approves along with this one; zero means the
// answer is not offered.
type question struct {
	prompt, eofNotice, cancelNotice, discardedNotice string
	compose                                          bool
	remaining                                        int
	// lead opens the question, before any notice: a named question
	// starts after an empty line, apart from the calls listed above it.
	lead string
}

// toolQuestion asks about one call. The executor shows a round's calls before
// it asks about any of them, and says which call each question is about; such
// a question starts the line with the call's number. A question asked without
// a round, such as a rerun, sits under the call's review block, aligned with
// its detail column.
func toolQuestion(ctx context.Context, ask, eofNotice, cancelNotice string) question {
	q := question{
		compose:         true,
		prompt:          tools.DetailIndent + ask + toolApprovalChoices,
		eofNotice:       "\n" + tools.DetailIndent + eofNotice + "\n",
		cancelNotice:    "\n" + tools.DetailIndent + cancelNotice + "\n",
		discardedNotice: tools.DetailIndent + discardedInputNotice + "\n",
	}
	if target, ok := core.ApprovalTargetFrom(ctx); ok {
		choices := toolApprovalChoices
		if target.Remaining > 0 {
			choices = toolApprovalChoicesWithRest
			q.remaining = target.Remaining
		}
		q.prompt = fmt.Sprintf("[%d/%d] %s%s", target.Index+1, target.Total, ask, choices)
		q.lead = "\n"
	}
	return q
}

// commandName names a command inside a question by its program.
func commandName(args core.UniversalCommandArgs) string {
	call := core.ToolCall{Name: core.UniversalCommandToolName}
	return core.ToolCallDisplayName(call, &args)
}

// discardedInputNotice reports a complete line that was typed before a
// question appeared and dropped, so it could not answer the question.
const discardedInputNotice = "Discarded input typed before this question."

const (
	toolApprovalChoices         = "[y/N or reason; /ed to edit]: "
	toolApprovalChoicesWithRest = "[y/N, a = this and the rest, or reason; /ed to edit]: "
)

func (a *cliApprover) Approve(ctx context.Context, args core.UniversalCommandArgs) (core.Approval, error) {
	// The tool UI renders the command through this same notifier immediately
	// above this prompt — newOperatorToolSurface hands both halves one stream —
	// so the question and its outcomes line up under that block's detail column.
	return a.ask(ctx, toolQuestion(ctx,
		"Allow "+commandName(args)+"? ",
		"No interactive input available; denying by default.",
		"Approval prompt cancelled.",
	))
}

// ApproveImage asks about the file a view_image call read. The review line
// above it showed the path the model asked for; the question names the type
// and size of what was actually read, which is what will be sent.
func (a *cliApprover) ApproveImage(ctx context.Context, _ core.ViewImageArgs, image core.ImageBlock) (core.Approval, error) {
	send := fmt.Sprintf("Send %s to the model? ", core.DescribeImageBlock(image))
	return a.ask(ctx, toolQuestion(ctx, send,
		"No interactive input available; denying by default.",
		"Approval prompt cancelled.",
	))
}

// ApproveMCP asks about a call to an MCP tool. The review line above it
// showed the server, the tool, and the arguments; the question names the
// server and tool the way a rule would.
func (a *cliApprover) ApproveMCP(ctx context.Context, call core.MCPCall) (core.Approval, error) {
	ask := "Call " + core.DisplayName(call.Server+"/"+call.Tool) + "? "
	return a.ask(ctx, toolQuestion(ctx, ask,
		"No interactive input available; denying by default.",
		"Approval prompt cancelled.",
	))
}

// ApproveRerun asks about a call an earlier run started without recording
// its outcome. The notice above it named the call and its arguments; running
// it again may repeat whatever it already did, which is why the answer
// defaults to no and no policy can give it.
func (a *cliApprover) ApproveRerun(ctx context.Context, call core.ToolCall) (core.Approval, error) {
	ask := "Run " + core.ToolCallDisplayName(call, nil) + " again? It may already have run. "
	return a.ask(ctx, toolQuestion(ctx, ask,
		"No interactive input available; not running it again.",
		"Rerun prompt cancelled.",
	))
}

func (a *cliApprover) ApproveToolRoundLimitReset(ctx context.Context, maxRounds int) (bool, error) {
	approval, err := a.ask(ctx, question{
		prompt:          fmt.Sprintf("\nTool-call round limit reached (%d). Reset it and continue? [y/N]: ", maxRounds),
		eofNotice:       "\nNo interactive input available; keeping the tool-call round limit.\n",
		cancelNotice:    "\nTool-call round prompt cancelled.\n",
		discardedNotice: "\n" + discardedInputNotice,
	})
	return approval.Approved, err
}

// ask holds the approval conversation every question shares: refuse a
// question that is already cancelled, show it, and read the answer. The
// answer source shows the question once it has dropped what was typed
// before, and says whether that held a complete line, which the notice
// reports above the question.
func (a *cliApprover) ask(ctx context.Context, q question) (core.Approval, error) {
	if err := ctx.Err(); err != nil {
		return core.Approval{}, err
	}
	bound := maxAnswerBytes
	readBound := bound
	if q.compose {
		bound = maxTurnBytes
		readBound = bound + 2 // Continuation marker and carriage return.
	}
	editing := false
	if owner, ok := a.answers.(*inputOwner); ok && owner.editing {
		editing = true
		// The question keeps its indent; only the space that would have
		// held the answer goes, since the answer has a line of its own.
		q.prompt = strings.TrimRight(q.prompt, " ") + "\n" + approvalPrompt
		owner.setPrompt(approvalPrompt)
	}
	got, err := a.answers.answer(ctx, readBound, func(discardedLine bool) {
		if q.lead != "" {
			a.notifier.Promptf("%s", q.lead)
		}
		if discardedLine {
			a.notifier.Promptf("%s", q.discardedNotice)
		}
		if a.bell != nil && *a.bell {
			a.notifier.Promptf("\a")
		}
		a.notifier.Promptf("%s", q.prompt)
	})
	if err == nil && q.compose && !got.eof {
		got, err = a.composeAnswer(ctx, got)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if !a.quietCancel {
				a.notifier.Promptf("%s", q.cancelNotice)
			}
			return core.Approval{}, ctxErr
		}
		return core.Approval{}, errors.WrapError("read response", err)
	}
	if got.eof && editing {
		// In the editor Ctrl-D ends input only on an empty line, and an
		// answer nobody gave is a refusal. Only Enter submits; text an
		// unfinished paste left behind is discarded.
		if got.text != "" || got.tooLong {
			a.notifier.Infof("Unsubmitted approval draft discarded.")
		}
		a.notifier.Promptf("\n%sDenied.\n", tools.DetailIndent)
		return core.Approval{}, nil
	}
	if got.eof {
		if q.compose {
			text, continues := continuationMarker(strings.TrimSuffix(got.text, "\r"))
			command, _ := splitREPLCommand(text)
			if continues || command == "/ed" {
				a.notifier.Infof("Unsubmitted approval draft discarded.")
				got.text = ""
			} else {
				got.text = text
				if strings.HasPrefix(text, "//") {
					got.text = text[1:]
				}
			}
		}
		// An answer and the end of input arrive together when the operator
		// types y and then Ctrl-D; reading only the end would turn their
		// approval into a denial. The absence of an answer is an empty line,
		// which keeps the fail-safe denial the [y/N] default promises.
		if got.text == "" && !got.tooLong {
			a.notifier.Promptf("%s", q.eofNotice)
			return core.Approval{}, nil
		}
		// Nothing terminated the answer, so nothing terminated the echo of
		// it either; close the prompt line the way a typed newline would.
		a.notifier.Promptf("\n")
	}
	if got.tooLong || len(got.text) > bound {
		a.notifier.Warnf("Approval response is too long; denying without a reason.")
		return core.Approval{}, nil
	}
	approval := parseApprovalAnswer(got.text)
	if approval.Rest && q.remaining > 0 {
		a.notifier.Promptf("%sApproved this call and the remaining %s.\n", tools.DetailIndent, core.Plural(q.remaining, "call"))
	}
	return approval, nil
}

// parseApprovalAnswer classifies a submitted answer. y or yes approves; a or
// all approves the rest of the round too, which is this call alone when no
// later call would ask; n, no, or nothing denies; anything else denies with
// the text as the reason.
func parseApprovalAnswer(line string) core.Approval {
	text := strings.TrimSpace(line)
	switch {
	case isAffirmativeAnswer(text):
		return core.Approval{Approved: true}
	case strings.EqualFold(text, "a") || strings.EqualFold(text, "all"):
		return core.Approval{Approved: true, Rest: true}
	case text == "" || strings.EqualFold(text, "n") || strings.EqualFold(text, "no"):
		return core.Approval{}
	}
	return core.Approval{Reason: text}
}

// Only a complete y or yes approves; every other reply denies permission.
func isAffirmativeAnswer(line string) bool {
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
