package session

// Batched Message Appends
//
// Appending messages one at a time costs a full directory listing each time,
// because GetNextMessageID scans the session directory to find the highest ID
// in use. Committing a whole transcript that way is quadratic: the proxy writes
// every replayed message on each turn, so a few thousand messages turn into a
// few thousand listings of a directory holding a few thousand files.
//
// AppendMessagesWithBlocks stages the messages first, then takes the session
// lock once, scans once, and advances the ID in memory. Anything the fast path
// cannot claim falls back to the per-message path, which knows how to create
// siblings and retry. A lock it could not take hands over as-is; an ID a
// concurrent writer already owns is returned separately and forked first,
// because the conflict is visible only here and a fresh scan would not see it
// again.

import (
	"context"
	stdErrors "errors"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"strconv"
)

// MessageEntry is one message to append, with its optional tool interaction and
// typed blocks.
type MessageEntry struct {
	Message     Message
	ToolCalls   []core.ToolCall
	ToolResults []core.ToolResult
	Blocks      []core.Block
}

func (e MessageEntry) toolInteraction() *core.ToolInteraction {
	if len(e.ToolCalls) == 0 && len(e.ToolResults) == 0 {
		return nil
	}
	return &core.ToolInteraction{Calls: e.ToolCalls, Results: e.ToolResults}
}

// AppendMessagesWithBlocks appends entries in order and returns the result of
// the last one. Like AppendMessageWithBlocks, it advances session.Path when a
// commit moves to a sibling directory.
//
// Every entry is staged before the first commit, so serialization or staging
// failure leaves the session unchanged. Commits are not atomic as a group:
// cancellation or an I/O failure after committing begins may leave a prefix,
// and the returned SaveResult identifies the last committed message.
func AppendMessagesWithBlocks(ctx context.Context, session *Session, entries []MessageEntry) (SaveResult, error) {
	if session == nil {
		return SaveResult{}, errors.WrapError("append messages", stdErrors.New("session is required"))
	}
	if len(entries) == 0 {
		return SaveResult{Path: session.Path}, nil
	}
	if ctx.Err() != nil {
		return SaveResult{Path: session.Path}, errors.WrapError("commit cancelled", ctx.Err())
	}

	// Match the single-message path: serialization and temporary-file writes are
	// preparation, not part of the session critical section. A large attachment
	// can take seconds to stage; holding the lock for that work makes unrelated
	// writers time out even though only the final renames need exclusion.
	staged := make([]*MessageStaging, 0, len(entries))
	cleanupStaged := func() {
		for _, entry := range staged {
			entry.Close()
		}
		staged = nil
	}
	defer cleanupStaged()
	for _, entry := range entries {
		if ctx.Err() != nil {
			return SaveResult{Path: session.Path}, errors.WrapError("commit cancelled", ctx.Err())
		}
		files, err := stageMessageFilesWithBlocks(session.Path, entry.Message, entry.toolInteraction(), entry.Blocks)
		if err != nil {
			return SaveResult{Path: session.Path}, err
		}
		staged = append(staged, files)
	}

	result, committed, conflictID, err := commitStagedMessageBatch(ctx, session.Path, staged)
	if err != nil || conflictID != "" {
		if err != nil && !stdErrors.Is(err, ErrLockTimeout) {
			return result, err
		}
		// The fallback may move to a sibling and stages each remaining message in
		// that directory. Release the unused fast-path files before doing so.
		cleanupStaged()
		if conflictID != "" {
			logger.From(ctx).Debugf("Batch append stopped after %d/%d messages because ID %s already exists; finishing one at a time", committed, len(entries), conflictID)
			// Handing the conflict to the per-message path as-is would lose it: that
			// path scans afresh, finds the first ID past the other writer's message,
			// and appends behind it, so a stranger's turn ends up in the middle of
			// this response's lineage. The conflict has to be answered where it was
			// seen, the way messageCommitter.Commit answers it — by forking at the
			// contested ID, which leaves that message out of the branch.
			siblingPath, siblingErr := CreateSibling(ctx, session.Path, conflictID)
			if siblingErr != nil {
				return result, errors.WrapError("create sibling", siblingErr)
			}
			logger.From(ctx).Debugf("Message ID conflict: %s exists in %s, moving to sibling %s", conflictID, GetSessionID(session.Path), GetSessionID(siblingPath))
			session.Path = siblingPath
		} else {
			logger.From(ctx).Debugf("Batch append stopped after %d/%d messages (%v); finishing one at a time", committed, len(entries), err)
		}
	}

	for _, entry := range entries[committed:] {
		single, err := AppendMessageWithBlocks(ctx, session, entry.Message, entry.ToolCalls, entry.ToolResults, entry.Blocks)
		if err != nil {
			return result, err
		}
		session.Path = single.Path
		result = single
	}
	return result, nil
}

// commitStagedMessageBatch takes the session lock and commits as many staged
// entries as it can after one directory scan. It reports how many it committed
// and, separately, an ID claimed by another writer so the caller can fork at
// that exact point.
//
// A cancelled context stops the batch where it stands, the way the per-message
// path stops between retries. The caller propagates that error instead of
// writing the rest of the transcript one message at a time under a context that
// is already dead.
func commitStagedMessageBatch(ctx context.Context, sessionPath string, staged []*MessageStaging) (SaveResult, int, string, error) {
	result := SaveResult{Path: sessionPath}
	if ctx.Err() != nil {
		return result, 0, "", errors.WrapError("commit cancelled", ctx.Err())
	}
	committed := 0
	conflictID := ""
	err := WithSessionLock(sessionPath, messageCommitLockTimeout, func() error {
		nextID, err := GetNextMessageID(sessionPath)
		if err != nil {
			return errors.WrapError("get next message ID", err)
		}
		id, err := strconv.ParseUint(nextID, 16, 64)
		if err != nil {
			return errors.WrapError("parse next message ID", err)
		}
		if afterGetNextMessageIDForTest != nil {
			afterGetNextMessageIDForTest(sessionPath, nextID)
		}

		for _, entry := range staged {
			if ctx.Err() != nil {
				return errors.WrapError("commit cancelled", ctx.Err())
			}
			msgID := formatVariableWidthHexID(int(id))
			if fileExists(buildMessageFilePaths(sessionPath, msgID).JSONPath) {
				conflictID = msgID
				return nil
			}
			if err := commitStagedMessageLocked(ctx, sessionPath, msgID, entry); err != nil {
				return err
			}
			result.Path = sessionPath
			result.MessageID = msgID
			committed++
			id++
		}
		return nil
	})
	return result, committed, conflictID, err
}
