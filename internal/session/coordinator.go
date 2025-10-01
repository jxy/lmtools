package session

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"lmtools/internal/prompts"
	"os"
	"strings"
	"time"
)

// Coordinator orchestrates session operations including creation, loading,
// forking, and pending tools execution. It encapsulates the complex session
// management logic that was previously in cmd/lmc/main.go.
type Coordinator struct {
	cfg      core.RequestConfig
	notifier core.Notifier
}

// NewCoordinator creates a new session coordinator
func NewCoordinator(cfg core.RequestConfig, notifier core.Notifier) *Coordinator {
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

	// Check if we need to fork for system prompt changes
	// The purpose of forking is to ensure the best system message is used for the conversation
	//
	// Decision Matrix:
	//
	// Case 1: System prompt specified on command line (-s flag)
	//   - If specified prompt ≠ session prompt → Fork with specified prompt
	//   - If specified prompt = session prompt → Resume without fork
	//
	// Case 2: No system prompt specified on command line
	//   - If session has default non-tool prompt AND now using tools → Fork with tool prompt
	//   - If session has default non-tool prompt AND not using tools → Resume without fork
	//   - If session has default tool prompt → Resume without fork (regardless of tool state)
	//   - If session has custom prompt → Resume without fork (preserve custom prompt)
	//
	if c.cfg.GetResume() != "" {
		// Get the session's current system prompt
		sessionSystemMsg, err := GetSystemMessage(sess.Path)
		if err != nil {
			return nil, errors.WrapError("get session system message", err)
		}

		shouldFork := false
		var newSystemPrompt string

		if c.cfg.IsSystemExplicitlySet() {
			// Case 1: System prompt explicitly specified on command line
			// Simple logic: fork if the specified prompt differs from session's prompt
			specifiedPrompt := c.cfg.GetEffectiveSystem()

			if sessionSystemMsg == nil && specifiedPrompt != "" {
				// Session has no prompt, but user specified one
				shouldFork = true
				newSystemPrompt = specifiedPrompt
			} else if sessionSystemMsg != nil && *sessionSystemMsg != specifiedPrompt {
				// Session has a different prompt than specified
				shouldFork = true
				newSystemPrompt = specifiedPrompt
			}
			// Otherwise: prompts match, no fork needed

		} else {
			// Case 2: No system prompt specified on command line
			// Check if we need to upgrade from default non-tool to tool prompt

			if sessionSystemMsg != nil {
				sessionPrompt := *sessionSystemMsg

				// Check if session has the default non-tool prompt
				if sessionPrompt == prompts.DefaultSystemPrompt && c.cfg.IsToolEnabled() {
					// Session has default non-tool prompt and we're now using tools
					// Fork to use the tool-specific prompt
					shouldFork = true
					newSystemPrompt = prompts.ToolSystemPrompt
				}
				// For all other cases (default tool prompt, custom prompt), keep the session's prompt
			} else if c.cfg.IsToolEnabled() {
				// Session has no system prompt and we're using tools
				// Fork to add the tool prompt
				shouldFork = true
				newSystemPrompt = prompts.ToolSystemPrompt
			}
			// If session has no prompt and we're not using tools, we could fork to add
			// the default prompt, but this is optional - keeping no prompt is also valid
		}

		if shouldFork {
			forkedSess, forked, err := MaybeForkForSystem(ctx, sess, newSystemPrompt)
			if err != nil {
				return nil, err
			}
			if forked {
				c.notifier.Infof("Forked session due to system prompt change: %s", GetSessionID(forkedSess.Path))
			}
			sess = forkedSess
		}
	}

	// Execute pending tool calls BEFORE saving new user input
	// executedPending tracks whether pending tools were found (not whether they succeeded)
	// This is used to allow empty input when continuing tool execution
	executedPending := false
	if sess != nil && c.cfg.GetResume() != "" && !isRegeneration {
		hasPending, err := ExecutePendingTools(ctx, sess, c.cfg, logger.From(ctx), c.notifier, approver)
		if err != nil {
			return nil, err
		}
		executedPending = hasPending
	}

	// Save user input if not regenerating
	if !isRegeneration {
		if err := c.saveUserMessage(ctx, sess, inputStr); err != nil {
			return nil, err
		}
	}

	return &PrepareSessionResult{
		Session:         sess,
		ExecutedPending: executedPending,
	}, nil
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
