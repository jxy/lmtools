package session

import (
	"context"
	stdErrors "errors"
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
	originalPath = DefaultManager().ResolveSessionPath(originalPath)

	var (
		newSession *Session
		err        error
	)
	if newSystemPrompt != nil {
		newSession, err = CreateSession(*newSystemPrompt, logger.From(ctx))
	} else {
		newSession, err = CreateSession("", logger.From(ctx))
	}
	if err != nil {
		return nil, errors.WrapError("create new session", err)
	}

	if err := copyForkLineage(ctx, originalPath, newSession); err != nil {
		os.RemoveAll(newSession.Path)
		return nil, err
	}

	return newSession, nil
}

func copyForkLineage(ctx context.Context, originalPath string, newSession *Session) error {
	messages, err := GetLineage(originalPath)
	if err != nil {
		return errors.WrapError("get lineage from original session", err)
	}

	msgIndex, err := indexMessagesAlongPath(originalPath)
	if err != nil {
		return errors.WrapError("index lineage messages", err)
	}

	mc := newMessageCommitter(newSession.Path)

	for _, msg := range messages {
		if msg.Role == core.RoleSystem {
			continue
		}

		var toolInteraction *core.ToolInteraction
		originalMsgPath := msgIndex[msg.ID]
		logger.From(ctx).Debugf("Processing message %s (role=%s) from path %s", msg.ID, msg.Role, originalMsgPath)

		if originalMsgPath != "" {
			ti, err := LoadToolInteraction(originalMsgPath, msg.ID)
			if err != nil {
				logger.From(ctx).Debugf("Failed to load tool interaction for message %s: %v", msg.ID, err)
			} else if ti != nil {
				toolInteraction = ti
				logger.From(ctx).Debugf("Loaded tool interaction for message %s: %d calls, %d results",
					msg.ID, len(ti.Calls), len(ti.Results))
			} else {
				logger.From(ctx).Debugf("No tool file found for message %s at %s", msg.ID, buildMessageFilePaths(originalMsgPath, msg.ID).ToolsPath)
			}
		}

		newMsg := Message{
			Role:      msg.Role,
			Content:   msg.Content,
			Timestamp: msg.Timestamp,
			Model:     msg.Model,
		}

		staged, err := mc.Stage(newMsg, toolInteraction)
		if err != nil {
			return errors.WrapError("stage message", err)
		}

		newMsgID, needSibling, _, err := mc.Commit(ctx, staged)
		staged.Close()

		if err != nil {
			return errors.WrapError("place message", err)
		}
		if needSibling {
			return errors.WrapError("copy message", stdErrors.New("unexpected conflict when copying message"))
		}

		logger.From(ctx).Debugf("Copied message %s -> %s (role=%s, hasTools=%v)",
			msg.ID, newMsgID, msg.Role, toolInteraction != nil)
	}

	return nil
}
