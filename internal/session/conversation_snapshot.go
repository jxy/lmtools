package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/errors"
)

// conversationSnapshot caches message directory lookups along the active session
// lineage so repeated conversation reconstruction does not rebuild indexes.
type conversationSnapshot struct {
	sessionPath  string
	messageIndex map[string]string
}

func newConversationSnapshot(sessionPath string) (*conversationSnapshot, error) {
	messageIndex, err := indexMessagesAlongPath(sessionPath)
	if err != nil {
		return nil, errors.WrapError("index message directories", err)
	}

	return &conversationSnapshot{
		sessionPath:  sessionPath,
		messageIndex: messageIndex,
	}, nil
}

func (s *conversationSnapshot) ensureIndexed(path string) error {
	if path == s.sessionPath {
		return nil
	}

	newIndex, err := indexMessagesAlongPath(path)
	if err != nil {
		return errors.WrapError("index new message directories", err)
	}
	for msgID, dir := range newIndex {
		s.messageIndex[msgID] = dir
	}
	s.sessionPath = path
	return nil
}

func (s *conversationSnapshot) buildTypedMessages(ctx context.Context, path string) ([]core.TypedMessage, error) {
	if err := s.ensureIndexed(path); err != nil {
		return nil, err
	}

	messages, err := GetLineage(path)
	if err != nil {
		return nil, errors.WrapError("get lineage", err)
	}

	return BuildMessagesWithIndex(ctx, messages, s.messageIndex, path)
}

func (s *conversationSnapshot) pendingToolCalls(ctx context.Context, path string) ([]core.ToolCall, error) {
	if err := s.ensureIndexed(path); err != nil {
		return nil, err
	}

	messages, err := GetLineage(path)
	if err != nil {
		return nil, errors.WrapError("get lineage", err)
	}
	if len(messages) == 0 {
		return nil, nil
	}

	lastMsg := messages[len(messages)-1]
	if lastMsg.Role != core.RoleAssistant {
		return nil, nil
	}

	msgDir := resolveIndexedMessageDir(ctx, s.messageIndex, lastMsg.ID, path)
	toolInteraction, err := LoadToolInteraction(msgDir, lastMsg.ID)
	if err != nil {
		return nil, errors.WrapError("load tool interaction", err)
	}
	if toolInteraction == nil || len(toolInteraction.Calls) == 0 {
		return nil, nil
	}

	return toolInteraction.Calls, nil
}
