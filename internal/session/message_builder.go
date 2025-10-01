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
	cached, err := CreateCachedMessageBuilder(ctx, sessionPath)
	if err != nil {
		return nil, err
	}
	return cached(sessionPath)
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

	for _, msg := range messages {
		// Load any tool interactions for this message using the index
		msgDir := messageIndex[msg.ID]
		if msgDir == "" {
			// Message not found in index - this shouldn't happen but handle gracefully
			logger.From(ctx).Debugf("Message %s not found in index, using session path", msg.ID)
			msgDir = sessionPath
		}

		toolInteraction, err := LoadToolInteraction(msgDir, msg.ID)
		if err != nil {
			return nil, errors.WrapError("load tool interaction for message "+msg.ID, err)
		}

		// Build the message in TypedMessage format
		if toolInteraction == nil {
			// Simple message without tools
			typedMsg := core.TypedMessage{
				Role: string(msg.Role),
			}
			// Only create a text block if there's content
			if msg.Content != "" {
				typedMsg.Blocks = []core.Block{
					core.TextBlock{
						Text: msg.Content,
					},
				}
			}
			result = append(result, typedMsg)
		} else if len(toolInteraction.Calls) > 0 {
			// Message with tool calls - preserve original role
			typedMsg := core.TypedMessage{
				Role:   string(msg.Role),
				Blocks: make([]core.Block, 0),
			}

			// Add text content if present
			if msg.Content != "" {
				typedMsg.Blocks = append(typedMsg.Blocks, core.TextBlock{
					Text: msg.Content,
				})
			}

			// Add tool calls
			for _, call := range toolInteraction.Calls {
				typedMsg.Blocks = append(typedMsg.Blocks, core.ToolUseBlock{
					ID:    call.ID,
					Name:  call.Name,
					Input: call.Args,
				})
			}
			result = append(result, typedMsg)
		} else if len(toolInteraction.Results) > 0 {
			// Message with tool results - preserve original role
			typedMsg := core.TypedMessage{
				Role:   string(msg.Role),
				Blocks: make([]core.Block, 0),
			}

			// Add any additional text (e.g., truncation notes)
			if msg.Content != "" {
				typedMsg.Blocks = append(typedMsg.Blocks, core.TextBlock{
					Text: msg.Content,
				})
			}

			// Add tool results
			for _, res := range toolInteraction.Results {
				block := core.ToolResultBlock{
					ToolUseID: res.ID,
					Content:   res.Output,
				}

				// Add error flag if present
				if res.Error != "" {
					block.IsError = true
					block.Content = res.Error
				}

				typedMsg.Blocks = append(typedMsg.Blocks, block)
			}
			result = append(result, typedMsg)
		} else {
			// Message with empty tool interaction (shouldn't happen but handle gracefully)
			typedMsg := core.TypedMessage{
				Role: string(msg.Role),
			}
			if msg.Content != "" {
				typedMsg.Blocks = []core.Block{
					core.TextBlock{
						Text: msg.Content,
					},
				}
			}
			result = append(result, typedMsg)
		}
	}

	return result, nil
}

// CheckForPendingToolCalls checks if the last message in a session has tool calls
// without corresponding results, indicating pending tool execution
func CheckForPendingToolCalls(ctx context.Context, sessionPath string) ([]core.ToolCall, error) {
	messages, err := GetLineage(sessionPath)
	if err != nil {
		return nil, errors.WrapError("get lineage", err)
	}

	if len(messages) == 0 {
		return nil, nil
	}

	// Check the last message
	lastMsg := messages[len(messages)-1]
	logger.From(ctx).Debugf("CheckForPendingToolCalls: last message in %s is %s role=%s",
		GetSessionID(sessionPath), lastMsg.ID, lastMsg.Role)

	// Only assistant messages can have tool calls
	if lastMsg.Role != core.RoleAssistant {
		return nil, nil
	}

	// Build message index to find the directory containing the last message
	messageIndex, err := indexMessageDirectories(sessionPath)
	if err != nil {
		return nil, errors.WrapError("index message directories", err)
	}

	msgDir := messageIndex[lastMsg.ID]
	if msgDir == "" {
		// Message not found in index - this shouldn't happen but handle gracefully
		logger.From(ctx).Debugf("Message %s not found in index, using session path", lastMsg.ID)
		msgDir = sessionPath
	}

	toolInteraction, err := LoadToolInteraction(msgDir, lastMsg.ID)
	if err != nil {
		return nil, errors.WrapError("load tool interaction", err)
	}

	// If there are tool calls, they're pending (no subsequent user message with results)
	if toolInteraction != nil && len(toolInteraction.Calls) > 0 {
		return toolInteraction.Calls, nil
	}

	return nil, nil
}

// CreateCachedMessageBuilder creates a message builder function that caches the message index
// This is useful for tool execution loops to avoid rebuilding the index on each round
func CreateCachedMessageBuilder(ctx context.Context, sessionPath string) (func(string) ([]core.TypedMessage, error), error) {
	// Build the index once
	messageIndex, err := indexMessageDirectories(sessionPath)
	if err != nil {
		return nil, errors.WrapError("index message directories", err)
	}

	// Return a closure that uses the cached index
	return func(path string) ([]core.TypedMessage, error) {
		// Get the basic message lineage
		messages, err := GetLineage(path)
		if err != nil {
			return nil, errors.WrapError("get lineage", err)
		}

		// If the path changed (e.g., after forking), update the index for new messages only
		if path != sessionPath {
			// Index any new messages in the new path
			newIndex, err := indexMessageDirectories(path)
			if err != nil {
				return nil, errors.WrapError("index new message directories", err)
			}
			// Merge new entries into the existing index
			for k, v := range newIndex {
				messageIndex[k] = v
			}
			sessionPath = path
		}

		return BuildMessagesWithIndex(ctx, messages, messageIndex, path)
	}, nil
}
