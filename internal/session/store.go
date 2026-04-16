package session

import (
	"context"
	"fmt"
	"lmtools/internal/core"
	"strings"
)

// Store implements the core.SessionStore interface
// It provides a clean abstraction for session storage operations
type Store struct {
	session *Session
	log     core.Logger
}

// NewStore creates a new SessionStore implementation
func NewStore(session *Session, log core.Logger) *Store {
	return &Store{
		session: session,
		log:     log,
	}
}

func (s *Store) finalizeSave(result SaveResult, logFormat string) (string, string, error) {
	if result.Path != s.session.Path {
		s.session.Path = result.Path
		if s.log != nil {
			s.log.Debugf(logFormat, GetSessionID(result.Path), result.MessageID)
		}
	}

	return result.Path, result.MessageID, nil
}

// SaveAssistant saves an assistant message with optional tool calls
func (s *Store) SaveAssistant(ctx context.Context, text string, calls []core.ToolCall, model string) (string, string, error) {
	return s.SaveAssistantWithThoughtSignature(ctx, text, calls, model, "")
}

// SaveAssistantWithThoughtSignature saves an assistant message with optional
// Google thought signature metadata.
func (s *Store) SaveAssistantWithThoughtSignature(ctx context.Context, text string, calls []core.ToolCall, model string, thoughtSignature string) (string, string, error) {
	if s.session == nil {
		return "", "", fmt.Errorf("session is nil")
	}

	result, err := SaveAssistantResponseWithMetadata(ctx, s.session, strings.TrimSpace(text), calls, model, thoughtSignature)
	if err != nil {
		return "", "", err
	}

	return s.finalizeSave(result, "Response saved to sibling branch %s as message %s")
}

// SaveToolResults saves tool execution results with optional additional text
func (s *Store) SaveToolResults(ctx context.Context, results []core.ToolResult, additionalText string) (string, string, error) {
	if s.session == nil {
		return "", "", fmt.Errorf("session is nil")
	}

	result, err := SaveToolResults(ctx, s.session, results, additionalText)
	if err != nil {
		return "", "", err
	}

	return s.finalizeSave(result, "Tool results saved to sibling branch %s as message %s")
}

// UpdatePath updates the current session path
func (s *Store) UpdatePath(newPath string) {
	if s.session != nil && newPath != "" && newPath != s.session.Path {
		s.session.Path = newPath
		if s.log != nil {
			s.log.Debugf("Session path updated to %s", GetSessionID(newPath))
		}
	}
}

// GetPath returns the current session path
func (s *Store) GetPath() string {
	if s.session == nil {
		return ""
	}
	return s.session.Path
}
