package session

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MessageMetadata represents the JSON metadata for a message
type MessageMetadata struct {
	Role      core.Role `json:"role"`
	Timestamp time.Time `json:"timestamp"`
	Model     *string   `json:"model"`
}

func buildMessageMetadata(msg Message) MessageMetadata {
	metadata := MessageMetadata{
		Role:      msg.Role,
		Timestamp: msg.Timestamp,
	}
	if msg.Model != "" {
		metadata.Model = &msg.Model
	}
	return metadata
}

// writeMessage atomically writes a message to disk
func writeMessage(sessionPath, msgID string, msg Message) error {
	// Write content file
	contentPath := filepath.Join(sessionPath, msgID+".txt")
	if err := writeFileAtomic(contentPath, []byte(msg.Content)); err != nil {
		return errors.WrapError("write message content", err)
	}

	// Write metadata file
	metaPath := filepath.Join(sessionPath, msgID+".json")
	metaData, err := json.MarshalIndent(buildMessageMetadata(msg), "", "  ")
	if err != nil {
		return errors.WrapError("marshal metadata", err)
	}

	if err := writeFileAtomic(metaPath, metaData); err != nil {
		// Try to clean up content file
		os.Remove(contentPath)
		return errors.WrapError("write metadata", err)
	}

	return nil
}

// readMessage reads a message from disk
// Invariant: A message exists if and only if its .json exists.
// .txt may be missing (e.g., tool-only messages), .tools.json is optional.
func readMessage(sessionPath, msgID string) (*Message, error) {
	// Read metadata first (always required)
	metaPath := filepath.Join(sessionPath, msgID+".json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, errors.WrapError("read metadata", err)
	}

	var metadata MessageMetadata
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		return nil, errors.WrapError("unmarshal metadata", err)
	}

	// Read content if it exists (some messages like tool results might not have text)
	var content string
	contentPath := filepath.Join(sessionPath, msgID+".txt")
	if _, err := os.Stat(contentPath); err == nil {
		contentBytes, err := os.ReadFile(contentPath)
		if err != nil {
			return nil, errors.WrapError("read content", err)
		}
		content = string(contentBytes)
	}

	msg := &Message{
		ID:        msgID,
		Role:      metadata.Role,
		Content:   content,
		Timestamp: metadata.Timestamp,
	}

	if metadata.Model != nil {
		msg.Model = *metadata.Model
	}

	return msg, nil
}

// writeFileAtomic writes data to a file atomically using rename
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	// Create temp file in same directory
	tmpFile, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return errors.WrapError("create temp file", err)
	}
	tmpPath := tmpFile.Name()

	// Clean up on error
	defer func() {
		if tmpFile != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
		}
	}()

	// Write data
	if _, err := tmpFile.Write(data); err != nil {
		return errors.WrapError("write temp file", err)
	}

	// Sync to disk
	if err := tmpFile.Sync(); err != nil {
		return errors.WrapError("sync temp file", err)
	}

	// Close before rename
	if err := tmpFile.Close(); err != nil {
		return errors.WrapError("close temp file", err)
	}
	tmpFile = nil // Prevent defer cleanup

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		return errors.WrapError("rename temp file", err)
	}

	return nil
}

// listMessages returns all message IDs in a directory, sorted
// Invariant: A message exists if and only if its .json exists.
// .txt and .tools.json are optional adjuncts to the message.
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
		// Look for .json files (metadata) which every message has
		// Skip .tools.json files as they're part of the message, not the ID
		if strings.HasSuffix(name, ".json") && !strings.Contains(name, ".tools.") {
			msgID := strings.TrimSuffix(name, ".json")
			// Per documentation: "A message exists if and only if its JSON file exists"
			// No need to check for content files - JSON presence is authoritative
			messageIDs[msgID] = true
		}
	}

	// Convert to sorted slice
	result := make([]string, 0, len(messageIDs))
	for msgID := range messageIDs {
		result = append(result, msgID)
	}
	// Sort numerically to ensure correct ordering even after hex overflow
	sort.Slice(result, func(i, j int) bool {
		a, _ := strconv.ParseUint(result[i], 16, 64)
		b, _ := strconv.ParseUint(result[j], 16, 64)
		return a < b
	})

	return result, nil
}

// loadMessagesInDir loads all messages from a directory
func loadMessagesInDir(dirPath string) ([]Message, error) {
	msgIDs, err := listMessages(dirPath)
	if err != nil {
		return nil, err
	}

	messages := make([]Message, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		msg, err := readMessage(dirPath, msgID)
		if err != nil {
			// Skip corrupted messages silently - caller can decide to log
			continue
		}
		messages = append(messages, *msg)
	}

	return messages, nil
}

