package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	// ErrMaxRetriesExceeded is returned when AppendMessage fails after maximum retry attempts
	ErrMaxRetriesExceeded = errors.New("exceeded maximum retry attempts")
	// ErrSiblingOverflow is returned when too many sibling branches exist
	ErrSiblingOverflow = errors.New("too many sibling branches")
)

// Session represents a conversation session
type Session struct {
	Path        string // Directory path (also serves as session ID)
	SessionsDir string // Base directory for sessions (optional, defaults to GetSessionsDir())
}

// flockChecked tracks whether we've already tested flock support
var flockChecked bool

// TestFlockSupport checks if the filesystem supports flock
func TestFlockSupport() error {
	// Ensure ~/.lmc exists
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	lmcDir := filepath.Join(homeDir, ".lmc")
	if err := os.MkdirAll(lmcDir, 0o750); err != nil {
		return fmt.Errorf("failed to create lmc directory: %w", err)
	}

	// Create test file in ~/.lmc
	testFile, err := os.CreateTemp(lmcDir, ".flock-test-*")
	if err != nil {
		return fmt.Errorf("failed to create test file: %w", err)
	}
	defer os.Remove(testFile.Name())
	defer testFile.Close()

	// Test flock
	fd := int(testFile.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("flock not supported on %s: %w", lmcDir, err)
	}

	// Clean unlock
	if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
		return fmt.Errorf("flock unlock failed: %w", err)
	}

	return nil
}

// Message represents a single message in a conversation
type Message struct {
	ID        string // Message hex ID (e.g., "0002")
	Role      string // "user" or "assistant"
	Content   string // Message text
	Timestamp time.Time
	Model     string // Model name (empty for user messages)
}

// sessionsBaseDir can be overridden for testing or custom directory
var (
	sessionsBaseDir string
	sessionsDirMu   sync.RWMutex
	skipFlockCheck  bool // Skip file locking check if set
)

// SetSessionsDir sets a custom sessions directory
func SetSessionsDir(dir string) {
	sessionsDirMu.Lock()
	defer sessionsDirMu.Unlock()
	sessionsBaseDir = dir
}

// SetSkipFlockCheck sets whether to skip the file locking check
func SetSkipFlockCheck(skip bool) {
	skipFlockCheck = skip
}

// GetSessionsDir returns the base directory for all sessions
func GetSessionsDir() string {
	sessionsDirMu.RLock()
	baseDir := sessionsBaseDir
	sessionsDirMu.RUnlock()

	if baseDir != "" {
		return baseDir
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".lmc", "sessions")
	}
	return filepath.Join(homeDir, ".lmc", "sessions")
}

// CreateSession creates a new session with a sequential ID
func CreateSession() (*Session, error) {
	// Test flock support once (only on first session creation)
	if !flockChecked {
		// Skip check if flag is set
		if !skipFlockCheck {
			if err := TestFlockSupport(); err != nil {
				logger.Warnf("File locking may not work properly: %v", err)
				logger.Warnf("Concurrent access to sessions may cause issues")
				// Continue anyway - some users might not need locking
			}
		}
		flockChecked = true
	}

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

		return &Session{Path: sessionPath, SessionsDir: sessionsDir}, nil
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

	// Determine which sessions directory was used
	sessionsDir := GetSessionsDir()
	return &Session{Path: sessionPath, SessionsDir: sessionsDir}, nil
}

// AppendMessage atomically appends a message to the session
// Returns: (finalPath, messageID, error)
func AppendMessage(session *Session, msg Message) (string, string, error) {
	const maxRetries = 10

	// 1. Create temp files ONCE before any locks
	tempTxtFile, err := os.CreateTemp(session.Path, ".msg-*.txt.tmp")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp txt: %w", err)
	}
	tempTxtPath := tempTxtFile.Name()
	defer func() { _ = os.Remove(tempTxtPath) }()

	// Write content and close immediately to avoid FD exhaustion
	if _, err := tempTxtFile.Write([]byte(msg.Content)); err != nil {
		tempTxtFile.Close()
		return "", "", fmt.Errorf("failed to write content: %w", err)
	}
	if err := tempTxtFile.Close(); err != nil {
		return "", "", fmt.Errorf("failed to close txt: %w", err)
	}

	// Create JSON temp file
	tempJsonFile, err := os.CreateTemp(session.Path, ".msg-*.json.tmp")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp json: %w", err)
	}
	tempJsonPath := tempJsonFile.Name()
	defer func() { _ = os.Remove(tempJsonPath) }()

	// Prepare metadata
	var modelPtr *string
	if msg.Model != "" {
		modelPtr = &msg.Model
	}
	metadata := MessageMetadata{
		Role:      msg.Role,
		Timestamp: msg.Timestamp,
		Model:     modelPtr,
	}

	// Write metadata and close
	encoder := json.NewEncoder(tempJsonFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(metadata); err != nil {
		tempJsonFile.Close()
		return "", "", fmt.Errorf("failed to encode metadata: %w", err)
	}
	if err := tempJsonFile.Close(); err != nil {
		return "", "", fmt.Errorf("failed to close json: %w", err)
	}

	// 2. Try to place files in correct location
	currentPath := session.Path
	var lastConflictMsgID string

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Optional exponential backoff after 2 retries
		if attempt > 2 {
			time.Sleep(time.Duration(1<<(attempt-2)) * 10 * time.Millisecond)
		}

		success, msgID, siblingPath, err := tryPlaceMessage(currentPath, tempTxtPath, tempJsonPath)
		if err != nil {
			// Retry on lock timeout
			if errors.Is(err, ErrLockTimeout) && attempt < maxRetries-1 {
				// Debug logging would go here if we had access to config
				continue
			}
			return "", "", err
		}

		if success {
			return currentPath, msgID, nil
		}

		// Track conflict for error reporting
		lastConflictMsgID = msgID

		// Always log conflicts - they're important operational events
		logger.Infof("Message ID conflict: %s exists in %s, using sibling %s",
			msgID, GetSessionID(currentPath), GetSessionID(siblingPath))

		// Move to sibling for next attempt
		currentPath = siblingPath
	}

	// Max retries exceeded - provide detailed error
	return "", "", fmt.Errorf(
		"%w: gave up after %d attempts\n"+
			"Last conflict: message ID '%s' already exists in %s\n"+
			"Your content has been preserved in temporary files:\n"+
			"  Content: %s\n"+
			"  Metadata: %s\n"+
			"Options:\n"+
			"  1. Retry with: echo <your_message> | lmc -resume %s\n"+
			"  2. Manually move files to session directory with next available ID\n"+
			"  3. Check for concurrent processes that may be writing to this session",
		ErrMaxRetriesExceeded, maxRetries, lastConflictMsgID,
		GetSessionID(currentPath), tempTxtPath, tempJsonPath,
		GetSessionID(session.Path))
}

