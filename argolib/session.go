package argo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Session represents a conversation session
type Session struct {
	Path string // Directory path (also serves as session ID)
}

// Message represents a single message in a conversation
type Message struct {
	ID        string // Message hex ID (e.g., "0002")
	Role      string // "user" or "assistant"
	Content   string // Message text
	Timestamp time.Time
	Model     string // Model name (empty for user messages)
}

// sessionsBaseDir can be overridden for testing
var sessionsBaseDir string

// GetSessionsDir returns the base directory for all sessions
func GetSessionsDir() string {
	if sessionsBaseDir != "" {
		return sessionsBaseDir
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".argo", "sessions")
	}
	return filepath.Join(homeDir, ".argo", "sessions")
}

// CreateSession creates a new session with a sequential ID
func CreateSession() (*Session, error) {
	sessionsDir := GetSessionsDir()
	if err := os.MkdirAll(sessionsDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}

	// Find the next available sequential ID
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	// Find the highest numeric ID
	maxID := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Try to parse as hex number
		if id, err := strconv.ParseUint(entry.Name(), 16, 64); err == nil {
			if int(id) > maxID {
				maxID = int(id)
			}
		}
	}

	// Try sequential IDs starting from maxID + 1
	for i := maxID + 1; i < maxID+100; i++ {
		// Format with appropriate width based on the number
		var sessionID string
		if i <= 0xffff {
			sessionID = fmt.Sprintf("%04x", i)
		} else if i <= 0xfffff {
			sessionID = fmt.Sprintf("%05x", i)
		} else if i <= 0xffffff {
			sessionID = fmt.Sprintf("%06x", i)
		} else if i <= 0xfffffff {
			sessionID = fmt.Sprintf("%07x", i)
		} else {
			sessionID = fmt.Sprintf("%08x", i)
		}
		sessionPath := filepath.Join(sessionsDir, sessionID)

		// Check if already exists
		if _, err := os.Stat(sessionPath); err == nil {
			// Already exists, try next
			continue
		}

		// Create directory
		if err := os.Mkdir(sessionPath, 0o750); err != nil {
			if os.IsExist(err) {
				// Race condition - someone else created it, try next
				continue
			}
			return nil, fmt.Errorf("failed to create session directory: %w", err)
		}

		return &Session{Path: sessionPath}, nil
	}

	return nil, fmt.Errorf("failed to create session after 100 attempts - too many collisions")
}

// LoadSession loads an existing session by path
func LoadSession(sessionPath string) (*Session, error) {
	// If sessionPath is relative (just ID), make it absolute
	if !filepath.IsAbs(sessionPath) {
		sessionPath = filepath.Join(GetSessionsDir(), sessionPath)
	}

	// Check if session directory exists
	info, err := os.Stat(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session path is not a directory: %s", sessionPath)
	}

	return &Session{Path: sessionPath}, nil
}

// AppendMessage adds a new message to the session
func AppendMessage(session *Session, msg Message) (string, error) {
	// Use session lock to prevent concurrent modifications
	return WithSessionLockT(session.Path, func() (string, error) {
		// Get next message ID
		msgID, err := GetNextMessageID(session.Path)
		if err != nil {
			return "", fmt.Errorf("failed to get next message ID: %w", err)
		}

		msg.ID = msgID
		if err := writeMessage(session.Path, msgID, msg); err != nil {
			return "", fmt.Errorf("failed to write message: %w", err)
		}

		return msgID, nil
	})
}

// CreateSibling creates a new sibling branch from a message
func CreateSibling(sessionPath, messageID string) (string, error) {
	// Ensure sessionPath is absolute
	if !filepath.IsAbs(sessionPath) {
		sessionPath = filepath.Join(GetSessionsDir(), sessionPath)
	}

	// Get the anchor point for creating siblings (implements bubble-up logic)
	anchorPath, anchorID := GetAnchorForBranching(sessionPath, messageID)

	// Get the root session for locking to prevent any concurrent sibling creation
	// within the same session tree
	rootSession := GetRootSession(sessionPath)

	// Use session lock at the root level to prevent concurrent sibling creation
	return WithSessionLockT(rootSession, func() (string, error) {
		// Re-calculate the sibling path inside the lock to ensure consistency
		siblingPath, err := GetNextSiblingPath(anchorPath, anchorID)
		if err != nil {
			return "", fmt.Errorf("failed to get sibling path: %w", err)
		}

		fullPath := filepath.Join(anchorPath, siblingPath)
		if err := os.MkdirAll(fullPath, 0o750); err != nil {
			return "", fmt.Errorf("failed to create sibling directory: %w", err)
		}

		return fullPath, nil
	})
}

