package main

import (
	"bufio"
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
	reader   *bufio.Reader
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
// it is piped. Showing the question normally uses stderr, but `lmc -tool
// 2>lmc.log` is a documented invocation and redirecting a log should not
// silently turn approval off, so a redirected stderr falls back to the
// controlling terminal. The descriptor lives as long as this one-shot process.
func newOperatorToolSurface(notifier core.Notifier) (core.Notifier, core.Approver) {
	if !isTerminalFile(os.Stdin) {
		return notifier, nil
	}
	promptNotifier := approvalPromptNotifier(notifier)
	if promptNotifier == nil {
		return notifier, nil
	}
	return promptNotifier, newCLIApproverWithReader(promptNotifier, os.Stdin)
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

func newCLIApproverWithReader(notifier core.Notifier, input io.Reader) *cliApprover {
	return &cliApprover{
		notifier: notifier,
		reader:   bufio.NewReader(input),
	}
}

func (a *cliApprover) Approve(ctx context.Context, _ core.UniversalCommandArgs) (bool, error) {
	// The tool UI renders the command through this same notifier immediately
	// above this prompt — newOperatorToolSurface hands both halves one stream —
	// so the question and its outcomes line up under that block's detail column.
	return a.ask(ctx,
		tools.DetailIndent+"Allow execution? [y/N]: ",
		"\n"+tools.DetailIndent+"No interactive input available; denying by default.\n",
		"\n"+tools.DetailIndent+"Approval prompt cancelled.\n",
	)
}

func (a *cliApprover) ApproveToolRoundLimitReset(ctx context.Context, maxRounds int) (bool, error) {
	return a.ask(ctx,
		fmt.Sprintf("\nTool-call round limit reached (%d). Reset it and continue? [y/N]: ", maxRounds),
		"\nNo interactive input available; keeping the tool-call round limit.\n",
		"\nTool-call round prompt cancelled.\n",
	)
}

// ask shares the yes/no conversation both prompts hold: refuse a question that
// is already cancelled, show it, and read the answer.
func (a *cliApprover) ask(ctx context.Context, prompt, eofNotice, cancelNotice string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	a.notifier.Promptf("%s", prompt)
	return a.readYesNo(ctx, eofNotice, cancelNotice)
}

func (a *cliApprover) readYesNo(ctx context.Context, eofNotice, cancelNotice string) (bool, error) {
	type readResult struct {
		line string
		err  error
	}
	resultChan := make(chan readResult, 1)

	// Read response in a goroutine so we can handle context cancellation
	// without closing process-wide stdin. A cancelled read may remain blocked until
	// input arrives or the one-shot CLI exits; the buffered channel lets it finish.
	go func() {
		line, err := a.reader.ReadString('\n')
		resultChan <- readResult{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		a.notifier.Promptf("%s", cancelNotice)
		return false, ctx.Err()
	case result := <-resultChan:
		if result.err != nil && result.err != io.EOF {
			return false, errors.WrapError("read response", result.err)
		}
		if result.err == io.EOF {
			// An answer and an end of input arrive together: ReadString
			// returns the final unterminated line alongside io.EOF, which is
			// exactly what an operator who types `y` and then Ctrl-D produces.
			// Reading only the error there turns their approval into a denial.
			// The absence of an answer is an empty line, and that keeps the
			// fail-safe denial the prompt's [y/N] default promises.
			if result.line == "" {
				a.notifier.Promptf("%s", eofNotice)
				return false, nil
			}
			// Nothing terminated the answer, so nothing terminated the echo of
			// it either; close the prompt line the way a typed newline would.
			a.notifier.Promptf("\n")
		}
		return isAffirmativeAnswer(result.line), nil
	}
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
