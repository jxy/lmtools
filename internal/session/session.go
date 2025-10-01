package session

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/errors"
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
	// ErrMaxRetriesExceeded is returned when AppendMessageWithToolInteraction fails after maximum retry attempts
	ErrMaxRetriesExceeded = stdErrors.New("exceeded maximum retry attempts")
	// ErrSiblingOverflow is returned when too many sibling branches exist
	ErrSiblingOverflow = stdErrors.New("too many sibling branches")
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
	// Use the sessions directory for testing
	sessionsDir := GetSessionsDir()
	// Ensure the parent directory exists
	parentDir := filepath.Dir(sessionsDir)
	if err := os.MkdirAll(parentDir, constants.DirPerm); err != nil {
		return errors.WrapError("create parent directory", err)
	}

	// Create test file in the parent directory
	testFile, err := os.CreateTemp(parentDir, ".flock-test-*")
	if err != nil {
		return errors.WrapError("create test file", err)
	}
	defer os.Remove(testFile.Name())
	defer testFile.Close()

	// Test flock
	fd := int(testFile.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.WrapError("flock test", err)
	}

	// Clean unlock
	if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
		return errors.WrapError("flock unlock", err)
	}

	return nil
}

// Message represents a single message in a conversation
type Message struct {
	ID        string    // Message hex ID (e.g., "0002")
	Role      core.Role // "user" or "assistant"
	Content   string    // Message text
	Timestamp time.Time
	Model     string // Model name (empty for user messages)
}

// SaveResult represents the result of a session save operation
type SaveResult struct {
	Path      string // Final path of saved message
	MessageID string // Unique message identifier
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
// If systemPrompt is not empty, it will be saved as message 0000
func CreateSession(systemPrompt string, log core.Logger) (*Session, error) {
	// Test flock support once (only on first session creation)
	if !flockChecked {
		// Skip check if flag is set
		if !skipFlockCheck {
			if err := TestFlockSupport(); err != nil {
				if log != nil {
					log.Debugf("File locking may not work properly: %v", err)
					log.Debugf("Concurrent access to sessions may cause issues")
				}
				// Continue anyway - some users might not need locking
			}
		}
		flockChecked = true
	}

	sessionsDir := GetSessionsDir()
	if err := os.MkdirAll(sessionsDir, constants.DirPerm); err != nil {
		return nil, errors.WrapError("create sessions directory", err)
	}

	// Find the next available sequential ID
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil, errors.WrapError("read sessions directory", err)
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
		if err := os.Mkdir(sessionPath, constants.DirPerm); err != nil {
			if os.IsExist(err) {
				// Race condition - someone else created it, try next
				continue
			}
			return nil, errors.WrapError("create session directory", err)
		}

		session := &Session{Path: sessionPath, SessionsDir: sessionsDir}

		// Save system message if provided
		if systemPrompt != "" {
			if err := saveSystemMessage(session, systemPrompt); err != nil {
				// Log error but don't fail session creation
				log.Debugf("Failed to save system message: %v", err)
			}
		}

		return session, nil
	}

	return nil, fmt.Errorf("failed to create session after 100 attempts: too many collisions")
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
		return nil, errors.WrapError("find session", err)
	}
	if !info.IsDir() {
		return nil, errors.WrapError("validate session path", stdErrors.New("session path is not a directory: "+sessionPath))
	}

	// Determine which sessions directory was used
	sessionsDir := GetSessionsDir()
	return &Session{Path: sessionPath, SessionsDir: sessionsDir}, nil
}

// MaybeForkForSystem checks if the session needs forking due to system prompt change
// and creates a fork if necessary. Returns the (possibly new) session and whether
// a fork was created.
func MaybeForkForSystem(ctx context.Context, sess *Session, effectiveSystem string) (*Session, bool, error) {
	// Get the original system message from the session
	originalSystemMsg, err := GetSystemMessage(sess.Path)
	if err != nil {
		return nil, false, errors.WrapError("get system message from session", err)
	}

	// Single rule: fork if the effective system prompt differs from the original
	needFork := false
	if originalSystemMsg == nil && effectiveSystem != "" {
		// Original has no system message, but we're setting one
		needFork = true
	} else if originalSystemMsg != nil && *originalSystemMsg != effectiveSystem {
		// System messages differ
		needFork = true
	}

	if !needFork {
		return sess, false, nil
	}

	// Fork the session with new system message
	originalID := GetSessionID(sess.Path)
	logger.From(ctx).Infof("Forking session %s due to system prompt change", originalID)

	// Fork the session, preserving the lineage with new system prompt
	newSession, err := ForkSessionWithSystemMessage(ctx, sess.Path, &effectiveSystem)
	if err != nil {
		return nil, false, errors.WrapError("create forked session", err)
	}

	logger.From(ctx).Infof("Created forked session %s from %s with new system prompt",
		GetSessionID(newSession.Path), originalID)

	return newSession, true, nil
}

