package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/errors"
)

// conversationSnapshot caches the stable parent lineage while tracking committed
// messages appended to the active session directory.
type conversationSnapshot struct {
	sessionPath      string
	manager          *Manager
	refs             []lineageMessageRef
	activeMessageIDs []string
}

func newConversationSnapshotWithManager(manager *Manager, sessionPath string) (*conversationSnapshot, error) {
	if manager == nil {
		manager = DefaultManager()
	}

	snapshot := &conversationSnapshot{manager: manager}
	if err := snapshot.rebuild(sessionPath); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *conversationSnapshot) rebuild(path string) error {
	path = s.manager.ResolveSessionPath(path)
	refs, err := lineageMessageRefsWithManager(s.manager, path)
	if err != nil {
		return errors.WrapError("build message lineage refs", err)
	}

	s.replaceLineage(path, refs)
	return nil
}

// replaceLineage installs refs and derives the active-directory index from that
// exact lineage view. A message committed after the lineage scan must remain
// absent from both fields so refresh can discover it as an append.
func (s *conversationSnapshot) replaceLineage(path string, refs []lineageMessageRef) {
	activeMessageIDs := make([]string, 0)
	for _, ref := range refs {
		if ref.path == path {
			activeMessageIDs = append(activeMessageIDs, ref.message.ID)
		}
	}

	s.refs = refs
	s.sessionPath = path
	s.activeMessageIDs = activeMessageIDs
}

func (s *conversationSnapshot) refresh(path string) error {
	path = s.manager.ResolveSessionPath(path)
	if path != s.sessionPath {
		return s.rebuild(path)
	}

	activeMessageIDs, err := listMessages(path)
	if err != nil {
		return errors.WrapError("list active session messages", err)
	}
	if !hasMessageIDPrefix(activeMessageIDs, s.activeMessageIDs) {
		return s.rebuild(path)
	}

	appendedRefs := make([]lineageMessageRef, 0, len(activeMessageIDs)-len(s.activeMessageIDs))
	for _, messageID := range activeMessageIDs[len(s.activeMessageIDs):] {
		msg, err := readMessage(path, messageID)
		if err != nil {
			return errors.WrapError("read appended message "+messageID, err)
		}
		appendedRefs = append(appendedRefs, lineageMessageRef{path: path, message: *msg})
	}

	// Commit the refreshed refs and their ID index together only after the entire
	// suffix was read successfully. A retry after a transient read failure then
	// starts from the original cache and cannot append an earlier ref twice.
	s.refs = append(s.refs, appendedRefs...)
	s.activeMessageIDs = activeMessageIDs
	return nil
}

func hasMessageIDPrefix(current, prefix []string) bool {
	if len(current) < len(prefix) {
		return false
	}
	for i, messageID := range prefix {
		if current[i] != messageID {
			return false
		}
	}
	return true
}

func (s *conversationSnapshot) buildTypedMessages(ctx context.Context, path string) ([]core.TypedMessage, error) {
	if err := s.refresh(path); err != nil {
		return nil, err
	}

	return buildTypedMessagesFromLineageRefs(ctx, s.refs)
}
