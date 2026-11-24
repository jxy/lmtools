package oldsession

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
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
	rootSession := treeRoot(sessionPath)

	// Use session lock at the root level to prevent concurrent sibling creation
	// Use retry logic to allow multiple goroutines to eventually succeed
	var created string
	err := withSessionLockContext(ctx, rootSession, 5*time.Second, func() error {
		// A sibling records the anchor role its lineage is read with: the
		// role recorded by the sibling the message bubbled up through, or
		// the role the anchor has now. Without either it records none; its
		// lineage cannot be read then anyway.
		role, err := bubbledAnchorRole(sessionPath, anchorPath)
		if err != nil {
			return err
		}
		if role == "" {
			if anchor, err := readMessage(anchorPath, anchorID); err == nil {
				role = anchor.Role
			} else {
				logger.From(ctx).Debugf("Creating sibling without an anchor role: %v", err)
			}
		}
		if created, err = createBranchLocked(anchorPath, anchorID, role, sessionPath); err != nil {
			return err
		}
		logger.From(ctx).Debugf("Created sibling branch: %s", GetSessionID(created))
		return nil
	})
	return created, err
}

// branchRecordFile, in a sibling directory, records the role that the
// message the sibling replaces had when the sibling was made. The role
// decides which earlier messages the sibling continues, and the sibling's
// lineage is read with the recorded one, so replacing or deleting that
// message later leaves the sibling's history as it was. A sibling made
// before the record existed is read with the message's current role.
const branchRecordFile = ".branch.json"

type branchRecord struct {
	AnchorRole core.Role `json:"anchor_role"`
}

// createBranchLocked makes the next sibling of message anchorID in
// anchorPath for a write or request that started in source. It records
// role, when it has one, as the anchor role the sibling's lineage is read
// with, and gives the sibling the notebook binding source reads. The caller
// holds the tree's lock.
func createBranchLocked(anchorPath, anchorID string, role core.Role, source string) (string, error) {
	name, err := GetNextSiblingPath(anchorPath, anchorID)
	if err != nil {
		return "", errors.WrapError("get sibling path", err)
	}
	fullPath := filepath.Join(anchorPath, name)
	if err := os.Mkdir(fullPath, constants.DirPerm); err != nil {
		return "", errors.WrapError("create sibling directory", err)
	}
	if role != "" {
		data, err := json.Marshal(branchRecord{AnchorRole: role})
		if err == nil {
			err = writeFileAtomic(filepath.Join(fullPath, branchRecordFile), data)
		}
		if err != nil {
			_ = os.RemoveAll(fullPath)
			return "", errors.WrapError("record sibling anchor", err)
		}
	}
	if err := carryBindingLocked(source, fullPath); err != nil {
		_ = os.RemoveAll(fullPath)
		return "", errors.WrapError("carry notebook binding", err)
	}
	return fullPath, nil
}

// recordedAnchorRole returns the anchor role a sibling directory recorded,
// or none when it has no record. A record that exists and cannot be read
// is an error: reading the sibling with another role would give it
// another history.
func recordedAnchorRole(siblingPath string) (core.Role, error) {
	path := filepath.Join(siblingPath, branchRecordFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	var record branchRecord
	if err == nil {
		err = json.Unmarshal(data, &record)
	}
	if err == nil && record.AnchorRole == "" {
		err = fmt.Errorf("no anchor role")
	}
	if err != nil {
		return "", errors.WrapError("read branch record "+path, err)
	}
	return record.AnchorRole, nil
}

// siblingAnchorRole returns the role the lineage of a sibling directory is
// read with: the role its branch record holds, or current, the role of the
// message it replaces, when it has no record.
func siblingAnchorRole(siblingPath string, current core.Role) (core.Role, error) {
	role, err := recordedAnchorRole(siblingPath)
	if err != nil || role != "" {
		return role, err
	}
	return current, nil
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
