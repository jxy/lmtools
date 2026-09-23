package session

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"time"
)

// requestPreparer builds session request plans and defers session writes until
// RequestPlan.Commit.
type requestPreparer struct {
	cfg      core.RequestOptions
	notifier core.Notifier
	// toolUI reviews and displays pending tool execution. It is injected from
	// the edge; a nil UI is valid only when pending tools cannot require
	// interactive approval.
	toolUI core.ToolUI
}

// PrepareRequest builds request messages without committing new session state.
// It runs no tool, however many times it is called: pending tool calls at a
// resumed head are resolved beforehand by ResolvePendingToolCalls, or
// previewed with placeholders for -print-curl.
func PrepareRequest(ctx context.Context, cfg core.RequestOptions, notifier core.Notifier, toolUI core.ToolUI, inputStr string, isRegeneration bool, pendingTools PendingToolMode) (*RequestPlan, error) {
	preparer := requestPreparer{cfg: cfg, notifier: notifier, toolUI: toolUI}
	return preparer.PrepareRequest(ctx, inputStr, isRegeneration, pendingTools)
}

// RequestPlan contains the messages for a provider request plus the deferred
// session writes that should run only after the provider response succeeds.
type RequestPlan struct {
	Messages        []core.TypedMessage
	HasPendingTools bool
	commit          func(context.Context) (*Session, error)
	committed       bool
}

// Commit applies the session writes planned by PrepareRequest.
func (p *RequestPlan) Commit(ctx context.Context) (*Session, error) {
	if p == nil {
		return nil, nil
	}
	if p.committed {
		return nil, fmt.Errorf("request plan already committed")
	}
	if p.commit == nil {
		p.committed = true
		return nil, nil
	}
	sess, err := p.commit(ctx)
	if err != nil {
		// A commit that fails part way keeps what it wrote, because another
		// run may already have adopted a directory it created. The session
		// comes back beside the error whenever something stays on disk, so
		// a caller can report it and continue there.
		return sess, err
	}
	p.committed = true
	return sess, nil
}

// PrepareRequest builds request messages without committing new session state.
func (c *requestPreparer) PrepareRequest(ctx context.Context, inputStr string, isRegeneration bool, pendingTools PendingToolMode) (*RequestPlan, error) {
	if resume := c.cfg.Resume; resume != "" {
		if !IsSessionResume(resume) {
			return c.prepareMessageResumeRequest(ctx, resume, inputStr, isRegeneration)
		}
		return c.prepareSessionResumeRequest(ctx, resume, inputStr, isRegeneration, pendingTools)
	}
	if branch := c.cfg.Branch; branch != "" {
		return c.prepareBranchRequest(ctx, branch, inputStr, isRegeneration)
	}
	return c.prepareNewRequest(inputStr, isRegeneration), nil
}

// IsSessionResume reports whether a -resume value names a session, which
// continues from the session's head, rather than a message, which branches
// at that message.
func IsSessionResume(resume string) bool {
	if resume == "" {
		return false
	}
	if IsMessageReference(resume) {
		if _, messageID := ParseMessageID(resume); messageID != "" {
			return false
		}
	}
	return true
}

func (c *requestPreparer) prepareNewRequest(inputStr string, isRegeneration bool) *RequestPlan {
	messages := []core.TypedMessage{}
	if system := c.cfg.GetEffectiveSystem(); system != "" {
		messages = append(messages, core.NewTextMessage(string(core.RoleSystem), system))
	}
	messages = appendPlannedUserMessage(messages, inputStr, c.cfg.Images, isRegeneration)

	return &RequestPlan{
		Messages: messages,
		commit: func(ctx context.Context) (*Session, error) {
			sess, head, err := DefaultManager().createSession(c.cfg.GetEffectiveSystem(), logger.From(ctx))
			if err != nil {
				return nil, errors.WrapError("prepare session", err)
			}
			// A new session ends at the system message it wrote, or holds
			// nothing.
			sess.Head = &head
			created := *sess
			if err := c.maybeSaveUserInput(ctx, sess, inputStr, isRegeneration); err != nil {
				return failCommit(err, sess, 0, created)
			}
			return sess, nil
		},
	}
}

func (c *requestPreparer) prepareSessionResumeRequest(ctx context.Context, resumeID, inputStr string, isRegeneration bool, pendingTools PendingToolMode) (*RequestPlan, error) {
	sess, err := loadSessionWithRetry(resumeID)
	if err != nil {
		return nil, errors.WrapError("load session", fmt.Errorf("session or message not found: %s", resumeID))
	}
	return c.prepareSessionAt(ctx, sess, nil, inputStr, isRegeneration, pendingTools)
}

