package session

import (
	stdErrors "errors"
	"lmtools/internal/core"
	"lmtools/internal/errors"
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
		// Look for .json files (metadata) which every message has.
		// Skip sidecar JSON files as they're part of the message, not the ID.
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
	return strings.HasSuffix(name, ".json") &&
		!strings.HasSuffix(name, ".tools.json") &&
		!strings.HasSuffix(name, ".blocks.json")
}

// loadMessagesInDir loads all messages from a directory.
func loadMessagesInDir(dirPath string) ([]Message, error) {
	msgIDs, err := listMessages(dirPath)
	if err != nil {
		return nil, err
	}

	messages := make([]Message, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		msg, err := readMessage(dirPath, msgID)
		if err != nil {
			// Skip corrupted messages silently; caller can decide to log.
			continue
		}
		messages = append(messages, *msg)
	}

	return messages, nil
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
