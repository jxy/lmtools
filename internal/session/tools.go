package session

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"os"
	"time"
)

// SaveAssistantResponseWithTools saves an assistant response that may include both text and tool calls
// This function uses proper locking via AppendMessageWithToolInteraction to ensure thread safety
func SaveAssistantResponseWithTools(ctx context.Context, sess *Session, text string, toolCalls []core.ToolCall, model string) (SaveResult, error) {
	return SaveAssistantResponseWithMetadata(ctx, sess, text, toolCalls, model, "")
}

// SaveAssistantResponseWithMetadata saves an assistant response that may include
// provider-specific metadata such as Google thought signatures.
func SaveAssistantResponseWithMetadata(ctx context.Context, sess *Session, text string, toolCalls []core.ToolCall, model string, thoughtSignature string) (SaveResult, error) {
	if sess == nil {
		return SaveResult{}, errors.WrapError("validate session", stdErrors.New("session is nil"))
	}

	msg := Message{
		Role:             core.RoleAssistant,
		Content:          text,
		ThoughtSignature: thoughtSignature,
		Timestamp:        time.Now(),
		Model:            model,
	}

	// Use AppendMessageWithToolInteraction which handles locking properly
	return AppendMessageWithToolInteraction(ctx, sess, msg, toolCalls, nil)
}

// SaveToolResults saves tool execution results to the session
// This function uses proper locking via AppendMessageWithToolInteraction to ensure thread safety
func SaveToolResults(ctx context.Context, sess *Session, results []core.ToolResult, additionalText string) (SaveResult, error) {
	if sess == nil {
		return SaveResult{}, errors.WrapError("validate session", stdErrors.New("session is nil"))
	}

	msg := Message{
		Role:      core.RoleUser,
		Content:   additionalText,
		Timestamp: time.Now(),
	}

	// Use AppendMessageWithToolInteraction which handles locking properly
	return AppendMessageWithToolInteraction(ctx, sess, msg, nil, results)
}

// LoadToolInteraction loads tool calls or results from a .tools.json file
func LoadToolInteraction(sessionPath, msgID string) (*core.ToolInteraction, error) {
	toolPath := buildMessageFilePaths(sessionPath, msgID).ToolsPath

	// Check if file exists
	if _, err := os.Stat(toolPath); os.IsNotExist(err) {
		return nil, nil // No tool interaction for this message
	}

	toolData, err := os.ReadFile(toolPath)
	if err != nil {
		return nil, errors.WrapError("read tool file", err)
	}

	var interaction core.ToolInteraction
	if err := json.Unmarshal(toolData, &interaction); err != nil {
		return nil, errors.WrapError("unmarshal tool interaction", err)
	}

	return &interaction, nil
}

// SaveToolInteraction saves tool interaction data to a .tools.json file
func SaveToolInteraction(sessionPath, msgID string, interaction *core.ToolInteraction) error {
	if !hasToolInteraction(interaction) {
		return nil // Nothing to save
	}

	toolPath := buildMessageFilePaths(sessionPath, msgID).ToolsPath
	toolData, err := marshalToolInteraction(interaction)
	if err != nil {
		return err
	}

	if err := writeFileAtomic(toolPath, toolData); err != nil {
		return errors.WrapError("write tool file", err)
	}

	return nil
}

// AppendMessageWithToolInteraction appends a message with tool calls and/or results using proper locking
// This function ensures thread safety for concurrent session operations
func AppendMessageWithToolInteraction(ctx context.Context, session *Session, msg Message, toolCalls []core.ToolCall, toolResults []core.ToolResult) (SaveResult, error) {
	// Create message committer
	mc := newMessageCommitter(session.Path)

	// Build tool interaction
	var toolInteraction *core.ToolInteraction
	if len(toolCalls) > 0 || len(toolResults) > 0 {
		toolInteraction = &core.ToolInteraction{
			Calls:   toolCalls,
			Results: toolResults,
		}
	}

	// Use the unified commit method
	return mc.CommitMessageWithRetries(ctx, msg, toolInteraction)
}
