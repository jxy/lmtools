package session

import (
	"context"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// filePair represents a temp file and its final destination.
type filePair struct {
	Tmp   string
	Final string
}

// CommitResult contains information about the commit operation.
type CommitResult struct {
	OrphanedFiles []string // Files that were removed as orphaned (no matching .json).
}

// commitFiles atomically renames multiple files with rollback on failure.
//
// ATOMIC COMMIT INVARIANT:
// A message exists if and only if its .json file exists. This is the fundamental
// invariant that ensures consistency across the session storage system.
//
// Files are committed in a specific order to maintain atomicity:
//  1. .txt file (message content)
//  2. .tools.json file (tool interaction data)
//  3. .blocks.json file (lossless typed block data)
//  4. .json file (message metadata)
//
// LOCKING REQUIREMENT:
// This function MUST be called while holding the session lock (via WithSessionLock).
func commitFiles(ctx context.Context, files []filePair) (CommitResult, error) {
	result := CommitResult{
		OrphanedFiles: make([]string, 0),
	}
	renamed := make([]int, 0, len(files))

	rollback := func() {
		for j := len(renamed) - 1; j >= 0; j-- {
			idx := renamed[j]
			_ = os.Rename(files[idx].Final, files[idx].Tmp)
		}
	}

	for i, f := range files {
		if f.Tmp == "" || f.Final == "" {
			if log := logger.From(ctx); log != nil {
				log.Debugf("Skipping empty entry at index %d", i)
			}
			continue
		}

		if info, err := os.Stat(f.Tmp); err != nil {
			if log := logger.From(ctx); log != nil {
				log.Debugf("Temp file doesn't exist, skipping: %s", f.Tmp)
			}
			continue
		} else if strings.HasSuffix(f.Final, ".txt") && info.Size() == 0 {
			if log := logger.From(ctx); log != nil {
				log.Debugf("Empty text file, skipping: %s", f.Final)
			}
			continue
		}

		if _, err := os.Stat(f.Final); err == nil {
			if log := logger.From(ctx); log != nil {
				log.Debugf("Destination exists: %s", f.Final)
			}
			if strings.HasSuffix(f.Final, ".txt") || strings.HasSuffix(f.Final, ".tools.json") || strings.HasSuffix(f.Final, ".blocks.json") {
				basePath := f.Final
				if strings.HasSuffix(f.Final, ".txt") {
					basePath = strings.TrimSuffix(f.Final, ".txt")
				} else if strings.HasSuffix(f.Final, ".tools.json") {
					basePath = strings.TrimSuffix(f.Final, ".tools.json")
				} else if strings.HasSuffix(f.Final, ".blocks.json") {
					basePath = strings.TrimSuffix(f.Final, ".blocks.json")
				}

				jsonPath := basePath + ".json"
				if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
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
					rollback()
					return result, errors.WrapError("commit files", fmt.Errorf("destination already exists: %s", filepath.Base(f.Final)))
				}
			} else {
				if log := logger.From(ctx); log != nil {
					log.Debugf("Conflict detected for non-orphanable file: %s", f.Final)
				}
				rollback()
				return result, errors.WrapError("commit files", fmt.Errorf("destination already exists: %s", filepath.Base(f.Final)))
			}
		}

		if err := os.Rename(f.Tmp, f.Final); err != nil {
			rollback()
			return result, errors.WrapError("rename "+filepath.Base(f.Tmp)+" to "+filepath.Base(f.Final), err)
		}

		renamed = append(renamed, i)
	}

	return result, nil
}

// MessageStaging represents staged message files before atomic commit.
type MessageStaging struct {
	TxtPath    string
	JsonPath   string
	ToolsPath  string
	BlocksPath string
}

// Close removes all staged files (cleanup on error or after successful commit).
func (s *MessageStaging) Close() {
	if s.TxtPath != "" {
		_ = os.Remove(s.TxtPath)
	}
	if s.JsonPath != "" {
		_ = os.Remove(s.JsonPath)
	}
	if s.ToolsPath != "" {
		_ = os.Remove(s.ToolsPath)
	}
	if s.BlocksPath != "" {
		_ = os.Remove(s.BlocksPath)
	}
}