// PrepareRequestAt prepares a request that continues sess from the head it
// pins, which a nil head pins at the lineage's current end. Messages another
// writer appended past the head are left out of the request, and the plan's
// writes check that the session still ends there. This is how a caller that
// resolved pending tool calls, or ran a turn before, prepares the next
// request against the history it knows.
func PrepareRequestAt(ctx context.Context, cfg core.RequestOptions, notifier core.Notifier, toolUI core.ToolUI, sess *Session, inputStr string, isRegeneration bool, pendingTools PendingToolMode) (*RequestPlan, error) {
	preparer := requestPreparer{cfg: cfg, notifier: notifier, toolUI: toolUI}
	return preparer.prepareSessionAt(ctx, sess, sess.Head, inputStr, isRegeneration, pendingTools)
}

// prepareSessionAt builds the plan for continuing a session. The head and the
// messages come from one lineage scan, read with every sidecar under one hold
// of the tree's lock, and the head is pinned on sess for the plan's writes.
// Preparation runs no tool: pending calls at the head are previewed with
// placeholders for -print-curl, and must otherwise have been resolved first
// by ResolvePendingToolCalls.
func (c *requestPreparer) prepareSessionAt(ctx context.Context, sess *Session, head *MessageRef, inputStr string, isRegeneration bool, pendingTools PendingToolMode) (*RequestPlan, error) {
	var (
		pending  pendingToolResolution
		decision ResumeForkDecision
		messages []core.TypedMessage
	)
	err := withTreeLock(sess.Path, func() error {
		refs, pinned, err := lineageThroughHeadLocked(DefaultManager(), sess.Path, head)
		if err != nil {
			return errors.WrapError("build session messages", err)
		}
		sess.Head = pinned

		if pending, err = c.previewPendingTools(ctx, refs, isRegeneration, pendingTools); err != nil {
			return err
		}

		sessionSystemMsg, err := GetSystemMessage(sess.Path)
		if err != nil {
			return errors.WrapError("get session system message", err)
		}
		decision = DecideResumeFork(sessionSystemMsg, c.cfg)

		if messages, err = buildTypedMessagesFromLineageRefs(ctx, refs); err != nil {
			return errors.WrapError("build session messages", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	messages = applyPlannedSystemDecision(messages, decision)
	messages = appendPendingToolPreviewResults(messages, pending)
	messages = appendPlannedUserMessage(messages, inputStr, c.cfg.Images, isRegeneration)

	return &RequestPlan{
		Messages:        messages,
		HasPendingTools: pending.HasPending,
		commit: func(ctx context.Context) (*Session, error) {
			committed, forked, err := c.commitResumeSystemDecision(ctx, sess, decision)
			if err != nil {
				return nil, err
			}
			var created []Session
			if forked {
				created = append(created, *committed)
			}
			forkID := GetSessionID(committed.Path)
			forksBefore := len(committed.ConflictForks)
			if err := c.maybeSaveUserInput(ctx, committed, inputStr, isRegeneration); err != nil {
				return failCommit(err, committed, forksBefore, created...)
			}
			if forked {
				c.notifier.Infof("Forked session due to system prompt change: %s", forkID)
			}
			return committed, nil
		},
	}, nil
}

func (c *requestPreparer) prepareMessageResumeRequest(ctx context.Context, resumeRef, inputStr string, isRegeneration bool) (*RequestPlan, error) {
	messages, _, head, err := buildBranchRequestMessages(ctx, resumeRef)
	if err != nil {
		return nil, err
	}

	decision := DecideResumeFork(nil, c.cfg)
	messages = applyPlannedSystemDecision(messages, decision)
	messages = appendPlannedUserMessage(messages, inputStr, c.cfg.Images, isRegeneration)

	return &RequestPlan{
		Messages: messages,
		commit: func(ctx context.Context) (*Session, error) {
			branch, err := c.commitBranch(ctx, resumeRef, "create branch", head)
			if err != nil {
				return nil, err
			}
			created := []Session{*branch}
			sess, forked, err := c.commitResumeSystemDecision(ctx, branch, decision)
			if err != nil {
				return failCommit(err, nil, 0, created...)
			}
			if forked {
				created = append(created, *sess)
			}
			forkID := GetSessionID(sess.Path)
			forksBefore := len(sess.ConflictForks)
			if err := c.maybeSaveUserInput(ctx, sess, inputStr, isRegeneration); err != nil {
				return failCommit(err, sess, forksBefore, created...)
			}
			c.notifier.Infof("Branching from message %s", resumeRef)
			if forked {
				c.notifier.Infof("Forked session due to system prompt change: %s", forkID)
			}
			return sess, nil
		},
	}, nil
}

func (c *requestPreparer) prepareBranchRequest(ctx context.Context, branchRef, inputStr string, isRegeneration bool) (*RequestPlan, error) {
	messages, _, head, err := buildBranchRequestMessages(ctx, branchRef)
	if err != nil {
		return nil, err
	}
	messages = appendPlannedUserMessage(messages, inputStr, c.cfg.Images, isRegeneration)

	return &RequestPlan{
		Messages: messages,
		commit: func(ctx context.Context) (*Session, error) {
			sess, err := c.commitBranch(ctx, branchRef, "create branch", head)
			if err != nil {
				return nil, err
			}
			created := *sess
			if err := c.maybeSaveUserInput(ctx, sess, inputStr, isRegeneration); err != nil {
				return failCommit(err, sess, 0, created)
			}
			return sess, nil
		},
	}, nil
}

// commitBranch creates the branch a plan was prepared for and pins head, the
// last message of the lineage the plan's request carries, on the branch.
func (c *requestPreparer) commitBranch(ctx context.Context, branchRef, wrap string, head *MessageRef) (*Session, error) {
	sessionPath, messageID := ParseMessageID(branchRef)
	siblingPath, err := CreateSibling(ctx, sessionPath, messageID)
	if err != nil {
		return nil, errors.WrapError(wrap, err)
	}
	sess, err := LoadSession(siblingPath)
	if err != nil {
		return failCommit(err, nil, 0, Session{Path: siblingPath, Head: head})
	}
	sess.Head = head
	return sess, nil
}

// commitResumeSystemDecision forks the session when the plan decided the
// system prompt changed, and reports whether it did. The caller notes the fork
// once the rest of the commit has succeeded, since a failure removes it.
func (c *requestPreparer) commitResumeSystemDecision(ctx context.Context, sess *Session, decision ResumeForkDecision) (*Session, bool, error) {
	if !decision.ShouldFork {
		return sess, false, nil
	}
	return MaybeForkForSystem(ctx, sess, decision.NewSystem)
}

// appendPlannedUserMessage stages the user turn the provider will answer. It
// is built by core.UserMessageBlocks, the same constructor saveUserMessage
// commits, so the staged request and the persisted message cannot differ.
func appendPlannedUserMessage(messages []core.TypedMessage, inputStr string, images []core.ImageBlock, isRegeneration bool) []core.TypedMessage {
	if !shouldAppendUserInput(inputStr, images, isRegeneration) {
		return messages
	}
	return append(messages, core.NewUserMessage(inputStr, images))
}

// shouldAppendUserInput is true when the run contributes a user turn: a
// regeneration re-asks for the previous answer and sends none, and otherwise
// either a prompt or an attached image is enough to make one.
func shouldAppendUserInput(inputStr string, images []core.ImageBlock, isRegeneration bool) bool {
	return !isRegeneration && (inputStr != "" || len(images) > 0)
}

func applyPlannedSystemDecision(messages []core.TypedMessage, decision ResumeForkDecision) []core.TypedMessage {
	if !decision.ShouldFork {
		return messages
	}

	rest := messages
	if len(rest) > 0 && rest[0].Role == string(core.RoleSystem) {
		rest = rest[1:]
	}

	out := make([]core.TypedMessage, 0, len(rest)+1)
	if decision.NewSystem != "" {
		out = append(out, core.NewTextMessage(string(core.RoleSystem), decision.NewSystem))
	}
	out = append(out, rest...)
	return out
}

// appendPendingToolPreviewResults stages the tool-results message that
// RequestPlan.Commit later persists via SaveToolResults. It includes
// AdditionalText (truncation notes) so the request the provider answers
// matches the message the session records — the in-loop follow-up path
// already sends the note this way.
func appendPendingToolPreviewResults(messages []core.TypedMessage, pending pendingToolResolution) []core.TypedMessage {
	if len(pending.PreviewResults) == 0 {
		return messages
	}

	toolNamesByID := make(map[string]string, len(pending.PreviewCalls))
	for _, call := range pending.PreviewCalls {
		if call.ID != "" {
			toolNamesByID[call.ID] = call.Name
		}
	}

	return append(messages, core.TypedMessage{
		Role:   string(core.RoleUser),
		Blocks: core.ToolResultsMessageBlocks(pending.PreviewResults, pending.AdditionalText, toolNamesByID),
	})
}

// previewPendingTools handles tool calls pending at the head a plan was
// prepared against. Preview mode substitutes placeholder results for
// -print-curl. Execute mode expects them resolved already, because
// preparation never runs a tool, and reports a pending call as an error.
func (c *requestPreparer) previewPendingTools(ctx context.Context, refs []lineageMessageRef, isRegeneration bool, mode PendingToolMode) (pendingToolResolution, error) {
	if c.cfg.Resume == "" || isRegeneration || mode == PendingToolSkip {
		return pendingToolResolution{}, nil
	}
	calls, _, err := pendingToolCallsWithRef(ctx, refs)
	if err != nil {
		logger.From(ctx).Debugf("Failed to check pending tools: %v", err)
		return pendingToolResolution{}, nil
	}
	if len(calls) == 0 {
		return pendingToolResolution{}, nil
	}
	if !c.cfg.ToolEnabled {
		return pendingToolResolution{HasPending: true}, errors.WrapError("execute pending tools", fmt.Errorf("pending tool calls require -tool to continue"))
	}
	if mode != PendingToolPreview {
		return pendingToolResolution{HasPending: true}, errors.WrapError("prepare request", fmt.Errorf("%d pending tool call(s) must be resolved before the request is prepared", len(calls)))
	}
	return pendingToolResolution{
		HasPending:     true,
		PreviewCalls:   calls,
		PreviewResults: placeholderPendingToolResults(calls),
	}, nil
}

func (c *requestPreparer) maybeSaveUserInput(ctx context.Context, sess *Session, inputStr string, isRegeneration bool) error {
	if !shouldAppendUserInput(inputStr, c.cfg.Images, isRegeneration) {
		return nil
	}
	return c.saveUserMessage(ctx, sess, inputStr)
}

// loadSessionWithRetry attempts to load a session with retries for concurrent scenarios
func loadSessionWithRetry(sessionID string) (*Session, error) {
	const maxRetries = 10
	const retryDelay = 50 * time.Millisecond
	const finalDelay = 100 * time.Millisecond

	var sess *Session
	var loadErr error

	// Try multiple times with short delays
	for i := 0; i < maxRetries; i++ {
		sess, loadErr = LoadSession(sessionID)
		if loadErr == nil {
			return sess, nil
		}

		// If it's not a "not found" error, fail immediately
		if !stdErrors.Is(loadErr, os.ErrNotExist) {
			return nil, errors.WrapError("load session "+sessionID, loadErr)
		}

		// Wait before retry (except on last iteration)
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}

	// One final attempt after a longer delay
	time.Sleep(finalDelay)
	sess, loadErr = LoadSession(sessionID)
	if loadErr == nil {
		return sess, nil
	}

	return nil, loadErr
}

// beforeUserMessageSaveForTest runs before a plan commit saves its user
// message. An error it returns fails the save, the way an I/O failure would.
var beforeUserMessageSaveForTest func(*Session) error

// saveUserMessage saves the user turn to the session. The prompt goes to the
// message's .txt as before; the explicit blocks go to its .blocks.json, which
// is where an attached image lives. The image is stored inline, as the data
// URL the request carried, because the session is what replays the turn on
// resume and no provider keeps image bytes between requests.
func (c *requestPreparer) saveUserMessage(ctx context.Context, sess *Session, inputStr string) error {
	blocks := core.UserMessageBlocks(inputStr, c.cfg.Images)
	if len(blocks) == 0 {
		return nil
	}
	if beforeUserMessageSaveForTest != nil {
		if err := beforeUserMessageSaveForTest(sess); err != nil {
			return errors.WrapError("save user message", err)
		}
	}

	userMsg := Message{
		Role:      core.RoleUser,
		Content:   inputStr,
		Timestamp: time.Now(),
	}

	result, err := AppendMessageWithBlocks(ctx, sess, userMsg, nil, nil, blocks)
	if err != nil {
		return errors.WrapError("save user message", err)
	}
	path := result.Path

	// Update session path if a sibling was created
	if path != sess.Path {
		sess.Path = path
		// Log that we're using a sibling
		c.notifier.Infof("Using sibling branch %s", GetSessionID(path))
	}

	return nil
}
