package session

import (
	"context"
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
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath = manager.ResolveSessionPath(sessionPath)

	// Split the path into "root dir" and the list of sibling-components that
	// were traversed to arrive at sessionPath.
	rootDir, components := manager.ParseSessionPath(sessionPath)

	// Helper that loads messages in a dir and returns them sorted.
	load := func(dir string) ([]Message, error) {
		msgs, err := loadMessagesInDir(dir)
		if err != nil {
			return nil, errors.WrapError("load messages in "+dir, err)
		}
		return msgs, nil
	}

	var lineage []Message

	// Track the last assistant seen along the path and whether it has already
	// been appended to the lineage.
	var lastAssistant *Message
	assistantAlreadyInLineage := false

	dir := rootDir

	for i := 0; ; i++ {
		msgs, err := load(dir)
		if err != nil {
			return nil, err
		}

		// Final directory: include everything and stop.
		if i == len(components) {
			lineage = append(lineage, msgs...)
			break
		}

		// We are about to step into a sibling directory. Find the branch point.
		comp := components[i]
		_, branchMsgID, _ := IsSiblingDir(comp)

		branchIdx := -1
		for j := range msgs {
			if msgs[j].ID == branchMsgID {
				branchIdx = j
				break
			}
		}
		if branchIdx == -1 {
			return nil, errors.WrapError("find branch point", fmt.Errorf("branch point %s not found in %s", branchMsgID, dir))
		}

		branchMsg := msgs[branchIdx]

		switch branchMsg.Role {
		case core.RoleAssistant:
			// Regenerating an assistant: keep everything before it.
			lineage = append(lineage, msgs[:branchIdx]...)
			lastAssistant = &branchMsg
			assistantAlreadyInLineage = false

		case core.RoleUser:
			// Alternative user input. Keep everything up to and including the
			// previous assistant, if there is one.
			prevAssistIdx := -1
			for j := branchIdx - 1; j >= 0; j-- {
				if msgs[j].Role == core.RoleAssistant {
					prevAssistIdx = j
					break
				}
			}

			if prevAssistIdx != -1 {
				lineage = append(lineage, msgs[:prevAssistIdx+1]...)
				lastAssistant = &msgs[prevAssistIdx]
				assistantAlreadyInLineage = true
			} else {
				if lastAssistant != nil && !assistantAlreadyInLineage {
					lineage = append(lineage, *lastAssistant)
					assistantAlreadyInLineage = true
				}
			}

		default:
			return nil, errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", branchMsg.Role, branchMsg.ID))
		}

		dir = filepath.Join(dir, comp)
	}

	return lineage, nil
}

// GetSessionID returns the session ID from a full session path.
func GetSessionID(sessionPath string) string {
	return DefaultManager().SessionID(sessionPath)
}