// findSiblings returns all sibling directories for a given message ID
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

// IsAssistantMessage checks if the given branch path points to an assistant message
func IsAssistantMessage(branchPath string) (bool, error) {
	if branchPath == "" {
		return false, errors.WrapError("validate branch path", stdErrors.New("branch path cannot be empty"))
	}

	sessionPath, messageID := ParseMessageID(branchPath)
	if messageID == "" {
		// Not a message path, but this is not necessarily an error
		// The path might be a session directory
		return false, nil
	}

	// Ensure sessionPath is absolute
	if !filepath.IsAbs(sessionPath) {
		sessionPath = filepath.Join(GetSessionsDir(), sessionPath)
	}

	msg, err := readMessage(sessionPath, messageID)
	if err != nil {
		return false, errors.WrapError("read message "+messageID, err)
	}

	return msg.Role == core.RoleAssistant, nil
}

// deleteMessageAndDescendants deletes a message and all subsequent messages/branches
func deleteMessageAndDescendants(dirPath string, msgNum int) error {
	// Get all messages in the directory
	msgIDs, err := listMessages(dirPath)
	if err != nil {
		return errors.WrapError("list messages", err)
	}

	// Delete the target message and all subsequent messages
	for _, msgID := range msgIDs {
		num, err := strconv.ParseUint(msgID, 16, 64)
		if err != nil {
			continue
		}

		if int(num) >= msgNum {
			// Delete message files
			contentPath := filepath.Join(dirPath, msgID+".txt")
			metaPath := filepath.Join(dirPath, msgID+".json")
			toolsPath := filepath.Join(dirPath, msgID+".tools.json")

			// Delete content file (ignore if doesn't exist)
			_ = os.Remove(contentPath)

			// Delete tools file (ignore if doesn't exist)
			_ = os.Remove(toolsPath)

			// Delete metadata file last to maintain atomicity invariant
			if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
				return errors.WrapError("delete metadata file", err)
			}
		}
	}

	// Find and delete sibling branches for messages >= msgNum
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return errors.WrapError("read directory", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Check if it's a sibling directory (format: XXXX.s.YYYY)
		if ok, branchMsgID, _ := IsSiblingDir(name); ok {
			// Parse the message number from the branch point
			branchNum, err := strconv.ParseUint(branchMsgID, 16, 64)
			if err != nil {
				continue
			}

			// Delete sibling branches that stem from messages after the deleted one
			if int(branchNum) > msgNum {
				branchPath := filepath.Join(dirPath, name)
				if err := os.RemoveAll(branchPath); err != nil {
					return errors.WrapError("delete sibling branch "+name, err)
				}
			}
		}
	}

	return nil
}

// fileExists is a simple helper to check if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// filePair represents a temp file and its final destination
type filePair struct {
	Tmp   string
	Final string
}

// CommitResult contains information about the commit operation
type CommitResult struct {
	OrphanedFiles []string // Files that were removed as orphaned (no matching .json)
}

