package session

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"strings"
	"time"
)

// Coordinator orchestrates session operations including creation, loading,
// forking, and pending tools execution. It encapsulates the complex session
// management logic that was previously in cmd/lmc/main.go.
type Coordinator struct {
	cfg      core.RequestOptions
	notifier core.Notifier
}

// NewCoordinator creates a new session coordinator
func NewCoordinator(cfg core.RequestOptions, notifier core.Notifier) *Coordinator {
	return &Coordinator{
		cfg:      cfg,
		notifier: notifier,
	}
}

// PrepareSessionResult contains the result of preparing a session
type PrepareSessionResult struct {
	Session         *Session
	ExecutedPending bool // True if pending tools were found (not necessarily executed successfully)
}

// PrepareSession prepares a session for use, handling creation, loading, forking,
// pending tools execution, and user message saving as needed.
func (c *Coordinator) PrepareSession(ctx context.Context, inputStr string, isRegeneration bool, approver core.Approver) (*PrepareSessionResult, error) {
	// Load or create the session
	sess, err := c.loadOrCreateSession(ctx, inputStr)
	if err != nil {
		return nil, errors.WrapError("prepare session", err)
	}

	sess, err = c.maybeForkResumedSession(ctx, sess)
	if err != nil {
		return nil, err
	}

	executedPending, err := c.maybeExecutePendingTools(ctx, sess, isRegeneration, approver)
	if err != nil {
		return nil, err
	}

	if err := c.maybeSaveUserInput(ctx, sess, inputStr, isRegeneration); err != nil {
		return nil, err
	}

	return &PrepareSessionResult{
		Session:         sess,
		ExecutedPending: executedPending,
	}, nil
}

func (c *Coordinator) maybeForkResumedSession(ctx context.Context, sess *Session) (*Session, error) {
	if c.cfg.GetResume() == "" {
		return sess, nil
	}

	sessionSystemMsg, err := GetSystemMessage(sess.Path)
	if err != nil {
		return nil, errors.WrapError("get session system message", err)
	}

	decision := DecideResumeFork(sessionSystemMsg, c.cfg)
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

func (c *Coordinator) maybeExecutePendingTools(ctx context.Context, sess *Session, isRegeneration bool, approver core.Approver) (bool, error) {
	if sess == nil || c.cfg.GetResume() == "" || isRegeneration {
		return false, nil
	}

	hasPending, err := ExecutePendingTools(ctx, sess, c.cfg, logger.From(ctx), c.notifier, approver)
	if err != nil {
		return false, err
	}
	return hasPending, nil
}

func (c *Coordinator) maybeSaveUserInput(ctx context.Context, sess *Session, inputStr string, isRegeneration bool) error {
	if isRegeneration {
		return nil
	}
	return c.saveUserMessage(ctx, sess, inputStr)
}

// loadOrCreateSession determines and loads/creates the appropriate session
func (c *Coordinator) loadOrCreateSession(ctx context.Context, inputStr string) (*Session, error) {
	if resume := c.cfg.GetResume(); resume != "" {
		// Resume or branch based on the provided ID
		return c.handleResumeOrBranch(ctx, resume, inputStr)
	} else if branch := c.cfg.GetBranch(); branch != "" {
		// Explicit branch
		sessionPath, messageID := ParseMessageID(branch)
		siblingPath, err := CreateSibling(ctx, sessionPath, messageID)
		if err != nil {
			return nil, errors.WrapError("create branch", err)
		}
		return LoadSession(siblingPath)
	} else {
		// Create new session with system prompt
		return CreateSession(c.cfg.GetEffectiveSystem(), logger.From(ctx))
	}
}

// interpretResumeArg determines if the resume argument is a session ID or message ID
type resumeArgType int

const (
	resumeArgSession resumeArgType = iota
	resumeArgMessage
)

type resumeArgInfo struct {
	argType     resumeArgType
	sessionPath string
	messageID   string
}

func interpretResumeArg(resumeID string) resumeArgInfo {
	if IsMessageReference(resumeID) {
		sessionPath, messageID := ParseMessageID(resumeID)
		if messageID != "" {
			return resumeArgInfo{
				argType:     resumeArgMessage,
				sessionPath: sessionPath,
				messageID:   messageID,
			}
		}
	}
	return resumeArgInfo{
		argType:     resumeArgSession,
		sessionPath: resumeID,
	}
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
		if !os.IsNotExist(loadErr) && !strings.Contains(loadErr.Error(), "not found") {
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

// handleResumeOrBranch handles the -resume flag which can be either a session ID or a message ID
func (c *Coordinator) handleResumeOrBranch(ctx context.Context, resumeID string, inputStr string) (*Session, error) {
	info := interpretResumeArg(resumeID)

	switch info.argType {
	case resumeArgSession:
		// Try to load as a session with retries
		sess, err := loadSessionWithRetry(info.sessionPath)
		if err != nil {
			return nil, errors.WrapError("load session", fmt.Errorf("session or message not found: %s", resumeID))
		}
		return sess, nil

	case resumeArgMessage:
		// Create branch from the message
		siblingPath, err := CreateSibling(ctx, info.sessionPath, info.messageID)
		if err != nil {
			return nil, errors.WrapError("create branch", err)
		}
		sess, err := LoadSession(siblingPath)
		if err != nil {
			return nil, err
		}
		c.notifier.Infof("Branching from message %s", resumeID)
		return sess, nil

	default:
		return nil, errors.WrapError("parse resume argument", fmt.Errorf("invalid resume argument: %s", resumeID))
	}
}

// saveUserMessage saves the user input to the session
func (c *Coordinator) saveUserMessage(ctx context.Context, sess *Session, inputStr string) error {
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
