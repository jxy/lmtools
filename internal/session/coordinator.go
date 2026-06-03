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
}

// Coordinator is kept as a compatibility alias for older tests and callers.
// New code should call PrepareRequest directly.
type Coordinator = requestPreparer

// NewCoordinator creates a new session request preparer.
// Deprecated: use PrepareRequest.
func NewCoordinator(cfg core.RequestOptions, notifier core.Notifier) *Coordinator {
	return &requestPreparer{
		cfg:      cfg,
		notifier: notifier,
	}
}

// PrepareRequest builds request messages without committing new session state.
func PrepareRequest(ctx context.Context, cfg core.RequestOptions, notifier core.Notifier, inputStr string, isRegeneration bool, approver core.Approver, pendingTools PendingToolMode) (*RequestPlan, error) {
	preparer := requestPreparer{cfg: cfg, notifier: notifier}
	return preparer.PrepareRequest(ctx, inputStr, isRegeneration, approver, pendingTools)
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
		return nil, err
	}
	p.committed = true
	return sess, nil
}

// PrepareRequest builds request messages without committing new session state.
func (c *requestPreparer) PrepareRequest(ctx context.Context, inputStr string, isRegeneration bool, approver core.Approver, pendingTools PendingToolMode) (*RequestPlan, error) {
	if resume := c.cfg.Resume; resume != "" {
		if IsMessageReference(resume) {
			_, messageID := ParseMessageID(resume)
			if messageID != "" {
				return c.prepareMessageResumeRequest(ctx, resume, inputStr, isRegeneration)
			}
		}
		return c.prepareSessionResumeRequest(ctx, resume, inputStr, isRegeneration, approver, pendingTools)
	}
	if branch := c.cfg.Branch; branch != "" {
		return c.prepareBranchRequest(ctx, branch, inputStr, isRegeneration)
	}
	return c.prepareNewRequest(inputStr, isRegeneration), nil
}

func (c *requestPreparer) prepareNewRequest(inputStr string, isRegeneration bool) *RequestPlan {
	messages := []core.TypedMessage{}
	if system := c.cfg.GetEffectiveSystem(); system != "" {
		messages = append(messages, core.NewTextMessage(string(core.RoleSystem), system))
	}
	messages = appendPlannedUserMessage(messages, inputStr, isRegeneration)

	return &RequestPlan{
		Messages: messages,
		commit: func(ctx context.Context) (*Session, error) {
			sess, err := CreateSession(c.cfg.GetEffectiveSystem(), logger.From(ctx))
			if err != nil {
				return nil, errors.WrapError("prepare session", err)
			}
			if err := c.maybeSaveUserInput(ctx, sess, inputStr, isRegeneration); err != nil {
				return nil, err
			}
			return sess, nil
		},
	}
}

func (c *requestPreparer) prepareSessionResumeRequest(ctx context.Context, resumeID, inputStr string, isRegeneration bool, approver core.Approver, pendingTools PendingToolMode) (*RequestPlan, error) {
	sess, err := loadSessionWithRetry(resumeID)
	if err != nil {
		return nil, errors.WrapError("load session", fmt.Errorf("session or message not found: %s", resumeID))
	}

	pending, err := c.maybeResolvePendingTools(ctx, sess, isRegeneration, approver, pendingTools)
	if err != nil {
		return nil, err
	}

	sessionSystemMsg, err := GetSystemMessage(sess.Path)
	if err != nil {
		return nil, errors.WrapError("get session system message", err)
	}
	decision := DecideResumeFork(sessionSystemMsg, c.cfg)

	messages, err := BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		return nil, errors.WrapError("build session messages", err)
	}
	messages = applyPlannedSystemDecision(messages, decision)
	messages = appendPendingToolPreviewResults(messages, pending.PreviewCalls, pending.PreviewResults)
	messages = appendPlannedUserMessage(messages, inputStr, isRegeneration)

	return &RequestPlan{
		Messages:        messages,
		HasPendingTools: pending.HasPending,
		commit: func(ctx context.Context) (*Session, error) {
			committed, err := c.commitResumeSystemDecision(ctx, sess, decision)
			if err != nil {
				return nil, err
			}
			if err := commitPendingToolResults(ctx, committed, pending, logger.From(ctx)); err != nil {
				return nil, err
			}
			if err := c.maybeSaveUserInput(ctx, committed, inputStr, isRegeneration); err != nil {
				return nil, err
			}
			return committed, nil
		},
	}, nil
}

