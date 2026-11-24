package main

import (
	"context"
	stdErrors "errors"
	"fmt"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"lmtools/internal/session"
	"lmtools/internal/ui/tools"
	"lmtools/internal/workingcontext"
	"os"
)

// turnEnv is what every turn of one process shares: the parsed configuration
// and the operator surfaces, decided once at startup. A single run builds one
// and runs one turn with it.
type turnEnv struct {
	window *workingcontext.Engine
	cfg    *config.Config
	// store is the sessions directory, nil with -no-session.
	store           *session.Store
	notifier        core.Notifier
	toolUI          core.ToolUI
	approver        core.Approver
	logDir          string
	pendingToolMode session.PendingToolMode
	// stdout carries answer bytes alone; stderr carries thinking summaries
	// and the separation that keeps a note off the end of an answer.
	stdout io.Writer
	stderr io.Writer
	// answersOnScreen reports that answers appear where stderr's notes do,
	// so a note must be kept off the end of an answer. A single run whose
	// stdout is another file than its stderr, and a loop whose stdout is not
	// a terminal, send answers elsewhere and separate them there.
	answersOnScreen bool
	// answerLabel identifies provider text on an interactive terminal.
	answerLabel string
	progress    *turnProgress
	// quoteThinking quotes and wraps visible thinking for a terminal, at
	// columns when that is known.
	quoteThinking bool
	columns       func() int
	// served is the model the provider last said answered.
	served string
	// hiddenThinkingNoted records that the run has said a provider returned
	// no visible thinking for its reasoning; every turn's presenter shares
	// it, so the note is given once a run.
	hiddenThinkingNoted bool
}

func newTurnEnv(cfg *config.Config, notifier core.Notifier, logDir string, input *inputOwner) *turnEnv {
	pendingToolMode := session.PendingToolExecute
	if cfg.PrintCurl {
		pendingToolMode = session.PendingToolPreview
	}
	// The command review and the approval prompt share one operator-facing
	// stream, decided once here; the executor and the pending-tools path both
	// receive this same UI.
	toolNotifier, approver := newOperatorToolSurface(notifier, input)
	// On a terminal, thinking is quoted and wrapped, and the tool transcript
	// wraps its lines, within the terminal's width.
	columns := fileColumns(os.Stderr)
	env := &turnEnv{
		cfg:             cfg,
		store:           openStore(cfg),
		notifier:        notifier,
		toolUI:          tools.NewCLIToolUIWithOptions(toolNotifier, tools.Options{Columns: columns}),
		approver:        approver,
		logDir:          logDir,
		pendingToolMode: pendingToolMode,
		stdout:          os.Stdout,
		stderr:          os.Stderr,
		answersOnScreen: true,
		quoteThinking:   columns != nil,
		columns:         columns,
	}
	if !sameFile(os.Stdout, os.Stderr) {
		env.stdout = &separatedAnswers{out: os.Stdout}
		env.answersOnScreen = false
	}
	return env
}

// sameFile reports whether a and b are one file, such as the terminal both
// write to, or the file or pipe a shell's 2>&1 gives both.
func sameFile(a, b *os.File) bool {
	aInfo, err := a.Stat()
	if err != nil {
		return false
	}
	bInfo, err := b.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}

// turnInput is what one turn contributes: the prompt, empty for a turn that
// asks the model to answer the end of the history.
type turnInput struct {
	text string
	// from is the session a loop continues: the session an earlier turn
	// wrote to, whose head is where that turn left the conversation. Nil
	// before anything was written, when the run's start point is where the
	// turn continues.
	from *session.Session
}

// turnOutcome reports what a turn left in the session. runTurn fills it on
// every exit, errors included, so a caller can tell a request that failed
// before anything was written from one that failed after the session
// advanced.
type turnOutcome struct {
	// Session is the session the turn wrote to, at the last message it
	// saved. It stays nil until something is written, and always with
	// -no-session.
	Session *session.Session
	// UnsavedAnswer is the error from saving an answer that was already
	// shown. The turn still succeeds, as a single run always has, but the
	// transcript lacks that answer.
	UnsavedAnswer error
	// PlanCommitted reports that the request plan's writes landed, the
	// turn's user message and its images among them.
	PlanCommitted bool
	PartialAnswer bool
}

// Committed reports whether the turn wrote anything to the session.
func (o turnOutcome) Committed() bool {
	return o.Session != nil
}

