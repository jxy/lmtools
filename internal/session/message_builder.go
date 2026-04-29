package session

import (
	"context"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
)

// BuildMessagesWithToolInteractions reconstructs messages with their tool interactions
// for use in API requests. It loads the conversation lineage and attaches any
// tool calls or results stored in .tools.json files.
//
// This function is a convenience wrapper around CreateCachedMessageBuilder for one-shot use.
// For multiple calls (e.g., in tool execution loops), use CreateCachedMessageBuilder directly.
func BuildMessagesWithToolInteractions(ctx context.Context, sessionPath string) ([]core.TypedMessage, error) {
	snapshot, err := newConversationSnapshot(sessionPath)
	if err != nil {
		return nil, err
	}
	return snapshot.buildTypedMessages(ctx, sessionPath)
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
	// Reconstruct messages with tool interactions
	var result []core.TypedMessage
	toolNamesByID := make(map[string]string)

	for _, msg := range messages {
		// Load any tool interactions for this message using the index
		msgDir := resolveIndexedMessageDir(ctx, messageIndex, msg.ID, sessionPath)

		toolInteraction, err := LoadToolInteraction(msgDir, msg.ID)
		if err != nil {
			return nil, errors.WrapError("load tool interaction for message "+msg.ID, err)
		}

		result = append(result, buildTypedMessage(msg, toolInteraction, toolNamesByID))
	}

	return result, nil
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
	typedMsg := core.TypedMessage{
		Role:   string(msg.Role),
		Blocks: make([]core.Block, 0),
	}

	if msg.Content != "" || msg.ThoughtSignature != "" {
		typedMsg.Blocks = append(typedMsg.Blocks, core.TextBlock{
			Text:             msg.Content,
			ThoughtSignature: msg.ThoughtSignature,
		})
	}

	if toolInteraction == nil {
		return typedMsg
	}

	for _, call := range toolInteraction.Calls {
		typedMsg.Blocks = append(typedMsg.Blocks, core.ToolUseBlock{
			ID:               call.ID,
			Name:             call.Name,
			Input:            call.Args,
			ThoughtSignature: call.ThoughtSignature,
		})
		if call.ID != "" {
			toolNamesByID[call.ID] = call.Name
		}
	}

	for _, res := range toolInteraction.Results {
		typedMsg.Blocks = append(typedMsg.Blocks, core.ToolResultBlockFromResult(res, toolNamesByID[res.ID]))
	}

	return typedMsg
}

// CheckForPendingToolCalls checks if the last message in a session has tool calls
// without corresponding results, indicating pending tool execution
func CheckForPendingToolCalls(ctx context.Context, sessionPath string) ([]core.ToolCall, error) {
	snapshot, err := newConversationSnapshot(sessionPath)
	if err != nil {
		return nil, err
	}

	calls, err := snapshot.pendingToolCalls(ctx, sessionPath)
	if err != nil {
		return nil, err
	}
	if len(calls) > 0 {
		logger.From(ctx).Debugf("CheckForPendingToolCalls: found %d pending tool call(s) in %s", len(calls), GetSessionID(sessionPath))
	}
	return calls, nil
}

// CreateCachedMessageBuilder creates a message builder function that caches the message index
// This is useful for tool execution loops to avoid rebuilding the index on each round
func CreateCachedMessageBuilder(ctx context.Context, sessionPath string) (func(string) ([]core.TypedMessage, error), error) {
	snapshot, err := newConversationSnapshot(sessionPath)
	if err != nil {
		return nil, err
	}

	return func(path string) ([]core.TypedMessage, error) {
		return snapshot.buildTypedMessages(ctx, path)
	}, nil
}