func (c *requestPreparer) prepareMessageResumeRequest(ctx context.Context, resumeRef, inputStr string, isRegeneration bool) (*RequestPlan, error) {
	messages, _, err := buildBranchRequestMessages(ctx, resumeRef)
	if err != nil {
		return nil, err
	}

	decision := DecideResumeFork(nil, c.cfg)
	messages = applyPlannedSystemDecision(messages, decision)
	messages = appendPlannedUserMessage(messages, inputStr, isRegeneration)

	return &RequestPlan{
		Messages: messages,
		commit: func(ctx context.Context) (*Session, error) {
			sess, err := c.commitBranch(ctx, resumeRef, "create branch")
			if err != nil {
				return nil, err
			}
			c.notifier.Infof("Branching from message %s", resumeRef)
			sess, err = c.commitResumeSystemDecision(ctx, sess, decision)
			if err != nil {
				return nil, err
			}
			if err := c.maybeSaveUserInput(ctx, sess, inputStr, isRegeneration); err != nil {
				return nil, err
			}
			return sess, nil
		},
	}, nil
}

func (c *requestPreparer) prepareBranchRequest(ctx context.Context, branchRef, inputStr string, isRegeneration bool) (*RequestPlan, error) {
	messages, _, err := buildBranchRequestMessages(ctx, branchRef)
	if err != nil {
		return nil, err
	}
	messages = appendPlannedUserMessage(messages, inputStr, isRegeneration)

	return &RequestPlan{
		Messages: messages,
		commit: func(ctx context.Context) (*Session, error) {
			sess, err := c.commitBranch(ctx, branchRef, "create branch")
			if err != nil {
				return nil, err
			}
			if err := c.maybeSaveUserInput(ctx, sess, inputStr, isRegeneration); err != nil {
				return nil, err
			}
			return sess, nil
		},
	}, nil
}

func (c *requestPreparer) commitBranch(ctx context.Context, branchRef, wrap string) (*Session, error) {
	sessionPath, messageID := ParseMessageID(branchRef)
	siblingPath, err := CreateSibling(ctx, sessionPath, messageID)
	if err != nil {
		return nil, errors.WrapError(wrap, err)
	}
	return LoadSession(siblingPath)
}

func (c *requestPreparer) commitResumeSystemDecision(ctx context.Context, sess *Session, decision ResumeForkDecision) (*Session, error) {
	if !decision.ShouldFork {
		return sess, nil
	}

	forkedSess, forked, err := MaybeForkForSystem(ctx, sess, decision.NewSystem)
	if err != nil {
		return nil, err
	}
	if forked {
		c.notifier.Infof("Forked session due to system prompt change: %s", GetSessionID(forkedSess.Path))
	}
	return forkedSess, nil
}

func appendPlannedUserMessage(messages []core.TypedMessage, inputStr string, isRegeneration bool) []core.TypedMessage {
	if !shouldAppendUserInput(inputStr, isRegeneration) {
		return messages
	}
	return append(messages, core.NewTextMessage(string(core.RoleUser), inputStr))
}

func shouldAppendUserInput(inputStr string, isRegeneration bool) bool {
	return !isRegeneration && inputStr != ""
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

func appendPendingToolPreviewResults(messages []core.TypedMessage, calls []core.ToolCall, results []core.ToolResult) []core.TypedMessage {
	if len(results) == 0 {
		return messages
	}

	toolNamesByID := make(map[string]string, len(calls))
	for _, call := range calls {
		if call.ID != "" {
			toolNamesByID[call.ID] = call.Name
		}
	}

	blocks := make([]core.Block, 0, len(results))
	for _, result := range results {
		blocks = append(blocks, core.ToolResultBlockFromResult(result, toolNamesByID[result.ID]))
	}
	return append(messages, core.TypedMessage{
		Role:   string(core.RoleUser),
		Blocks: blocks,
	})
}

func (c *requestPreparer) maybeResolvePendingTools(ctx context.Context, sess *Session, isRegeneration bool, approver core.Approver, mode PendingToolMode) (pendingToolResolution, error) {
	if sess == nil || c.cfg.Resume == "" || isRegeneration || mode == PendingToolSkip {
		return pendingToolResolution{}, nil
	}
	return resolvePendingTools(ctx, sess, c.cfg, logger.From(ctx), c.notifier, approver, mode)
}

func (c *requestPreparer) maybeSaveUserInput(ctx context.Context, sess *Session, inputStr string, isRegeneration bool) error {
	if !shouldAppendUserInput(inputStr, isRegeneration) {
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

// saveUserMessage saves the user input to the session
func (c *requestPreparer) saveUserMessage(ctx context.Context, sess *Session, inputStr string) error {
	if inputStr == "" {
		return nil
	}

	userMsg := Message{
		Role:      core.RoleUser,
		Content:   inputStr,
		Timestamp: time.Now(),
	}

	result, err := AppendMessageWithToolInteraction(ctx, sess, userMsg, nil, nil)
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