// commitFiles atomically renames multiple files with rollback on failure.
//
// ATOMIC COMMIT INVARIANT:
// A message exists if and only if its .json file exists. This is the fundamental
// invariant that ensures consistency across the session storage system.
//
// Files are committed in a specific order to maintain atomicity:
//  1. .txt file (message content) - optional, may not exist for tool-only messages
//  2. .tools.json file (tool interaction data) - optional, only if tools were used
//  3. .json file (message metadata) - REQUIRED, this is the atomic commit point
//
// The .json file MUST be written last because:
//   - Its presence indicates a complete, committed message
//   - Readers check for .json to determine if a message exists
//   - Partial messages (orphaned .txt/.tools.json without .json) are cleaned up
//
// LOCKING REQUIREMENT:
// This function MUST be called while holding the session lock (via WithSessionLock).
// Callers MUST verify they hold the session lock before calling this function.
// The lock ensures:
//   - No concurrent writers can create conflicting message IDs
//   - Orphan cleanup is safe (won't remove files being written by another process)
//   - Message ID generation and commit are atomic
//
// ORPHAN HANDLING:
// If .txt or .tools.json files exist without a corresponding .json file, they are
// considered orphaned (from an interrupted write) and are removed. This cleanup is
// only safe under lock to prevent removing files from an in-progress write.
func commitFiles(ctx context.Context, files []filePair) (CommitResult, error) {
	result := CommitResult{
		OrphanedFiles: make([]string, 0),
	}
	renamed := make([]int, 0, len(files))

	// Helper to rollback all renamed files
	rollback := func() {
		for j := len(renamed) - 1; j >= 0; j-- {
			idx := renamed[j]
			_ = os.Rename(files[idx].Final, files[idx].Tmp)
		}
	}

	for i, f := range files {
		// Skip empty entries
		if f.Tmp == "" || f.Final == "" {
			if log := logger.From(ctx); log != nil {
				log.Debugf("Skipping empty entry at index %d", i)
			}
			continue
		}

		// Check if temp file exists and has content (for .txt files)
		if info, err := os.Stat(f.Tmp); err != nil {
			// File doesn't exist, skip
			if log := logger.From(ctx); log != nil {
				log.Debugf("Temp file doesn't exist, skipping: %s", f.Tmp)
			}
			continue
		} else if strings.HasSuffix(f.Final, ".txt") && info.Size() == 0 {
			// Empty text file, skip
			if log := logger.From(ctx); log != nil {
				log.Debugf("Empty text file, skipping: %s", f.Final)
			}
			continue
		}

		// Check if destination already exists
		if _, err := os.Stat(f.Final); err == nil {
			if log := logger.From(ctx); log != nil {
				log.Debugf("Destination exists: %s", f.Final)
			}
			// Orphan handling: See tryPlaceMessageFiles for the complete invariant.
			// .txt and .tools.json files without matching .json are orphaned and can be removed.
			if strings.HasSuffix(f.Final, ".txt") || strings.HasSuffix(f.Final, ".tools.json") {
				// Extract base path without extension
				basePath := f.Final
				if strings.HasSuffix(f.Final, ".txt") {
					basePath = strings.TrimSuffix(f.Final, ".txt")
				} else if strings.HasSuffix(f.Final, ".tools.json") {
					basePath = strings.TrimSuffix(f.Final, ".tools.json")
				}

				// Check if the .json file exists (the commit point)
				jsonPath := basePath + ".json"
				if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
					// .json doesn't exist, so this is an orphaned file - safe to remove
					if log := logger.From(ctx); log != nil {
						log.Debugf("Removing orphaned file (no matching .json metadata): %s", f.Final)
					}
					result.OrphanedFiles = append(result.OrphanedFiles, f.Final)
					if err := os.Remove(f.Final); err != nil {
						if log := logger.From(ctx); log != nil {
							log.Warnf("Failed to remove orphaned file %s: %v", f.Final, err)
						}
					}
				} else {
					// .json exists, so this is a real conflict
					// Rollback and return error
					rollback()
					return result, errors.WrapError("commit files", fmt.Errorf("destination already exists: %s", filepath.Base(f.Final)))
				}
			} else {
				// For .json files and other files, always error if destination exists
				// Rollback and return error
				if log := logger.From(ctx); log != nil {
					log.Debugf("Conflict detected for non-orphanable file: %s", f.Final)
				}
				rollback()
				return result, errors.WrapError("commit files", fmt.Errorf("destination already exists: %s", filepath.Base(f.Final)))
			}
		}

		// Attempt rename
		if err := os.Rename(f.Tmp, f.Final); err != nil {
			// Rollback previously renamed files in reverse order
			rollback()
			return result, errors.WrapError("rename "+filepath.Base(f.Tmp)+" to "+filepath.Base(f.Final), err)
		}

		// Track successful rename for potential rollback
		renamed = append(renamed, i)
	}
	return result, nil
}

// MessageStaging represents staged message files before atomic commit
type MessageStaging struct {
	TxtPath   string
	JsonPath  string
	ToolsPath string
}

// Close removes all staged files (cleanup on error or after successful commit)
func (s *MessageStaging) Close() {
	if s.TxtPath != "" {
		os.Remove(s.TxtPath)
	}
	if s.JsonPath != "" {
		os.Remove(s.JsonPath)
	}
	if s.ToolsPath != "" {
		os.Remove(s.ToolsPath)
	}
}

