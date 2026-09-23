package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"time"
)

// MaybeForkForSystem checks if the session needs forking due to system prompt change
// and creates a fork if necessary. Returns the (possibly new) session and whether
// a fork was created.
func MaybeForkForSystem(ctx context.Context, sess *Session, effectiveSystem string) (*Session, bool, error) {
	// Get the original system message from the session
	originalSystemMsg, err := GetSystemMessage(sess.Path)
	if err != nil {
		return nil, false, errors.WrapError("get system message from session", err)
	}

	// Single rule: fork if the effective system prompt differs from the original
	needFork := false
	if originalSystemMsg == nil && effectiveSystem != "" {
		needFork = true
	} else if originalSystemMsg != nil && *originalSystemMsg != effectiveSystem {
		needFork = true
	}

	if !needFork {
		return sess, false, nil
	}

	originalID := GetSessionID(sess.Path)
	logger.From(ctx).Infof("Forking session %s due to system prompt change", originalID)

	var newSession *Session
	if sess.Head != nil {
		// A pinned head bounds the copy, so a message another writer
		// appended past it stays out of the fork this turn continues in,
		// and the fork pins the head it was built through.
		newSession, err = buildFork(ctx, DefaultManager(), sess.Path, func() (forkSource, error) {
			refs, _, err := lineageThroughHeadLocked(DefaultManager(), sess.Path, sess.Head)
			return forkSource{refs: refs, system: effectiveSystem, pin: true}, err
		})
	} else {
		newSession, err = ForkSessionWithSystemMessage(ctx, sess.Path, &effectiveSystem)
	}
	if err != nil {
		return nil, false, errors.WrapError("create forked session", err)
	}

	logger.From(ctx).Infof("Created forked session %s from %s with new system prompt",
		GetSessionID(newSession.Path), originalID)

	return newSession, true, nil
}

// saveSystemMessage saves the system prompt as message 0000 and returns the
// revision it was committed with.
func saveSystemMessage(session *Session, systemPrompt string) (string, error) {
	revision := newRevision()
	return revision, writeMessage(session.Path, "0000", Message{
		ID:        "0000",
		Role:      core.RoleSystem,
		Content:   systemPrompt,
		Timestamp: time.Now(),
		Revision:  revision,
	})
}

// GetSystemMessage reads the system message from a session if it exists.
func GetSystemMessage(sessionPath string) (*string, error) {
	sessionPath = DefaultManager().ResolveSessionPath(sessionPath)

	paths := buildMessageFilePaths(sessionPath, "0000")
	if _, err := os.Stat(paths.JSONPath); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, errors.WrapError("stat system message", err)
	}

	msg, err := readMessage(sessionPath, "0000")
	if err != nil {
		return nil, errors.WrapError("read system message", err)
	}
	if msg.Role == core.RoleSystem {
		systemMsg := msg.Content
		return &systemMsg, nil
	}

	return nil, nil
}

// ForkSessionWithSystemMessage creates a new session by copying an existing one with a new system message.
func ForkSessionWithSystemMessage(ctx context.Context, originalPath string, newSystemPrompt *string) (*Session, error) {
	return ForkSessionWithManager(ctx, DefaultManager(), originalPath, newSystemPrompt)
}

// ForkSessionWithManager creates a new session in manager's session tree by copying
// the lineage of an existing session with an optional replacement system message.
func ForkSessionWithManager(ctx context.Context, manager *Manager, originalPath string, newSystemPrompt *string) (*Session, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	originalPath = manager.ResolveSessionPath(originalPath)

	return buildFork(ctx, manager, originalPath, func() (forkSource, error) {
		refs, err := lineageMessageRefsWithManager(manager, originalPath)
		if err != nil {
			return forkSource{}, errors.WrapError("get lineage from original session", err)
		}
		source := forkSource{refs: refs}
		if newSystemPrompt != nil {
			source.system = *newSystemPrompt
		}
		return source, nil
	})
}