// CreateSibling creates a new sibling branch from a message
// WARNING: This function acquires a lock on the ROOT session path to ensure
// atomic creation of sibling branches across the entire session tree.
// DO NOT call this function while holding a lock on a child session path,
// as it will create a high risk of deadlocks (AB-BA lock acquisition).
// Lock hierarchy: root lock may acquire child locks, but not vice versa.
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
	// Use retry logic to allow multiple goroutines to eventually succeed
	return WithSessionLockT(rootSession, 5*time.Second, func() (string, error) {
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
	return WithSessionLock(rootSession, 0, func() error {
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

// tryPlaceMessage attempts to atomically place message files in a session directory
// Returns: (success, msgID, siblingPath, error)
// This function is called from AppendMessage and handles the lock/check/rename logic
func tryPlaceMessage(sessionPath, tempTxtPath, tempJsonPath string) (success bool, msgID string, siblingPath string, err error) {
	// Phase 1: Check if we can place files (with lock)
	var needSibling bool
	var conflictMsgID string

	err = WithSessionLock(sessionPath, 5*time.Second, func() error {
		// Get next ID inside lock (reduces conflicts)
		msgID, err = GetNextMessageID(sessionPath)
		if err != nil {
			return err
		}

		finalTxtPath := filepath.Join(sessionPath, msgID+".txt")
		finalJsonPath := filepath.Join(sessionPath, msgID+".json")

		// Use proper error checking
		_, errTxt := os.Stat(finalTxtPath)
		_, errJson := os.Stat(finalJsonPath)

		txtExists := errTxt == nil || !errors.Is(errTxt, fs.ErrNotExist)
		jsonExists := errJson == nil || !errors.Is(errJson, fs.ErrNotExist)

		if txtExists || jsonExists {
			// Conflict detected
			needSibling = true
			conflictMsgID = msgID
			success = false
			return nil
		}

		// No conflict - atomic rename
		if err := os.Rename(tempTxtPath, finalTxtPath); err != nil {
			return fmt.Errorf("failed to rename txt: %w", err)
		}

		if err := os.Rename(tempJsonPath, finalJsonPath); err != nil {
			// CRITICAL: Rename back, not delete!
			// This preserves user content for retry
			rollbackErr := os.Rename(finalTxtPath, tempTxtPath)

			if rollbackErr != nil {
				// Critical failure - both json rename and rollback failed
				return fmt.Errorf(
					"CRITICAL: Failed to place message files\n"+
						"  JSON rename error: %w\n"+
						"  Rollback error: %w\n"+
						"Files in inconsistent state:\n"+
						"  ✓ Created: %s\n"+
						"  ✗ Failed: %s\n"+
						"  ! Orphaned: %s\n"+
						"Manual intervention required to recover temp file: %s",
					err, rollbackErr, finalTxtPath, finalJsonPath, finalTxtPath, tempJsonPath)
			}

			// Rollback successful
			return fmt.Errorf(
				"failed to place message (rolled back successfully)\n"+
					"  Error: %w\n"+
					"Your content is preserved in:\n"+
					"  Text: %s\n"+
					"  JSON: %s\n"+
					"You can retry the operation or manually move these files",
				err, tempTxtPath, tempJsonPath)
		}

		success = true
		return nil
	})
	// LOCK RELEASED HERE!
	if err != nil {
		return false, "", "", err
	}

	// Phase 2: Create sibling if needed (NO LOCK - avoids deadlock)
	if needSibling {
		siblingPath, err = CreateSibling(sessionPath, conflictMsgID)
		if err != nil {
			return false, "", "", fmt.Errorf("failed to create sibling: %w", err)
		}
	}

	return success, msgID, siblingPath, nil
}
