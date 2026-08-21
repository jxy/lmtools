package session

import (
	stdErrors "errors"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"sort"
	"strconv"
	"strings"
)

// listMessages returns all message IDs in a directory, sorted.
// Invariant: A message exists if and only if its .json exists.
// .txt, .tools.json, and .blocks.json are optional adjuncts to the message.
func listMessages(sessionPath string) ([]string, error) {
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		return nil, errors.WrapError("read directory", err)
	}

	messageIDs := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Only committed metadata files have a valid message ID as their stem.
		// Staging files and JSON sidecars are visible in this directory too, but
		// they must never become lineage entries.
		if isMessageMetadataFilename(name) {
			msgID := strings.TrimSuffix(name, ".json")
			// Per documentation: "A message exists if and only if its JSON file exists".
			messageIDs[msgID] = true
		}
	}

	result := make([]string, 0, len(messageIDs))
	for msgID := range messageIDs {
		result = append(result, msgID)
	}

	// Sort numerically to ensure correct ordering even after hex overflow.
	sort.Slice(result, func(i, j int) bool {
		a, _ := strconv.ParseUint(result[i], 16, 64)
		b, _ := strconv.ParseUint(result[j], 16, 64)
		return a < b
	})

	return result, nil
}

func isMessageMetadataFilename(name string) bool {
	if !strings.HasSuffix(name, ".json") {
		return false
	}
	return IsValidMessageID(strings.TrimSuffix(name, ".json"))
}

// loadMessagesInDir loads all messages from a directory.
//
// A committed metadata file that cannot be read or decoded is skipped with a
// warning instead of failing the scan. The state is reachable without any
// operator error: writeStagedTempFile does not sync before commitFiles renames
// a temp into place, so power loss can leave a zero-length committed message.
// Failing the whole load makes resume, branching, forking, and pending-tool
// detection all report that one message, and the only in-tool remedy destroys
// it together with every descendant, so one lost message must cost one message.
// The staged-temp rule is a different rule and still holds ahead of this one:
// listMessages only accepts names whose stem is a valid message ID, so a
// half-written `.tmp-*.json` is never a message and never reaches this loop.
func loadMessagesInDir(dirPath string) ([]Message, error) {
	messages, _, err := loadMessagesInDirWithListing(dirPath)
	return messages, err
}

// loadMessagesInDirWithListing also returns every message ID the directory
// listed, in order, including the ones skipped as unreadable. A caller that
// tracks what a directory held has to tell "not seen yet" from "seen and
// unreadable": a skipped ID missing from that record looks like a later append
// and fails on the read this function just chose to survive.
func loadMessagesInDirWithListing(dirPath string) ([]Message, []string, error) {
	msgIDs, err := listMessages(dirPath)
	if err != nil {
		return nil, nil, err
	}

	messages := make([]Message, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		msg, err := readMessage(dirPath, msgID)
		if err != nil {
			logger.GetLogger().Warnf("Skipping unreadable session message %s in %s: %v", msgID, dirPath, err)
			continue
		}
		messages = append(messages, *msg)
	}

	return messages, msgIDs, nil
}

// findSiblings returns all sibling directories for a given message ID.
func findSiblings(sessionPath, msgID string) ([]string, error) {
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		return nil, errors.WrapError("read directory", err)
	}

	var siblings []string
	prefix := msgID + ".s."

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, prefix) {
			siblings = append(siblings, name)
		}
	}

	sort.Strings(siblings)
	return siblings, nil
}

// IsAssistantMessage checks if the given branch path points to an assistant message.
func IsAssistantMessage(branchPath string) (bool, error) {
	return IsAssistantMessageWithManager(DefaultManager(), branchPath)
}

// IsAssistantMessageWithManager checks if the given branch path points to an assistant message using the provided manager.
func IsAssistantMessageWithManager(manager *Manager, branchPath string) (bool, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	if branchPath == "" {
		return false, errors.WrapError("validate branch path", stdErrors.New("branch path cannot be empty"))
	}

	sessionPath, messageID := manager.ParseMessageID(branchPath)
	if messageID == "" {
		// Not a message path, but this is not necessarily an error.
		return false, nil
	}

	sessionPath = manager.ResolveSessionPath(sessionPath)

	msg, err := readMessage(sessionPath, messageID)
	if err != nil {
		return false, errors.WrapError("read message "+messageID, err)
	}

	return msg.Role == core.RoleAssistant, nil
}