// stageMessageFiles creates temporary files for atomic message placement.
func stageMessageFiles(sessionPath string, msg Message, toolInteraction *core.ToolInteraction) (*MessageStaging, error) {
	return stageMessageFilesWithBlocks(sessionPath, msg, toolInteraction, nil)
}

func stageMessageFilesWithBlocks(sessionPath string, msg Message, toolInteraction *core.ToolInteraction, blocks []core.Block) (*MessageStaging, error) {
	staging := &MessageStaging{}
	fileSet, err := buildMessageFileSetWithBlocks(msg, toolInteraction, blocks)
	if err != nil {
		return nil, err
	}

	dir := sessionPath

	if len(fileSet.Content) > 0 {
		staging.TxtPath, err = writeStagedTempFile(dir, ".tmp-*.txt", fileSet.Content)
		if err != nil {
			staging.Close()
			return nil, errors.WrapError("stage content", err)
		}
	}

	staging.JsonPath, err = writeStagedTempFile(dir, ".tmp-*.json", fileSet.Metadata)
	if err != nil {
		staging.Close()
		return nil, errors.WrapError("stage metadata", err)
	}

	if len(fileSet.Tools) > 0 {
		staging.ToolsPath, err = writeStagedTempFile(dir, ".tmp-*.tools.json", fileSet.Tools)
		if err != nil {
			staging.Close()
			return nil, errors.WrapError("stage tool interaction", err)
		}
	}

	if len(fileSet.Blocks) > 0 {
		staging.BlocksPath, err = writeStagedTempFile(dir, ".tmp-*.blocks.json", fileSet.Blocks)
		if err != nil {
			staging.Close()
			return nil, errors.WrapError("stage typed blocks", err)
		}
	}

	return staging, nil
}

func buildCommitTriplet(sessionPath, msgID string, staging *MessageStaging) []filePair {
	paths := buildMessageFilePaths(sessionPath, msgID)
	return []filePair{
		{Tmp: staging.TxtPath, Final: paths.TxtPath},
		{Tmp: staging.ToolsPath, Final: paths.ToolsPath},
		{Tmp: staging.BlocksPath, Final: paths.BlocksPath},
		{Tmp: staging.JsonPath, Final: paths.JSONPath},
	}
}

// messageCommitter encapsulates the atomic commit logic for session messages.
type messageCommitter struct {
	sessionPath string
}

var afterGetNextMessageIDForTest func(sessionPath, msgID string)

// newMessageCommitter creates a new message committer for the given session path.
func newMessageCommitter(sessionPath string) *messageCommitter {
	return &messageCommitter{sessionPath: sessionPath}
}

// Stage prepares temporary files for a message with optional tool interaction.
func (mc *messageCommitter) Stage(msg Message, toolInteraction *core.ToolInteraction) (*MessageStaging, error) {
	return stageMessageFiles(mc.sessionPath, msg, toolInteraction)
}

func (mc *messageCommitter) StageWithBlocks(msg Message, toolInteraction *core.ToolInteraction, blocks []core.Block) (*MessageStaging, error) {
	return stageMessageFilesWithBlocks(mc.sessionPath, msg, toolInteraction, blocks)
}

