package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/errors"
)

// conversationSnapshot caches message directory lookups along the active session
// lineage so repeated conversation reconstruction does not rebuild indexes.
type conversationSnapshot struct {
	sessionPath string
	manager     *Manager
	refs        []lineageMessageRef
}

func newConversationSnapshot(sessionPath string) (*conversationSnapshot, error) {
	return newConversationSnapshotWithManager(DefaultManager(), sessionPath)
}

func newConversationSnapshotWithManager(manager *Manager, sessionPath string) (*conversationSnapshot, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	refs, err := lineageMessageRefsWithManager(manager, sessionPath)
	if err != nil {
		return nil, errors.WrapError("build message lineage refs", err)
	}

	return &conversationSnapshot{
		sessionPath: sessionPath,
		manager:     manager,
		refs:        refs,
	}, nil
}

func (s *conversationSnapshot) ensureIndexed(path string) error {
	if path == s.sessionPath {
		return nil
	}

	refs, err := lineageMessageRefsWithManager(s.manager, path)
	if err != nil {
		return errors.WrapError("build new message lineage refs", err)
	}
	s.refs = refs
	s.sessionPath = path
	return nil
}

func (s *conversationSnapshot) buildTypedMessages(ctx context.Context, path string) ([]core.TypedMessage, error) {
	if err := s.ensureIndexed(path); err != nil {
		return nil, err
	}

	return buildTypedMessagesFromLineageRefs(ctx, s.refs)
}

func (s *conversationSnapshot) pendingToolCalls(ctx context.Context, path string) ([]core.ToolCall, error) {
	if err := s.ensureIndexed(path); err != nil {
		return nil, err
	}

	return pendingToolCallsFromLineageRefs(ctx, s.refs)
}
