package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"time"
)

// MaybeForkForSystem forks sess under effectiveSystem when that differs from
// the system prompt its lineage carries, and returns the session to continue
// in and whether it forked.
func MaybeForkForSystem(ctx context.Context, sess *Session, effectiveSystem string) (*Session, bool, error) {
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
	newSession, err := forkForSystem(ctx, sess, effectiveSystem)
	if err != nil {
		return nil, false, err
	}
	return newSession, true, nil
}

// forkForSystem forks sess under system: a new session holding its lineage,
// through the head it pins when it pins one, with system in place of the
// lineage's own prompt. An empty system is no system message, as a new
// session stores it and as the request carries it. A plan decides the fork
// when it prepares its request, so this does not decide again.
func forkForSystem(ctx context.Context, sess *Session, system string) (*Session, error) {
	originalID := GetSessionID(sess.Path)
	logger.From(ctx).Infof("Forking session %s due to system prompt change", originalID)

	var stored *string
	if system != "" {
		stored = &system
	}
	var (
		newSession *Session
		err        error
	)
	if sess.Head != nil {
		// A pinned head bounds the copy, so a message another writer
		// appended past it stays out of the fork this turn continues in,
		// and the fork pins the head it was built through.
		newSession, err = buildFork(ctx, DefaultManager(), sess.Path, func() (forkSource, error) {
			refs, _, err := lineageThroughHeadLocked(DefaultManager(), sess.Path, sess.Head)
			return forkSource{refs: refs, system: stored, pin: true}, err
		})
	} else {
		newSession, err = ForkSessionWithSystemMessage(ctx, sess.Path, stored)
	}
	if err != nil {
		return nil, errors.WrapError("create forked session", err)
	}

	logger.From(ctx).Infof("Created forked session %s from %s with new system prompt",
		GetSessionID(newSession.Path), originalID)
	return newSession, nil
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

// GetSystemMessage returns the system prompt the lineage of sessionPath
// carries, found the way scanLineage finds the rest of the conversation: a
// branch inherits the prompt its root session begins with. It is nil when the
// lineage has none.
func GetSystemMessage(sessionPath string) (*string, error) {
	refs, err := lineageMessageRefsWithManager(DefaultManager(), sessionPath)
	if err != nil {
		return nil, errors.WrapError("read session lineage", err)
	}
	return lineageSystemPrompt(refs), nil
}

// lineageSystemPrompt returns the system prompt refs begin with, or nil when
// they begin with another message. A stored empty prompt stays distinct from
// an absent one.
func lineageSystemPrompt(refs []lineageMessageRef) *string {
	if len(refs) == 0 || refs[0].message.Role != core.RoleSystem {
		return nil
	}
	system := refs[0].message.Content
	return &system
}

// ForkSessionWithSystemMessage creates a new session by copying an existing one with a new system message.
func ForkSessionWithSystemMessage(ctx context.Context, originalPath string, newSystemPrompt *string) (*Session, error) {
	return ForkSessionWithManager(ctx, DefaultManager(), originalPath, newSystemPrompt)
}

// ForkSessionWithManager creates a new session in manager's session tree by
// copying the lineage of an existing session. The fork begins with
// newSystemPrompt as its system message, an empty prompt included, and with
// none when newSystemPrompt is nil.
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
		return forkSource{refs: refs, system: newSystemPrompt}, nil
	})
}
