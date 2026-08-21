package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"slices"
)

// conversationSnapshot caches the stable parent lineage while tracking committed
// messages appended to the active session directory.
type conversationSnapshot struct {
	sessionPath string
	manager     *Manager
	refs        []lineageMessageRef
	// activeIDs is what the active directory listed at the last scan, in order,
	// including IDs skipped as unreadable. refs alone cannot answer that
	// question once a message is skipped, and append detection has to compare
	// against exactly what the snapshot already considered: a skipped ID left
	// out of this list would be rediscovered as an append on every later call.
	activeIDs []string
	// built holds the typed message decoded from each ref, aligned with refs
	// and never longer than it, and toolNames is the tool-name index those
	// builds accumulated. Both are dropped by rebuild.
	built     []core.TypedMessage
	toolNames map[string]string
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
	scan, err := scanLineage(s.manager, path)
	if err != nil {
		return errors.WrapError("build message lineage refs", err)
	}

	s.refs = scan.refs
	s.activeIDs = scan.activeIDs
	s.sessionPath = path
	// Every ref may now describe a different message than the one already
	// decoded at that index, so the decoded messages and the tool-name index
	// built from them go with it. Dropping them is what makes reuse safe.
	s.built = nil
	s.toolNames = nil
	return nil
}

func (s *conversationSnapshot) refresh(path string) error {
	path = s.manager.ResolveSessionPath(path)
	if path != s.sessionPath {
		return s.rebuild(path)
	}

	committedIDs, err := listMessages(path)
	if err != nil {
		return errors.WrapError("list active session messages", err)
	}
	knownIDs := s.activeIDs
	if len(committedIDs) < len(knownIDs) || !slices.Equal(committedIDs[:len(knownIDs)], knownIDs) {
		return s.rebuild(path)
	}

	appendedIDs := committedIDs[len(knownIDs):]
	appendedRefs := make([]lineageMessageRef, 0, len(appendedIDs))
	for _, messageID := range appendedIDs {
		msg, err := readMessage(path, messageID)
		if err != nil {
			return errors.WrapError("read appended message "+messageID, err)
		}
		appendedRefs = append(appendedRefs, lineageMessageRef{path: path, message: *msg})
	}

	// Commit the refreshed refs only after the entire suffix was read
	// successfully. A retry after a transient read failure then starts from the
	// original cache and cannot append an earlier ref twice.
	s.refs = append(s.refs, appendedRefs...)
	s.activeIDs = append(s.activeIDs, appendedIDs...)
	return nil
}

func (s *conversationSnapshot) buildTypedMessages(ctx context.Context, path string) ([]core.TypedMessage, error) {
	if err := s.refresh(path); err != nil {
		return nil, err
	}
	if err := s.buildAppendedRefs(); err != nil {
		return nil, err
	}

	// Hand back a copy: the next round appends to built, which would otherwise
	// write into the spare capacity of a slice a caller is still holding.
	return slices.Clone(s.built), nil
}

// buildAppendedRefs decodes the sidecars of the refs that joined since the last
// build and leaves the earlier ones alone. Between rebuilds refs only ever
// grows by append — a different session path, a sibling branch, a deletion, or
// any divergence in the committed ID prefix all route through rebuild, which
// drops what was built — so a ref already decoded still describes the same
// committed message, and no production caller rewrites a committed sidecar.
// Without this the tool loop re-opened and re-decoded the whole transcript once
// per round, which is quadratic in the number of rounds.
func (s *conversationSnapshot) buildAppendedRefs() error {
	if s.toolNames == nil {
		s.toolNames = make(map[string]string, len(s.refs))
	}

	// A failure leaves built holding the prefix it finished, still aligned with
	// refs, so a retry resumes at the message that failed rather than replaying
	// the tool-name index from the start.
	for i := len(s.built); i < len(s.refs); i++ {
		message, err := buildTypedMessageFromLineageRef(s.refs[i], s.toolNames)
		if err != nil {
			return err
		}
		s.built = append(s.built, message)
	}
	return nil
}
