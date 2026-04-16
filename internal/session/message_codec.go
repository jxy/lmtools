package session

import (
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"os"
	"time"
)

// MessageMetadata represents the JSON metadata for a message.
type MessageMetadata struct {
	Role             core.Role `json:"role"`
	Timestamp        time.Time `json:"timestamp"`
	Model            *string   `json:"model"`
	ThoughtSignature *string   `json:"thought_signature,omitempty"`
}

func buildMessageMetadata(msg Message) MessageMetadata {
	metadata := MessageMetadata{
		Role:      msg.Role,
		Timestamp: msg.Timestamp,
	}
	if msg.Model != "" {
		metadata.Model = &msg.Model
	}
	if msg.ThoughtSignature != "" {
		metadata.ThoughtSignature = &msg.ThoughtSignature
	}
	return metadata
}

func marshalMessageMetadata(msg Message) ([]byte, error) {
	metaData, err := json.MarshalIndent(buildMessageMetadata(msg), "", "  ")
	if err != nil {
		return nil, errors.WrapError("marshal metadata", err)
	}
	return metaData, nil
}

func hasToolInteraction(toolInteraction *core.ToolInteraction) bool {
	return toolInteraction != nil && (len(toolInteraction.Calls) > 0 || len(toolInteraction.Results) > 0)
}

func marshalToolInteraction(toolInteraction *core.ToolInteraction) ([]byte, error) {
	if !hasToolInteraction(toolInteraction) {
		return nil, nil
	}

	toolData, err := json.MarshalIndent(toolInteraction, "", "  ")
	if err != nil {
		return nil, errors.WrapError("marshal tool interaction", err)
	}
	return toolData, nil
}

type messageFileSet struct {
	Content  []byte
	Metadata []byte
	Tools    []byte
}

func buildMessageFileSet(msg Message, toolInteraction *core.ToolInteraction) (*messageFileSet, error) {
	metaData, err := marshalMessageMetadata(msg)
	if err != nil {
		return nil, err
	}

	toolData, err := marshalToolInteraction(toolInteraction)
	if err != nil {
		return nil, err
	}

	return &messageFileSet{
		Content:  []byte(msg.Content),
		Metadata: metaData,
		Tools:    toolData,
	}, nil
}

// writeMessage atomically writes a message to disk.
func writeMessage(sessionPath, msgID string, msg Message) error {
	fileSet, err := buildMessageFileSet(msg, nil)
	if err != nil {
		return err
	}

	paths := buildMessageFilePaths(sessionPath, msgID)
	if err := writeFileAtomic(paths.TxtPath, fileSet.Content); err != nil {
		return errors.WrapError("write message content", err)
	}

	if err := writeFileAtomic(paths.JSONPath, fileSet.Metadata); err != nil {
		// Try to clean up content file.
		_ = os.Remove(paths.TxtPath)
		return errors.WrapError("write metadata", err)
	}

	return nil
}

// readMessage reads a message from disk.
// Invariant: A message exists if and only if its .json exists.
// .txt may be missing (e.g., tool-only messages), .tools.json is optional.
func readMessage(sessionPath, msgID string) (*Message, error) {
	paths := buildMessageFilePaths(sessionPath, msgID)

	// Read metadata first (always required).
	metaBytes, err := os.ReadFile(paths.JSONPath)
	if err != nil {
		return nil, errors.WrapError("read metadata", err)
	}

	var metadata MessageMetadata
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		return nil, errors.WrapError("unmarshal metadata", err)
	}

	// Read content if it exists (some messages like tool results might not have text).
	var content string
	if _, err := os.Stat(paths.TxtPath); err == nil {
		contentBytes, err := os.ReadFile(paths.TxtPath)
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
	if metadata.ThoughtSignature != nil {
		msg.ThoughtSignature = *metadata.ThoughtSignature
	}

	return msg, nil
}
