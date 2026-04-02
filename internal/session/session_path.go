package session

import (
	"fmt"
	"lmtools/internal/errors"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseSessionPath breaks down a session path into root and components
func ParseSessionPath(path string) (root string, components []string) {
	return DefaultManager().ParseSessionPath(path)
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
	return formatVariableWidthHexID(nextID), nil
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
	return DefaultManager().ParseMessageID(messageIDPath)
}

// GetParentPath returns the parent directory of a session path
func GetParentPath(sessionPath string) string {
	return filepath.Dir(sessionPath)
}

// IsSessionRoot checks if a path is a session root directory
func IsSessionRoot(sessionPath string) bool {
	return DefaultManager().IsSessionRoot(sessionPath)
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
	return DefaultManager().GetRootSession(sessionPath)
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
