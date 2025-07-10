package argo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MessageMetadata represents the JSON metadata for a message
type MessageMetadata struct {
	Role      string    `json:"role"`
	Timestamp time.Time `json:"timestamp"`
	Model     *string   `json:"model"`
}

// writeMessage atomically writes a message to disk
func writeMessage(sessionPath, msgID string, msg Message) error {
	// Write content file
	contentPath := filepath.Join(sessionPath, msgID+".txt")
	if err := writeFileAtomic(contentPath, []byte(msg.Content)); err != nil {
		return fmt.Errorf("failed to write content: %w", err)
	}

	// Prepare metadata
	var model *string
	if msg.Model != "" {
		model = &msg.Model
	}

	metadata := MessageMetadata{
		Role:      msg.Role,
		Timestamp: msg.Timestamp,
		Model:     model,
	}

	// Write metadata file
	metaPath := filepath.Join(sessionPath, msgID+".json")
	metaData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := writeFileAtomic(metaPath, metaData); err != nil {
		// Try to clean up content file
		os.Remove(contentPath)
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// ReadMessage reads a message from disk
func ReadMessage(sessionPath, msgID string) (*Message, error) {
	// Read content
	contentPath := filepath.Join(sessionPath, msgID+".txt")
	contentBytes, err := os.ReadFile(contentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read content: %w", err)
	}

	// Read metadata
	metaPath := filepath.Join(sessionPath, msgID+".json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var metadata MessageMetadata
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	msg := &Message{
		ID:        msgID,
		Role:      metadata.Role,
		Content:   string(contentBytes),
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
		return fmt.Errorf("failed to create temp file: %w", err)
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
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Sync to disk
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Close before rename
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	tmpFile = nil // Prevent defer cleanup

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// ListMessages returns all message IDs in a directory, sorted
func ListMessages(sessionPath string) ([]string, error) {
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	messageIDs := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasSuffix(name, ".txt") {
			msgID := strings.TrimSuffix(name, ".txt")
			// Verify matching .json exists
			if _, err := os.Stat(filepath.Join(sessionPath, msgID+".json")); err == nil {
				messageIDs[msgID] = true
			}
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
	msgIDs, err := ListMessages(dirPath)
	if err != nil {
		return nil, err
	}

	messages := make([]Message, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		msg, err := ReadMessage(dirPath, msgID)
		if err != nil {
			// Skip corrupted messages with warning
			fmt.Fprintf(os.Stderr, "Warning: skipping corrupted message %s: %v\n", msgID, err)
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
		return nil, fmt.Errorf("failed to read directory: %w", err)
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

// ensureSessionDir creates a session directory if it doesn't exist
func ensureSessionDir(path string) error {
	return os.MkdirAll(path, 0o750)
}

// IsAssistantMessage checks if the given branch path points to an assistant message
func IsAssistantMessage(branchPath string) (bool, error) {
	if branchPath == "" {
		return false, fmt.Errorf("branch path cannot be empty")
	}

	sessionPath, messageID := ParseMessageID(branchPath)
	if messageID == "" {
		// Not a message path, but this is not necessarily an error
		// The path might be a session directory
		return false, nil
	}

	msg, err := ReadMessage(sessionPath, messageID)
	if err != nil {
		return false, fmt.Errorf("failed to read message %s: %w", messageID, err)
	}

	return msg.Role == "assistant", nil
}