// afterTurnCommitForTest runs right after a turn's request plan commits.
var afterTurnCommitForTest func(*session.Session)

// runTurn runs one turn: resolve the tool calls pending where it starts,
// saving their results, then prepare the request and either print the
// equivalent curl command or send it and handle the response. opts is the
// turn's own copy of the options; nothing here mutates the caller's.
func runTurn(ctx context.Context, env *turnEnv, opts core.RequestOptions, in turnInput) (out turnOutcome, err error) {
	env.window = newContextWindow(env, opts)
	if err := core.ValidateDeveloperTurn(opts, in.text); err != nil {
		return out, err
	}
	if env.store == nil {
		if in.text == "" && len(opts.Images) == 0 {
			return out, errors.WrapError("validate input", stdErrors.New("input cannot be empty"))
		}
		opts = config.UseSystemPrompt(opts, env.cfg.Settings)
		return sendTurn(ctx, env, opts, in.text, nil)
	}

	start := env.cfg.Start
	// resumed is the session the turn continues, which keeps the history it
	// reads, so resolving pending calls and preparing the request read it
	// once.
	var resumed *session.Session
	if in.from != nil {
		copied := *in.from
		resumed = &copied
		start = resumed.Ref()
	}
	if start.Session == "" && in.text == "" && len(opts.Images) == 0 {
		return out, errors.WrapError("validate input", stdErrors.New("input cannot be empty"))
	}
	// Resolving pending calls is a step of its own, before the request is
	// prepared, so a failed request is retried without running a tool.
	if env.pendingToolMode == session.PendingToolExecute && start.Session != "" {
		if resumed == nil {
			if resumed, err = env.store.Session(start); err != nil {
				return out, err
			}
		}
		resolution, err := resumed.ResolvePending(ctx, opts, logger.From(ctx), env.notifier, env.toolUI, env.approver, func(calls []core.ToolCall) error {
			return bindPendingHistory(ctx, env, opts, resumed, calls)
		})
		if resolution.Committed {
			out.Session = resumed
			start = resumed.Ref()
		}
		if err != nil {
			return out, err
		}
	}
	turn := session.Turn{Prompt: in.text, Images: opts.Images, Developer: opts.Developer}
	var plan *session.Plan
	if resumed != nil {
		plan, err = resumed.Prepare(ctx, env.cfg.Change, turn, opts, env.pendingToolMode)
	} else {
		plan, err = env.store.Prepare(ctx, start, env.cfg.Change, turn, opts, env.pendingToolMode)
	}
	if err != nil {
		return out, err
	}
	opts = config.UseSystemPrompt(opts, plan.Settings)
	executed, err := sendTurn(ctx, env, opts, in.text, plan)
	if executed.Session != nil {
		out.Session = executed.Session
	}
	out.UnsavedAnswer = executed.UnsavedAnswer
	out.PlanCommitted = executed.PlanCommitted
	out.PartialAnswer = executed.PartialAnswer
	return out, err
}

// sendTurn prints the curl command of a prepared request with -print-curl,
// and sends it otherwise.
func sendTurn(ctx context.Context, env *turnEnv, opts core.RequestOptions, input string, plan *session.Plan) (turnOutcome, error) {
	if env.cfg.PrintCurl {
		rb, err := buildTurnRequest(ctx, env, opts, plan, input)
		if err != nil {
			return turnOutcome{}, err
		}
		_, _ = fmt.Fprintln(env.stdout, renderCurlCommand(rb.Request, rb.Body))
		return turnOutcome{}, nil
	}
	return executeRequest(ctx, env, opts, input, plan)
}

// bindPendingHistory gives the history tools the history of the session a
// pending history call was made in, before the call runs.
func bindPendingHistory(ctx context.Context, env *turnEnv, opts core.RequestOptions, resumed *session.Session, calls []core.ToolCall) error {
	if env.window == nil {
		return nil
	}
	for _, call := range calls {
		if (call.MCPServer == "" || call.MCPServer == "history") && workingcontext.IsHistoryTool(call.Name) {
			view, err := resumed.View(opts)
			if err != nil {
				return err
			}
			return env.window.BindExisting(ctx, view.Messages, view.Refs, view.Transcript)
		}
	}
	return nil
}

// openStore returns the run's sessions directory, or nil with -no-session.
func openStore(cfg *config.Config) *session.Store {
	if cfg.NoSession {
		return nil
	}
	return cfg.Store()
}
