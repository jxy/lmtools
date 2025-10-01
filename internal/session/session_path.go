package session

import (
	"fmt"
	"lmtools/internal/errors"
	"os"
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
	// KISS: Only scan the current directory's JSON filenames
	// Message IDs only need to be unique within the current directory.
	// Sibling directories can reuse IDs safely because they're separate namespaces.
	msgs, err := listMessages(sessionPath)
	if err != nil {
		return "", errors.WrapError("list messages", err)
	}

	// Find the highest numeric ID
	maxID := -1
	for _, msgID := range msgs {
		// Skip non-hex IDs
		if id, err := strconv.ParseUint(msgID, 16, 64); err == nil {
			if int(id) > maxID {
				maxID = int(id)
			}
		}
	}

	nextID := maxID + 1
	// Format with appropriate width
	switch {
	case nextID <= 0xffff:
		return fmt.Sprintf("%04x", nextID), nil
	case nextID <= 0xfffff:
		return fmt.Sprintf("%05x", nextID), nil
	case nextID <= 0xffffff:
		return fmt.Sprintf("%06x", nextID), nil
	case nextID <= 0xfffffff:
		return fmt.Sprintf("%07x", nextID), nil
	default:
		return fmt.Sprintf("%08x", nextID), nil
	}
}

// GetNextSiblingPath returns the next available sibling path for a message
func GetNextSiblingPath(sessionPath, messageID string) (string, error) {
	siblings, err := findSiblings(sessionPath, messageID)
	if err != nil {
		return "", errors.WrapError("find siblings", err)
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
		// Check if it's a directory (session path without message ID)
		if info, err := os.Stat(messageIDPath); err == nil && info.IsDir() {
			return messageIDPath, ""
		}

		// Split by path separator and find the message ID
		parts := strings.Split(messageIDPath, string(filepath.Separator))
		for i := len(parts) - 1; i >= 0; i-- {
			// Use strict validation for message IDs
			if IsValidMessageID(parts[i]) {
				// Found a valid message ID
				messageID = parts[i]
				// Reconstruct the session path using platform-agnostic methods
				sessionPath = filepath.Join(parts[:i]...)
				// Handle root directory properly
				if filepath.IsAbs(messageIDPath) && !filepath.IsAbs(sessionPath) {
					// Reconstruct absolute path
					sessionPath = filepath.Join(string(filepath.Separator), sessionPath)
				}

				// Special check: if this is a direct child of sessions dir and the path exists as a directory,
				// it's a session ID, not a message ID
				sessionsDir := GetSessionsDir()
				if strings.HasPrefix(sessionPath, sessionsDir+string(filepath.Separator)) || sessionPath == sessionsDir {
					// Check if this path exists as a directory
					if info, err := os.Stat(messageIDPath); err == nil && info.IsDir() {
						// It's a session directory
						return messageIDPath, ""
					}
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
	if IsValidMessageID(lastPart) {
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

// isValidSessionID checks if a string could be a valid session ID
func isValidSessionID(id string) bool {
	// Session IDs are hex strings of varying lengths
	if id == "" {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// GetRootSession returns the root session directory from any nested path
func GetRootSession(sessionPath string) string {
	// Parse the session path to get the root directory
	rootDir, _ := ParseSessionPath(sessionPath)
	return rootDir
}

// GetAnchorForBranching determines the appropriate location for creating a sibling
// If we're already in a sibling directory, it bubbles up through ALL sibling directories
// until it reaches a non-sibling directory or the root
func GetAnchorForBranching(sessionPath string, messageID string) (anchorPath string, anchorID string) {
	// Parse the session path to understand its structure
	rootDir, components := ParseSessionPath(sessionPath)

	// Start from the end and bubble up through all sibling directories
	lastOriginalMsgID := messageID
	bubbledComponents := components // Start with all components

	// Iterate from the last component backwards
	for i := len(components) - 1; i >= 0; i-- {
		isSib, originalMsgID, _ := IsSiblingDir(components[i])

		if isSib {
			// This is a sibling directory, remember the original message ID
			lastOriginalMsgID = originalMsgID
			// Continue bubbling up by removing this component
			bubbledComponents = components[:i]
		} else {
			// Not a sibling directory, stop bubbling here
			break
		}
	}

	// Reconstruct the anchor path
	if len(bubbledComponents) == 0 {
		// Bubbled all the way to root
		anchorPath = rootDir
	} else {
		// Reconstruct path from remaining components
		anchorPath = rootDir
		for _, comp := range bubbledComponents {
			anchorPath = filepath.Join(anchorPath, comp)
		}
	}

	return anchorPath, lastOriginalMsgID
}

// IsValidMessageID checks if a string is a valid message ID format (4-8 lowercase hex digits)
func IsValidMessageID(s string) bool {
	// Message IDs can be 4-8 characters long (to handle overflow past ffff)
	if len(s) < 4 || len(s) > 8 {
		return false
	}
	// Only lowercase hex characters are allowed
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// IsMessageReference checks if the ID is a message reference (not a session ID)
// Message references end with /XXXX where XXXX doesn't contain .s.
// Examples:
//   - "0001" → false (session ID)
//   - "0001.s.0002" → false (sibling session ID)
//   - "0001/0002" → true (message reference)
//   - "0001/0002.s.0003" → false (sibling session ID)
//   - "0001/0002.s.0003/0004" → true (message reference)
func IsMessageReference(id string) bool {
	if !strings.Contains(id, "/") {
		return false
	}

	parts := strings.Split(id, "/")
	lastPart := parts[len(parts)-1]

	// If last part contains .s., it's a sibling session, not a message
	if strings.Contains(lastPart, ".s.") {
		return false
	}

	// Check if it's a valid hex ID (4-8 chars)
	return IsValidMessageID(lastPart)
}
