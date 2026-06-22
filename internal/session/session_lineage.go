package session

import (
	"context"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"time"
)

// CreateSibling creates a new sibling branch from a message.
// WARNING: This function acquires a lock on the ROOT session path to ensure
// atomic creation of sibling branches across the entire session tree.
// DO NOT call this function while holding a lock on a child session path,
// as it will create a high risk of deadlocks (AB-BA lock acquisition).
// Lock hierarchy: root lock may acquire child locks, but not vice versa.
func CreateSibling(ctx context.Context, sessionPath, messageID string) (string, error) {
	// Validate messageID is not empty
	if messageID == "" {
		return "", errors.WrapError("validate message ID", fmt.Errorf("messageID cannot be empty"))
	}

	sessionPath = DefaultManager().ResolveSessionPath(sessionPath)

	// Get the anchor point for creating siblings (implements bubble-up logic)
	anchorPath, anchorID := GetAnchorForBranching(sessionPath, messageID)
	logger.From(ctx).Debugf("Creating sibling for message %s at anchor %s",
		messageID, GetSessionID(anchorPath))

	// Get the root session for locking to prevent any concurrent sibling creation
	// within the same session tree
	rootSession := GetRootSession(sessionPath)

	// Use session lock at the root level to prevent concurrent sibling creation
	// Use retry logic to allow multiple goroutines to eventually succeed
	return WithSessionLockT(rootSession, 5*time.Second, func() (string, error) {
		// Re-calculate the sibling path inside the lock to ensure consistency
		siblingPath, err := GetNextSiblingPath(anchorPath, anchorID)
		if err != nil {
			return "", errors.WrapError("get sibling path", err)
		}

		fullPath := filepath.Join(anchorPath, siblingPath)
		if err := os.MkdirAll(fullPath, constants.DirPerm); err != nil {
			return "", errors.WrapError("create sibling directory", err)
		}

		logger.From(ctx).Debugf("Created sibling branch: %s", GetSessionID(fullPath))
		return fullPath, nil
	})
}

// GetLineage returns all messages in the conversation path, handling sibling branches correctly.
func GetLineage(sessionPath string) ([]Message, error) {
	return GetLineageWithManager(DefaultManager(), sessionPath)
}

func GetLineageWithManager(manager *Manager, sessionPath string) ([]Message, error) {
	// The lineage walk (branch-point resolution across sibling directories) lives
	// in lineageMessageRefsWithManager; this is the same lineage without the
	// per-message source paths.
	refs, err := lineageMessageRefsWithManager(manager, sessionPath)
	if err != nil {
		return nil, err
	}
	lineage := make([]Message, 0, len(refs))
	for _, ref := range refs {
		lineage = append(lineage, ref.message)
	}
	return lineage, nil
}

// GetSessionID returns the session ID from a full session path.
func GetSessionID(sessionPath string) string {
	return DefaultManager().SessionID(sessionPath)
}
