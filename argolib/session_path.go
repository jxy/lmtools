package argo

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseSessionPath breaks down a session path into root and components
func ParseSessionPath(path string) (root string, components []string) {
	// Get sessions base directory
	sessionsDir := GetSessionsDir()

	// If path is already relative to sessions dir, extract it
	relPath := path
	if strings.HasPrefix(path, sessionsDir) {
		relPath, _ = filepath.Rel(sessionsDir, path)
	}

	// Split into parts
	parts := strings.Split(relPath, string(filepath.Separator))

	// Filter out empty parts
	var filtered []string
	for _, part := range parts {
		if part != "" && part != "." {
			filtered = append(filtered, part)
		}
	}

	if len(filtered) == 0 {
		return sessionsDir, nil
	}

	// First part is the session root ID
	root = filepath.Join(sessionsDir, filtered[0])

	// Rest are components
	if len(filtered) > 1 {
		components = filtered[1:]
	}

	return root, components
}

// IsSiblingDir checks if a directory name is a sibling and extracts its parts
func IsSiblingDir(name string) (isSibling bool, messageID string, siblingNum string) {
	// Check for .s. pattern
	parts := strings.Split(name, ".s.")
	if len(parts) != 2 {
		return false, "", ""
	}

	// Validate that the sibling number is hex
	if _, err := strconv.ParseUint(parts[1], 16, 64); err != nil {
		return false, "", ""
	}

	return true, parts[0], parts[1]
}

// GetNextMessageID returns the next available message ID in a directory
func GetNextMessageID(sessionPath string) (string, error) {
	messages, err := ListMessages(sessionPath)
	if err != nil {
		return "", fmt.Errorf("failed to list messages: %w", err)
	}

	// Find the highest numeric ID
	maxID := -1
	for _, msgID := range messages {
		// Skip non-hex IDs
		if id, err := strconv.ParseUint(msgID, 16, 64); err == nil {
			if int(id) > maxID {
				maxID = int(id)
			}
		}
	}

	nextID := maxID + 1

	// Format with appropriate width
	if nextID <= 0xffff {
		return fmt.Sprintf("%04x", nextID), nil
	} else if nextID <= 0xfffff {
		return fmt.Sprintf("%05x", nextID), nil
	} else if nextID <= 0xffffff {
		return fmt.Sprintf("%06x", nextID), nil
	} else if nextID <= 0xfffffff {
		return fmt.Sprintf("%07x", nextID), nil
	} else {
		return fmt.Sprintf("%08x", nextID), nil
	}
}

// GetNextSiblingPath returns the next available sibling path for a message
func GetNextSiblingPath(sessionPath, messageID string) (string, error) {
	siblings, err := findSiblings(sessionPath, messageID)
	if err != nil {
		return "", fmt.Errorf("failed to find siblings: %w", err)
	}

	// Find the highest sibling number
	maxNum := -1
	for _, sibling := range siblings {
		if isSib, msgID, sibNum := IsSiblingDir(sibling); isSib && msgID == messageID {
			if num, err := strconv.ParseUint(sibNum, 16, 64); err == nil {
				if int(num) > maxNum {
					maxNum = int(num)
				}
			}
		}
	}

	nextNum := maxNum + 1
	return fmt.Sprintf("%s.s.%04x", messageID, nextNum), nil
}

// GetMessagePath constructs the full path to a message
func GetMessagePath(sessionPath, messageID string) string {
	return filepath.Join(sessionPath, messageID)
}

// ParseMessageID extracts session path and message ID from a full message path
func ParseMessageID(messageIDPath string) (sessionPath string, messageID string) {
	// First check if it's already an absolute path
	if filepath.IsAbs(messageIDPath) {
		// Split by path separator and find the message ID
		parts := strings.Split(messageIDPath, string(filepath.Separator))
		for i := len(parts) - 1; i >= 0; i-- {
			if _, err := strconv.ParseUint(parts[i], 16, 64); err == nil {
				// Found the message ID
				messageID = parts[i]
				// Reconstruct the session path using platform-agnostic methods
				sessionPath = filepath.Join(parts[:i]...)
				// Handle root directory properly
				if filepath.IsAbs(messageIDPath) && !filepath.IsAbs(sessionPath) {
					// Reconstruct absolute path
					sessionPath = filepath.Join(string(filepath.Separator), sessionPath)
				}
				return sessionPath, messageID
			}
		}
		return messageIDPath, ""
	}

	// For relative paths like "a7c4/0002", split and process
	parts := strings.Split(messageIDPath, "/")
	if len(parts) < 2 {
		// Just a session ID
		return filepath.Join(GetSessionsDir(), messageIDPath), ""
	}

	// Last part should be message ID
	lastPart := parts[len(parts)-1]
	if _, err := strconv.ParseUint(lastPart, 16, 64); err == nil {
		messageID = lastPart
		// Join the rest as session path
		sessionPath = filepath.Join(GetSessionsDir(), strings.Join(parts[:len(parts)-1], "/"))
		return sessionPath, messageID
	}

	// If no valid message ID, treat whole thing as session path
	return filepath.Join(GetSessionsDir(), messageIDPath), ""
}

// GetParentPath returns the parent directory of a session path
func GetParentPath(sessionPath string) string {
	return filepath.Dir(sessionPath)
}

// IsSessionRoot checks if a path is a session root directory
func IsSessionRoot(sessionPath string) bool {
	sessionsDir := GetSessionsDir()
	parent := filepath.Dir(sessionPath)
	return parent == sessionsDir
}

// SanitizeSessionID ensures a session ID is safe for filesystem use
func SanitizeSessionID(id string) string {
	// Remove any path separators or special characters
	id = strings.ReplaceAll(id, string(filepath.Separator), "")
	id = strings.ReplaceAll(id, ".", "")
	id = strings.ReplaceAll(id, " ", "")
	return id
}
