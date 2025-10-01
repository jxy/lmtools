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

// SaveAssistant saves an assistant message with optional tool calls
func (s *Store) SaveAssistant(ctx context.Context, text string, calls []core.ToolCall, model string) (string, string, error) {
	if s.session == nil {
		return "", "", fmt.Errorf("session is nil")
	}

	result, err := SaveAssistantResponseWithTools(ctx, s.session, strings.TrimSpace(text), calls, model)
	if err != nil {
		return "", "", err
	}

	// Update internal session path if it changed
	if result.Path != s.session.Path {
		s.session.Path = result.Path
		if s.log != nil {
			s.log.Debugf("Response saved to sibling branch %s as message %s",
				GetSessionID(result.Path), result.MessageID)
		}
	}

	return result.Path, result.MessageID, nil
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

	// Update internal session path if it changed
	if result.Path != s.session.Path {
		s.session.Path = result.Path
		if s.log != nil {
			s.log.Debugf("Tool results saved to sibling branch %s as message %s",
				GetSessionID(result.Path), result.MessageID)
		}
	}

	return result.Path, result.MessageID, nil
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
