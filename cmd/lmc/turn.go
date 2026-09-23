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
	"os"
)

// turnEnv is what every turn of one process shares: the parsed configuration
// and the operator surfaces, decided once at startup. A single run builds one
// and runs one turn with it.
type turnEnv struct {
	cfg             *config.Config
	notifier        core.Notifier
	toolUI          core.ToolUI
	approver        core.Approver
	logDir          string
	pendingToolMode session.PendingToolMode
	// stdout carries answer bytes alone; stderr carries thinking summaries
	// and the separation that keeps a note off the end of an answer.
	stdout io.Writer
	stderr io.Writer
}

func newTurnEnv(cfg *config.Config, notifier core.Notifier, logDir string) *turnEnv {
	pendingToolMode := session.PendingToolExecute
	if cfg.PrintCurl {
		pendingToolMode = session.PendingToolPreview
	}
	// The command review and the approval prompt share one operator-facing
	// stream, decided once here; the executor and the pending-tools path both
	// receive this same UI.
	toolNotifier, approver := newOperatorToolSurface(notifier)
	return &turnEnv{
		cfg:             cfg,
		notifier:        notifier,
		toolUI:          tools.NewCLIToolUI(toolNotifier),
		approver:        approver,
		logDir:          logDir,
		pendingToolMode: pendingToolMode,
		stdout:          os.Stdout,
		stderr:          os.Stderr,
	}
}

// turnInput is what one turn contributes: the prompt, and whether the turn
// regenerates an assistant message instead of adding a user message.
type turnInput struct {
	text           string
	isRegeneration bool
}

// turnOutcome reports what a turn left in the session. runTurn fills it on
// every exit, errors included, so a caller can tell a request that failed
// before anything was committed from one that failed after the session
// advanced.
type turnOutcome struct {
	// Session is the session the turn committed to. It stays nil until the
	// request plan's writes land, and always with -no-session. Every later
	// save of the turn moves its Path, so afterwards it names the message
	// the next turn continues from.
	Session *session.Session
	// UnsavedAnswer is the error from saving an answer that was already
	// shown. The turn still succeeds, as a single run always has, but the
	// transcript lacks that answer.
	UnsavedAnswer error
	// ConflictForks lists the forks the turn's writes moved to because
	// another writer changed the session while the turn ran.
	ConflictForks []session.ConflictFork
}

// Committed reports whether the turn wrote anything to the session.
func (o turnOutcome) Committed() bool {
	return o.Session != nil
}

// afterTurnCommitForTest runs right after a turn's request plan commits. Tests
// use it to change the session between the commit and the saves that follow.
var afterTurnCommitForTest func(*session.Session)

// runTurn runs one turn against the session: resolve tool calls a resumed
// session left pending, plan the request, refuse a turn with nothing to send,
// then either print the equivalent curl command or send the request and
// handle the response. opts is the turn's own copy of the options; nothing
// here mutates the caller's.
func runTurn(ctx context.Context, env *turnEnv, opts core.RequestOptions, in turnInput) (out turnOutcome, err error) {
	var resumed *session.Session
	defer func() {
		out.ConflictForks = collectConflictForks(resumed, out.Session)
		for _, fork := range out.ConflictForks {
			env.notifier.Infof("Session %s changed while this turn ran; the turn continued in session %s", fork.From, fork.To)
		}
	}()

	var plan *session.RequestPlan
	resolvedPending := false
	if resolvesPendingTools(env, opts, in) {
		// Resolving pending calls is a step of its own, before the request is
		// prepared, so a failed request is retried without running a tool.
		resumed, err = session.OpenSession(ctx, opts.Resume)
		if err != nil {
			return out, err
		}
		var resolution session.PendingResolution
		resolution, err = session.ResolvePendingToolCalls(ctx, resumed, opts, logger.From(ctx), env.notifier, env.toolUI, env.approver)
		if resolution.Committed {
			out.Session = resumed
		}
		resolvedPending = resolution.ResultsAwaitReply
		if err != nil {
			return out, err
		}
		plan, err = session.PrepareRequestAt(ctx, opts, env.notifier, env.toolUI, resumed, in.text, in.isRegeneration, env.pendingToolMode)
		if err != nil {
			return out, err
		}
	} else {
		plan, err = prepareSessionRequestPlan(ctx, env.cfg, opts, env.notifier, env.toolUI, in.text, in.isRegeneration, env.pendingToolMode)
		if err != nil {
			return out, err
		}
	}
	hasPendingTools := resolvedPending || (plan != nil && plan.HasPendingTools)
	if !in.isRegeneration && in.text == "" && len(opts.Images) == 0 && !hasPendingTools {
		return out, errors.WrapError("validate input", stdErrors.New("input cannot be empty"))
	}

	if env.cfg.PrintCurl {
		rb, err := buildHTTPRequest(ctx, env.cfg, opts, plan, in.text)
		if err != nil {
			return out, err
		}
		_, _ = fmt.Fprintln(env.stdout, renderCurlCommand(rb.Request, rb.Body))
		return out, nil
	}

	executed, err := executeRequest(ctx, env, opts, in.text, plan)
	if executed.Session != nil {
		out.Session = executed.Session
	}
	out.UnsavedAnswer = executed.UnsavedAnswer
	return out, err
}

// resolvesPendingTools reports whether a turn resumes a session whose
// pending tool calls it must resolve before preparing its request. -print-curl
// previews them instead, and a resume by message reference branches.
func resolvesPendingTools(env *turnEnv, opts core.RequestOptions, in turnInput) bool {
	return !env.cfg.NoSession &&
		env.pendingToolMode == session.PendingToolExecute &&
		!in.isRegeneration &&
		session.IsSessionResume(opts.Resume)
}

// collectConflictForks gathers the conflict forks of a turn's session values.
// A system prompt fork replaces the value pending resolution worked with, so
// both are read.
func collectConflictForks(values ...*session.Session) []session.ConflictFork {
	var forks []session.ConflictFork
	seen := make(map[*session.Session]bool)
	for _, value := range values {
		if value == nil || seen[value] {
			continue
		}
		seen[value] = true
		forks = append(forks, value.ConflictForks...)
	}
	return forks
}

// branchRegenerates reports whether -branch names an assistant message, which
// regenerates that answer and sends no new user turn.
func branchRegenerates(branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	isAssistant, err := session.IsAssistantMessage(branch)
	if err != nil {
		return false, errors.WrapError("check branch message type", err)
	}
	return isAssistant, nil
}
