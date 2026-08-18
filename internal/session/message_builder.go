package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/logger"
)

// BuildMessagesWithToolInteractions reconstructs messages with their tool interactions
// for use in API requests. It loads the conversation lineage and attaches any
// tool calls or results stored in .tools.json files.
func BuildMessagesWithToolInteractions(ctx context.Context, sessionPath string) ([]core.TypedMessage, error) {
	return BuildMessagesWithToolInteractionsWithManager(ctx, DefaultManager(), sessionPath)
}

func BuildMessagesWithToolInteractionsWithManager(ctx context.Context, manager *Manager, sessionPath string) ([]core.TypedMessage, error) {
	refs, err := lineageMessageRefsWithManager(manager, sessionPath)
	if err != nil {
		return nil, err
	}
	return buildTypedMessagesFromLineageRefs(ctx, refs)
}

// BuildMessagesWithIndex reconstructs messages using a pre-built index
// This allows reusing the index across multiple calls in the tool execution loop
//
// Performance improvement: Using a pre-built index reduces complexity from O(n²) to O(n)
// where n is the number of messages. The index maps message IDs to their containing
// directories, avoiding repeated directory traversals for each message lookup.
// This is especially important for sessions with sibling branches where messages
// may be scattered across multiple directories.
func BuildMessagesWithIndex(ctx context.Context, messages []Message, messageIndex map[string]string, sessionPath string) ([]core.TypedMessage, error) {
	refs := make([]lineageMessageRef, 0, len(messages))
	for _, msg := range messages {
		refs = append(refs, lineageMessageRef{
			path:    resolveIndexedMessageDir(ctx, messageIndex, msg.ID, sessionPath),
			message: msg,
		})
	}
	return buildTypedMessagesFromLineageRefs(ctx, refs)
}

func resolveIndexedMessageDir(ctx context.Context, messageIndex map[string]string, messageID, fallbackPath string) string {
	msgDir := messageIndex[messageID]
	if msgDir == "" {
		logger.From(ctx).Debugf("Message %s not found in index, using session path", messageID)
		return fallbackPath
	}
	return msgDir
}

func buildTypedMessage(msg Message, toolInteraction *core.ToolInteraction, toolNamesByID map[string]string) core.TypedMessage {
	return core.TypedMessage{
		Role:   string(msg.Role),
		Blocks: applyToolNameIndex(blocksFromMessageProjection(msg, toolInteraction), toolNamesByID),
	}
}

func applyToolNameIndex(blocks []core.Block, toolNamesByID map[string]string) []core.Block {
	for i, block := range blocks {
		switch value := block.(type) {
		case core.ToolUseBlock:
			if value.ID != "" {
				toolNamesByID[value.ID] = value.Name
			}
		case core.ToolResultBlock:
			if value.Name == "" && value.ToolUseID != "" {
				value.Name = toolNamesByID[value.ToolUseID]
				blocks[i] = value
			}
		}
	}
	return blocks
}

// CheckForPendingToolCalls checks if the last message in a session has tool calls
// without corresponding results, indicating pending tool execution
func CheckForPendingToolCalls(ctx context.Context, sessionPath string) ([]core.ToolCall, error) {
	refs, err := lineageMessageRefsWithManager(DefaultManager(), sessionPath)
	if err != nil {
		return nil, err
	}

	calls, err := pendingToolCallsFromLineageRefs(ctx, refs)
	if err != nil {
		return nil, err
	}
	if len(calls) > 0 {
		logger.From(ctx).Debugf("CheckForPendingToolCalls: found %d pending tool call(s) in %s", len(calls), GetSessionID(sessionPath))
	}
	return calls, nil
}

// CreateCachedMessageBuilder creates a message builder that caches the stable
// lineage and refreshes messages appended to the active session directory.
func CreateCachedMessageBuilder(ctx context.Context, sessionPath string) (func(string) ([]core.TypedMessage, error), error) {
	snapshot, err := newConversationSnapshotWithManager(DefaultManager(), sessionPath)
	if err != nil {
		return nil, err
	}

	return func(path string) ([]core.TypedMessage, error) {
		return snapshot.buildTypedMessages(ctx, path)
	}, nil
}