// stageMessageFiles creates temporary files for atomic message placement
func stageMessageFiles(sessionPath string, msg Message, toolInteraction *core.ToolInteraction) (*MessageStaging, error) {
	staging := &MessageStaging{}

	// Create temp files in session directory
	dir := sessionPath

	// Stage text content file
	if msg.Content != "" {
		tmpTxt, err := os.CreateTemp(dir, ".tmp-*.txt")
		if err != nil {
			return nil, errors.WrapError("create temp txt file", err)
		}
		staging.TxtPath = tmpTxt.Name()

		if _, err := tmpTxt.WriteString(msg.Content); err != nil {
			tmpTxt.Close()
			staging.Close()
			return nil, errors.WrapError("write content", err)
		}
		if err := tmpTxt.Close(); err != nil {
			staging.Close()
			return nil, errors.WrapError("close temp txt file", err)
		}
	}

	// Stage JSON metadata file
	tmpJson, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		staging.Close()
		return nil, errors.WrapError("create temp json file", err)
	}
	staging.JsonPath = tmpJson.Name()

	metaData, err := json.MarshalIndent(buildMessageMetadata(msg), "", "  ")
	if err != nil {
		tmpJson.Close()
		staging.Close()
		return nil, errors.WrapError("marshal metadata", err)
	}

	if _, err := tmpJson.Write(metaData); err != nil {
		tmpJson.Close()
		staging.Close()
		return nil, errors.WrapError("write metadata", err)
	}
	if err := tmpJson.Close(); err != nil {
		staging.Close()
		return nil, errors.WrapError("close temp json file", err)
	}

	// Stage tools file if present
	if toolInteraction != nil && (len(toolInteraction.Calls) > 0 || len(toolInteraction.Results) > 0) {
		tmpTools, err := os.CreateTemp(dir, ".tmp-*.tools.json")
		if err != nil {
			staging.Close()
			return nil, errors.WrapError("create temp tools file", err)
		}
		staging.ToolsPath = tmpTools.Name()

		toolData, err := json.MarshalIndent(toolInteraction, "", "  ")
		if err != nil {
			tmpTools.Close()
			staging.Close()
			return nil, errors.WrapError("marshal tool interaction", err)
		}

		if _, err := tmpTools.Write(toolData); err != nil {
			tmpTools.Close()
			staging.Close()
			return nil, errors.WrapError("write tool data", err)
		}
		if err := tmpTools.Close(); err != nil {
			staging.Close()
			return nil, errors.WrapError("close temp tools file", err)
		}
	}

	return staging, nil
}

// messageCommitter encapsulates the atomic commit logic for session messages
// It ensures the commit invariant: a message exists iff its .json exists
type messageCommitter struct {
	sessionPath string
}

// newMessageCommitter creates a new message committer for the given session path
func newMessageCommitter(sessionPath string) *messageCommitter {
	return &messageCommitter{sessionPath: sessionPath}
}

// Stage prepares temporary files for a message with optional tool interaction
func (mc *messageCommitter) Stage(msg Message, toolInteraction *core.ToolInteraction) (*MessageStaging, error) {
	return stageMessageFiles(mc.sessionPath, msg, toolInteraction)
}

// Commit atomically commits staged message files to the session.
//
// ATOMIC COMMIT INVARIANT:
// The commit follows a strict invariant to ensure atomicity:
// - A message exists if and only if its .json file exists
// - Files are committed in order: .txt → .tools.json → .json
// - The .json file is the atomic commit point
// - .txt and .tools.json files without a matching .json are considered orphaned
//
// This ordering ensures that:
// 1. No partial messages are visible during commit
// 2. The presence of .json indicates a complete, committed message
// 3. Orphaned files from interrupted operations can be detected and cleaned up
//
// LOCKING:
// This method MUST be called under session lock (via WithSessionLock) to ensure:
// - Atomic message ID generation
// - Safe conflict detection
// - Safe orphan cleanup without race conditions
//
// CONFLICT HANDLING:
// If a message with the generated ID already exists (detected by .json presence),
// the method returns needSibling=true and a suggested siblingPath. The caller
// should then create the sibling directory and retry with restaged files.
//
// Returns: msgID, needSibling, siblingPath, error
// If needSibling is true, the caller should create a sibling at siblingPath and retry
func (mc *messageCommitter) Commit(ctx context.Context, staging *MessageStaging) (string, bool, string, error) {
	var msgID string
	var needSibling bool
	var conflictMsgID string
	var siblingPath string

	// Use lock to ensure atomic operation
	err := WithSessionLock(mc.sessionPath, 5*time.Second, func() error {
		// Get next ID inside lock (reduces conflicts)
		var err error
		msgID, err = GetNextMessageID(mc.sessionPath)
		if err != nil {
			return errors.WrapError("get next message ID", err)
		}

		// Prepare final paths
		finalTxtPath := filepath.Join(mc.sessionPath, msgID+".txt")
		finalJsonPath := filepath.Join(mc.sessionPath, msgID+".json")
		finalToolsPath := filepath.Join(mc.sessionPath, msgID+".tools.json")

		// Check if a complete message already exists
		if fileExists(finalJsonPath) {
			// Conflict detected - message exists
			needSibling = true
			conflictMsgID = msgID
			return nil // Exit lock, will create sibling
		}

		// No conflict - use commitFiles helper for atomic operation
		// Prepare commit triplet in order: content, tools (optional), metadata (commit point)
		commitTriplet := []filePair{
			{Tmp: staging.TxtPath, Final: finalTxtPath},     // 1. Message content
			{Tmp: staging.ToolsPath, Final: finalToolsPath}, // 2. Tool data (if any)
			{Tmp: staging.JsonPath, Final: finalJsonPath},   // 3. Metadata (atomic commit point)
		}

		commitResult, err := commitFiles(ctx, commitTriplet)
		if err != nil {
			return errors.WrapError("commit message files", err)
		}

		// Log any orphaned files that were removed
		for _, orphanedFile := range commitResult.OrphanedFiles {
			logger.From(ctx).Debugf("Removed orphaned file during commit: %s (no matching .json metadata found)", orphanedFile)
		}

		return nil
	})
	if err != nil {
		return "", false, "", err
	}

	// Handle conflict by preparing sibling path
	if needSibling {
		siblingPath, err = CreateSibling(ctx, mc.sessionPath, conflictMsgID)
		if err != nil {
			return "", false, "", errors.WrapError("create sibling", err)
		}
		return conflictMsgID, true, siblingPath, nil
	}

	return msgID, false, "", nil
}