// CreateSibling creates a new sibling branch from a message
// WARNING: This function acquires a lock on the ROOT session path to ensure
// atomic creation of sibling branches across the entire session tree.
// DO NOT call this function while holding a lock on a child session path,
// as it will create a high risk of deadlocks (AB-BA lock acquisition).
// Lock hierarchy: root lock may acquire child locks, but not vice versa.
func CreateSibling(ctx context.Context, sessionPath, messageID string) (string, error) {
	// Validate messageID is not empty
	if messageID == "" {
		return "", errors.WrapError("validate message ID", stdErrors.New("messageID cannot be empty"))
	}

	// Ensure sessionPath is absolute
	if !filepath.IsAbs(sessionPath) {
		sessionPath = filepath.Join(GetSessionsDir(), sessionPath)
	}

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
			return nil, errors.WrapError("load messages in "+dir, err)
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
			return nil, errors.WrapError("find branch point", fmt.Errorf("branch point %s not found in %s", branchMsgID, dir))
		}

		branchMsg := msgs[branchIdx]

		switch branchMsg.Role {
		case core.RoleAssistant:
			// Regenerating an assistant: keep everything **before** it.
			lineage = append(lineage, msgs[:branchIdx]...)

			// Remember this assistant (it might be needed later by a user branch)
			lastAssistant = &branchMsg
			assistantAlreadyInLineage = false

		case core.RoleUser:
			// Alternative user input.  We want everything up to and including the
			// *previous* assistant.  Look for it in the same directory first.
			prevAssistIdx := -1
			for j := branchIdx - 1; j >= 0; j-- {
				if msgs[j].Role == core.RoleAssistant {
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
			return nil, errors.WrapError("validate message role", fmt.Errorf("unknown role %q in message %s", branchMsg.Role, branchMsg.ID))
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
		return errors.WrapError("validate path", stdErrors.New("invalid path: must be within sessions directory"))
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
		if !IsValidMessageID(msgID) {
			return errors.WrapError("delete node", fmt.Errorf("node not found: %s", nodePath))
		}

		// Check if the message files exist
		metaPath := filepath.Join(dir, msgID+".json")
		if _, err := os.Stat(metaPath); err != nil {
			return errors.WrapError("delete node", fmt.Errorf("node not found: %s", nodePath))
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
			return errors.WrapError("validate message ID", fmt.Errorf("invalid message ID: %s", msgID))
		}

		// Delete the message and all descendants
		return deleteMessageAndDescendants(dir, msgNum)
	})
}

// saveSystemMessage saves the system prompt as message 0000
func saveSystemMessage(session *Session, systemPrompt string) error {
	// Write content file
	contentPath := filepath.Join(session.Path, "0000.txt")
	if err := os.WriteFile(contentPath, []byte(systemPrompt), constants.FilePerm); err != nil {
		return errors.WrapError("write system content", err)
	}

	// Write metadata file
	metadata := MessageMetadata{
		Role:      "system",
		Timestamp: time.Now(),
		Model:     nil,
	}

	metadataPath := filepath.Join(session.Path, "0000.json")
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return errors.WrapError("marshal system metadata", err)
	}

	if err := os.WriteFile(metadataPath, metadataBytes, constants.FilePerm); err != nil {
		return errors.WrapError("write system metadata", err)
	}

	return nil
}

// GetSystemMessage reads the system message from a session if it exists
func GetSystemMessage(sessionPath string) (*string, error) {
	// Make path absolute if needed
	if !filepath.IsAbs(sessionPath) {
		sessionPath = filepath.Join(GetSessionsDir(), sessionPath)
	}

	// Check if system message files exist
	contentPath := filepath.Join(sessionPath, "0000.txt")
	metadataPath := filepath.Join(sessionPath, "0000.json")

	// Check metadata file exists and has system role
	if metaBytes, err := os.ReadFile(metadataPath); err == nil {
		var metadata MessageMetadata
		if err := json.Unmarshal(metaBytes, &metadata); err == nil && metadata.Role == core.RoleSystem {
			// Read the content
			if content, err := os.ReadFile(contentPath); err == nil {
				systemMsg := string(content)
				return &systemMsg, nil
			}
		}
	}

	// No system message found
	return nil, nil
}