// Commit atomically commits staged message files to the session.
// Returns: msgID, needSibling, siblingPath, error.
func (mc *messageCommitter) Commit(ctx context.Context, staging *MessageStaging) (string, bool, string, error) {
	var msgID string
	var needSibling bool
	var conflictMsgID string
	var siblingPath string

	err := WithSessionLock(mc.sessionPath, 5*time.Second, func() error {
		var err error
		msgID, err = GetNextMessageID(mc.sessionPath)
		if err != nil {
			return errors.WrapError("get next message ID", err)
		}
		if afterGetNextMessageIDForTest != nil {
			afterGetNextMessageIDForTest(mc.sessionPath, msgID)
		}

		paths := buildMessageFilePaths(mc.sessionPath, msgID)
		if fileExists(paths.JSONPath) {
			needSibling = true
			conflictMsgID = msgID
			return nil
		}

		commitResult, err := commitFiles(ctx, buildCommitTriplet(mc.sessionPath, msgID, staging))
		if err != nil {
			return errors.WrapError("commit message files", err)
		}

		for _, orphanedFile := range commitResult.OrphanedFiles {
			logger.From(ctx).Debugf("Removed orphaned file during commit: %s (no matching .json metadata found)", orphanedFile)
		}

		return nil
	})
	if err != nil {
		return "", false, "", err
	}

	if needSibling {
		siblingPath, err = CreateSibling(ctx, mc.sessionPath, conflictMsgID)
		if err != nil {
			return "", false, "", errors.WrapError("create sibling", err)
		}
		return conflictMsgID, true, siblingPath, nil
	}

	return msgID, false, "", nil
}

// CommitMessageWithRetries handles the complete message commit flow with retry logic.
func (mc *messageCommitter) CommitMessageWithRetries(ctx context.Context, msg Message, toolInteraction *core.ToolInteraction) (SaveResult, error) {
	return mc.CommitMessageWithBlocksWithRetries(ctx, msg, toolInteraction, nil)
}

func (mc *messageCommitter) CommitMessageWithBlocksWithRetries(ctx context.Context, msg Message, toolInteraction *core.ToolInteraction, blocks []core.Block) (SaveResult, error) {
	const maxRetries = core.MaxRetries

	staged, err := mc.StageWithBlocks(msg, toolInteraction, blocks)
	if err != nil {
		return SaveResult{}, err
	}
	defer staged.Close()

	currentPath := mc.sessionPath
	var lastConflictMsgID string

	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return SaveResult{}, errors.WrapError("commit cancelled", ctx.Err())
		}

		if attempt > 2 {
			backoffDuration := time.Duration(1<<(attempt-2)) * 10 * time.Millisecond
			logger.From(ctx).Debugf("Retry attempt %d/%d after %v backoff", attempt+1, maxRetries, backoffDuration)
			time.Sleep(backoffDuration)
		}

		if currentPath != mc.sessionPath {
			mc = newMessageCommitter(currentPath)
		}

		msgID, needSibling, siblingPath, err := mc.Commit(ctx, staged)
		if err != nil {
			if stdErrors.Is(err, ErrLockTimeout) && attempt < maxRetries-1 {
				logger.From(ctx).Debugf("Lock timeout on attempt %d/%d, retrying...", attempt+1, maxRetries)
				continue
			}
			return SaveResult{}, err
		}

		if !needSibling {
			if attempt > 0 {
				logger.From(ctx).Debugf(
					"Successfully saved message %s to %s after %d attempts",
					msgID,
					GetSessionID(currentPath),
					attempt+1,
				)
			}
			return SaveResult{Path: currentPath, MessageID: msgID}, nil
		}

		lastConflictMsgID = msgID

		logger.From(ctx).Debugf(
			"Attempt %d/%d: Message ID conflict: %s exists in %s, moving to sibling %s",
			attempt+1,
			maxRetries,
			msgID,
			GetSessionID(currentPath),
			GetSessionID(siblingPath),
		)

		staged.Close()
		mc = newMessageCommitter(siblingPath)
		staged, err = mc.StageWithBlocks(msg, toolInteraction, blocks)
		if err != nil {
			return SaveResult{}, errors.WrapError("restage in sibling directory", err)
		}

		currentPath = siblingPath
	}

	return SaveResult{}, errors.WrapError("save message", fmt.Errorf(
		"%w after %d attempts. Last conflict on message '%s' in %s.\n"+
			"Suggested actions:\n"+
			"  - Retry shortly (concurrent writer may be active)\n"+
			"  - Create a branch: lmc -branch %s/%s\n"+
			"  - Verify no other processes are writing to this session",
		ErrMaxRetriesExceeded,
		maxRetries,
		lastConflictMsgID,
		GetSessionID(currentPath),
		GetSessionID(currentPath),
		lastConflictMsgID,
	))
}
