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

	newSession, err := ForkSessionWithSystemMessage(ctx, sess.Path, &effectiveSystem)
	if err != nil {
		return nil, false, errors.WrapError("create forked session", err)
	}

	logger.From(ctx).Infof("Created forked session %s from %s with new system prompt",
		GetSessionID(newSession.Path), originalID)

	return newSession, true, nil
}

// saveSystemMessage saves the system prompt as message 0000.
func saveSystemMessage(session *Session, systemPrompt string) error {
	return writeMessage(session.Path, "0000", Message{
		ID:        "0000",
		Role:      core.RoleSystem,
		Content:   systemPrompt,
		Timestamp: time.Now(),
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

	var (
		newSession *Session
		err        error
	)
	if newSystemPrompt != nil {
		newSession, err = manager.CreateSession(*newSystemPrompt, logger.From(ctx))
	} else {
		newSession, err = manager.CreateSession("", logger.From(ctx))
	}
	if err != nil {
		return nil, errors.WrapError("create new session", err)
	}

	if err := copyForkLineageWithManager(ctx, manager, originalPath, newSession); err != nil {
		os.RemoveAll(newSession.Path)
		return nil, err
	}

	return newSession, nil
}

func copyForkLineageWithManager(ctx context.Context, manager *Manager, originalPath string, newSession *Session) error {
	if manager == nil {
		manager = DefaultManager()
	}
	messages, err := GetLineageWithManager(manager, originalPath)
	if err != nil {
		return errors.WrapError("get lineage from original session", err)
	}

	msgIndex, err := indexMessagesAlongPathWithManager(manager, originalPath)
	if err != nil {
		return errors.WrapError("index lineage messages", err)
	}

	refs := make([]lineageMessageRef, 0, len(messages))
	for _, msg := range messages {
		originalMsgPath := msgIndex[msg.ID]
		if originalMsgPath == "" {
			originalMsgPath = originalPath
		}
		logger.From(ctx).Debugf("Processing message %s (role=%s) from path %s", msg.ID, msg.Role, originalMsgPath)
		refs = append(refs, lineageMessageRef{path: originalMsgPath, message: msg})
	}

	return copyLineageMessageRefs(ctx, refs, newSession)
}