// ForkSessionWithSystemMessage creates a new session by copying an existing one with a new system message
func ForkSessionWithSystemMessage(ctx context.Context, originalPath string, newSystemPrompt *string) (*Session, error) {
	// Make path absolute if needed
	if !filepath.IsAbs(originalPath) {
		originalPath = filepath.Join(GetSessionsDir(), originalPath)
	}

	// Create new session
	var newSession *Session
	var err error
	if newSystemPrompt != nil {
		newSession, err = CreateSession(*newSystemPrompt, logger.From(ctx))
	} else {
		newSession, err = CreateSession("", logger.From(ctx))
	}
	if err != nil {
		return nil, errors.WrapError("create new session", err)
	}

	// Get lineage from original session (all messages including traversing branches)
	messages, err := GetLineage(originalPath)
	if err != nil {
		// Clean up the created session on error
		os.RemoveAll(newSession.Path)
		return nil, errors.WrapError("get lineage from original session", err)
	}

	// To properly map messages to their directories, we need to walk the same path
	// that GetLineage walked. GetLineage returns messages in root-to-leaf order,
	// properly handling branch points. We'll build our index by walking the same path.
	rootDir, components := ParseSessionPath(originalPath)

	// Build a map of which messages belong to which directory by walking the lineage path
	msgIndex := make(map[string]string)

	// Start at root
	currentDir := rootDir

	// Add root directory messages
	if rootMsgs, err := listMessages(currentDir); err == nil {
		for _, msgID := range rootMsgs {
			msgIndex[msgID] = currentDir
		}
	}

	// Walk through each component (sibling directory) in order
	for _, comp := range components {
		currentDir = filepath.Join(currentDir, comp)

		// Add messages from this directory
		if msgs, err := listMessages(currentDir); err == nil {
			for _, msgID := range msgs {
				// Later directories override earlier ones for the same ID
				// This matches how GetLineage selects messages
				msgIndex[msgID] = currentDir
			}
		}
	}

	// Copy non-system messages to the new session
	for _, msg := range messages {
		// Skip system messages from the original session
		if msg.Role == core.RoleSystem {
			continue
		}

		// Copy the message to the new session using staging/commit pattern
		// First, check if there are tool interactions to copy
		var toolInteraction *core.ToolInteraction

		// Only load tool interactions from the main lineage, not from sibling branches
		// This prevents tool interactions from regenerated messages from being copied
		originalMsgPath := msgIndex[msg.ID]
		logger.From(ctx).Debugf("Processing message %s (role=%s) from path %s", msg.ID, msg.Role, originalMsgPath)

		if originalMsgPath != "" {
			// Check if this message has a tool file in its actual location
			toolPath := filepath.Join(originalMsgPath, msg.ID+".tools.json")
			if _, err := os.Stat(toolPath); err == nil {
				// Use LoadToolInteraction to properly load tool data
				ti, err := LoadToolInteraction(originalMsgPath, msg.ID)
				if err != nil {
					// Log error but continue - missing tool data shouldn't fail the fork
					logger.From(ctx).Debugf("Failed to load tool interaction for message %s: %v", msg.ID, err)
				} else if ti != nil {
					toolInteraction = ti
					logger.From(ctx).Debugf("Loaded tool interaction for message %s: %d calls, %d results",
						msg.ID, len(ti.Calls), len(ti.Results))
				}
			} else {
				logger.From(ctx).Debugf("No tool file found for message %s at %s", msg.ID, toolPath)
			}
		}

		// Create a new message without the ID to let the system assign a new one
		newMsg := Message{
			Role:      msg.Role,
			Content:   msg.Content,
			Timestamp: msg.Timestamp,
			Model:     msg.Model,
		}

		// Stage the message files
		staged, err := stageMessageFiles(newSession.Path, newMsg, toolInteraction)
		if err != nil {
			os.RemoveAll(newSession.Path)
			return nil, errors.WrapError("stage message", err)
		}

		// Use messageCommitter to place the message files atomically
		mc := newMessageCommitter(newSession.Path)
		newMsgID, needSibling, _, err := mc.Commit(ctx, staged)
		staged.Close() // Clean up staging files immediately

		if err != nil {
			os.RemoveAll(newSession.Path)
			return nil, errors.WrapError("place message", err)
		}
		if needSibling {
			// This should not happen in a new session
			os.RemoveAll(newSession.Path)
			return nil, errors.WrapError("copy message", stdErrors.New("unexpected conflict when copying message"))
		}

		logger.From(ctx).Debugf("Copied message %s -> %s (role=%s, hasTools=%v)",
			msg.ID, newMsgID, msg.Role, toolInteraction != nil)
	}

	return newSession, nil
}

// indexMessageDirectories builds a map of message ID to directory path for efficient lookups.
// This function walks the session tree once and returns a map where keys are message IDs
// and values are the directory paths containing those messages.
// This is used to optimize BuildMessagesWithToolInteractions from O(n²) to O(n).
func indexMessageDirectories(sessionPath string) (map[string]string, error) {
	index := make(map[string]string)

	// Helper function to recursively index a directory
	var indexDir func(dirPath string) error
	indexDir = func(dirPath string) error {
		// List all messages in this directory
		msgIDs, err := listMessages(dirPath)
		if err != nil {
			return errors.WrapError("list messages in "+dirPath, err)
		}

		// Add each message to the index
		for _, msgID := range msgIDs {
			index[msgID] = dirPath
		}

		// Check for sibling directories
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			return errors.WrapError(fmt.Sprintf("read directory %s", dirPath), err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if isSibling, _, _ := IsSiblingDir(entry.Name()); isSibling {
				// Recursively index sibling directory
				sibPath := filepath.Join(dirPath, entry.Name())
				if err := indexDir(sibPath); err != nil {
					return err
				}
			}
		}

		return nil
	}

	// Start indexing from the root session path
	if err := indexDir(sessionPath); err != nil {
		return nil, err
	}

	return index, nil
}