// GetLineage returns all messages in the conversation path, handling sibling branches correctly
func GetLineage(sessionPath string) ([]Message, error) {
	// Make the path absolute
	if !filepath.IsAbs(sessionPath) {
		sessionPath = filepath.Join(GetSessionsDir(), sessionPath)
	}

	// Split the path into "root dir" and the list of sibling-components that
	// were traversed to arrive at sessionPath.
	rootDir, components := ParseSessionPath(sessionPath)

	// Helper that loads messages in a dir and returns them **sorted** (the
	// helpers already do this for us).
	load := func(dir string) ([]Message, error) {
		msgs, err := loadMessagesInDir(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to load messages in %s: %w", dir, err)
		}
		return msgs, nil
	}

	var lineage []Message

	// We keep track of the "last assistant we have seen along the way" and
	// whether it has already been added to the lineage slice.  This is needed
	// for the tricky "branch-from-user" rule where no assistant exists in the
	// current directory.
	var lastAssistant *Message
	assistantAlreadyInLineage := false

	dir := rootDir

	for i := 0; ; i++ {
		msgs, err := load(dir)
		if err != nil {
			return nil, err
		}

		// Final directory → include everything and stop
		if i == len(components) {
			lineage = append(lineage, msgs...)
			break
		}

		// We are about to step into a sibling directory.  Find the branch point.
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
			return nil, fmt.Errorf("branch point %s not found in %s", branchMsgID, dir)
		}

		branchMsg := msgs[branchIdx]

		switch branchMsg.Role {
		case "assistant":
			// Regenerating an assistant: keep everything **before** it.
			lineage = append(lineage, msgs[:branchIdx]...)

			// Remember this assistant (it might be needed later by a user branch)
			lastAssistant = &branchMsg
			assistantAlreadyInLineage = false

		case "user":
			// Alternative user input.  We want everything up to and including the
			// *previous* assistant.  Look for it in the same directory first.
			prevAssistIdx := -1
			for j := branchIdx - 1; j >= 0; j-- {
				if msgs[j].Role == "assistant" {
					prevAssistIdx = j
					break
				}
			}

			if prevAssistIdx != -1 {
				// Assistant found in the current directory – easy.
				lineage = append(lineage, msgs[:prevAssistIdx+1]...)
				lastAssistant = &msgs[prevAssistIdx]
				assistantAlreadyInLineage = true
			} else {
				// No assistant in this directory.  We need the *last* assistant
				// we encountered in an ancestor directory (even if it was
				// previously skipped).
				if lastAssistant != nil && !assistantAlreadyInLineage {
					lineage = append(lineage, *lastAssistant)
					assistantAlreadyInLineage = true
				}
				// Nothing else from this directory is kept because the branch
				// starts at the first message.
			}
		default:
			return nil, fmt.Errorf("unknown role %q in message %s", branchMsg.Role, branchMsg.ID)
		}

		// Advance to next directory in the path.
		dir = filepath.Join(dir, comp)
	}

	return lineage, nil
}

// GetSessionID returns the session ID from a full session path
func GetSessionID(sessionPath string) string {
	sessionsDir := GetSessionsDir()
	relPath, err := filepath.Rel(sessionsDir, sessionPath)
	if err != nil {
		// If we can't get relative path, return the base name
		return filepath.Base(sessionPath)
	}
	return relPath
}

// DeleteNode deletes a node (session, branch, or message) and all its descendants
func DeleteNode(nodePath string) error {
	// If nodePath is relative (just ID or partial path), make it absolute
	if !filepath.IsAbs(nodePath) {
		nodePath = filepath.Join(GetSessionsDir(), nodePath)
	}

	// Clean the path to resolve .. components and ensure it's normalized
	nodePath = filepath.Clean(nodePath)

	// Security check: ensure the path is within the sessions directory
	sessionsDir := GetSessionsDir()
	if !strings.HasPrefix(nodePath, sessionsDir+string(filepath.Separator)) && nodePath != sessionsDir {
		return fmt.Errorf("invalid path: must be within sessions directory")
	}

	// First check if the node exists (before acquiring lock)
	// Check if the path exists as a directory
	info, err := os.Stat(nodePath)
	if err == nil && info.IsDir() {
		// It's a directory - we can proceed
	} else {
		// It might be a message path (without file extension)
		// Check if it looks like a message ID
		dir := filepath.Dir(nodePath)
		msgID := filepath.Base(nodePath)

		// Validate it's a valid message ID format
		if !isValidMessageID(msgID) {
			return fmt.Errorf("node not found: %s", nodePath)
		}

		// Check if the message files exist
		contentPath := filepath.Join(dir, msgID+".txt")
		if _, err := os.Stat(contentPath); err != nil {
			return fmt.Errorf("node not found: %s", nodePath)
		}
	}

	// Get root session for locking
	rootSession := GetRootSession(nodePath)

	// Use session lock to prevent concurrent modifications
	return WithSessionLock(rootSession, func() error {
		// Check again if the path exists as a directory
		info, err := os.Stat(nodePath)
		if err == nil && info.IsDir() {
			// It's a directory (session or branch), delete recursively
			return os.RemoveAll(nodePath)
		}

		// It's a message path
		dir := filepath.Dir(nodePath)
		msgID := filepath.Base(nodePath)

		// Parse message number
		var msgNum int
		if _, err := fmt.Sscanf(msgID, "%x", &msgNum); err != nil {
			return fmt.Errorf("invalid message ID: %s", msgID)
		}

		// Delete the message and all descendants
		return deleteMessageAndDescendants(dir, msgNum)
	})
}