// CommitMessageWithRetries handles the complete message commit flow with retry logic
// This method encapsulates staging, committing, and handling sibling conflicts
func (mc *messageCommitter) CommitMessageWithRetries(ctx context.Context, msg Message, toolInteraction *core.ToolInteraction) (SaveResult, error) {
	const maxRetries = core.MaxRetries

	// Stage all files
	staged, err := mc.Stage(msg, toolInteraction)
	if err != nil {
		return SaveResult{}, err
	}
	defer staged.Close()

	// Retry loop for handling conflicts
	currentPath := mc.sessionPath
	var lastConflictMsgID string

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Check for context cancellation early
		if ctx.Err() != nil {
			return SaveResult{}, errors.WrapError("commit cancelled", ctx.Err())
		}

		// Optional exponential backoff
		if attempt > 2 {
			backoffDuration := time.Duration(1<<(attempt-2)) * 10 * time.Millisecond
			logger.From(ctx).Debugf("Retry attempt %d/%d after %v backoff", attempt+1, maxRetries, backoffDuration)
			time.Sleep(backoffDuration)
		}

		// Update committer path if we moved to a sibling
		if currentPath != mc.sessionPath {
			mc = newMessageCommitter(currentPath)
		}

		msgID, needSibling, siblingPath, err := mc.Commit(ctx, staged)
		if err != nil {
			// Retry on lock timeout
			if stdErrors.Is(err, ErrLockTimeout) && attempt < maxRetries-1 {
				logger.From(ctx).Debugf("Lock timeout on attempt %d/%d, retrying...", attempt+1, maxRetries)
				continue
			}
			return SaveResult{}, err
		}

		if !needSibling {
			// Success
			if attempt > 0 {
				logger.From(ctx).Debugf("Successfully saved message %s to %s after %d attempts",
					msgID, GetSessionID(currentPath), attempt+1)
			}
			return SaveResult{Path: currentPath, MessageID: msgID}, nil
		}

		// Track conflict for error reporting
		lastConflictMsgID = msgID

		// Log conflicts with more detail
		logger.From(ctx).Debugf("Attempt %d/%d: Message ID conflict: %s exists in %s, moving to sibling %s",
			attempt+1, maxRetries, msgID, GetSessionID(currentPath), GetSessionID(siblingPath))

		// Need to restage in sibling directory to avoid cross-directory rename
		// Close current staging and restage in the sibling directory
		staged.Close()
		mc = newMessageCommitter(siblingPath)
		staged, err = mc.Stage(msg, toolInteraction)
		if err != nil {
			return SaveResult{}, errors.WrapError("restage in sibling directory", err)
		}

		// Move to sibling for next attempt
		currentPath = siblingPath
	}

	// Max retries exceeded - provide helpful error message
	return SaveResult{}, errors.WrapError("save message", fmt.Errorf(
		"%w after %d attempts. Last conflict on message '%s' in %s.\n"+
			"Suggested actions:\n"+
			"  - Retry shortly (concurrent writer may be active)\n"+
			"  - Create a branch: lmc -branch %s/%s\n"+
			"  - Verify no other processes are writing to this session",
		ErrMaxRetriesExceeded, maxRetries, lastConflictMsgID, GetSessionID(currentPath),
		GetSessionID(currentPath), lastConflictMsgID))
}
