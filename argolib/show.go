package argo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ShowDispatcher routes the show command to the appropriate handler
func ShowDispatcher(showArg string) error {
	// Clean and validate the path
	showArg = strings.TrimSpace(showArg)
	if showArg == "" {
		return fmt.Errorf("show flag requires a non-empty argument")
	}

	// Clean the path
	cleanPath := filepath.Clean(showArg)

	// Make it absolute if it's not already
	var absPath string
	if filepath.IsAbs(cleanPath) {
		absPath = cleanPath
	} else {
		absPath = filepath.Join(GetSessionsDir(), cleanPath)
	}

	// Security check: ensure path is within sessions directory
	sessionsDir := GetSessionsDir()
	if !strings.HasPrefix(absPath, sessionsDir+string(filepath.Separator)) && absPath != sessionsDir {
		return fmt.Errorf("invalid path: must be within sessions directory")
	}

	// Check if it's a directory (session or branch)
	info, err := os.Stat(absPath)
	if err == nil && info.IsDir() {
		// It's a session or branch directory
		return ShowConversation(absPath)
	}

	// Check if it's a message (check for .txt and .json files)
	dir := filepath.Dir(absPath)
	msgID := filepath.Base(absPath)

	// Check if the message files exist
	contentPath := filepath.Join(dir, msgID+".txt")
	metaPath := filepath.Join(dir, msgID+".json")

	if _, err := os.Stat(contentPath); err == nil {
		if _, err := os.Stat(metaPath); err == nil {
			// Both files exist, it's a message
			return ShowMessage(absPath)
		}
	}

	// Not found
	return fmt.Errorf("session or message not found: %s", showArg)
}

// ShowConversation displays the linear conversation chain for a session or branch
func ShowConversation(sessionPath string) error {
	// Get the session ID for display
	sessionID := GetSessionID(sessionPath)

	// Get the lineage for this path
	messages, err := GetLineage(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to get conversation: %w", err)
	}

	// Print session header
	fmt.Printf("Session: %s\n", sessionID)

	// Try to get creation time from first message
	if len(messages) > 0 {
		fmt.Printf("Created: %s\n", messages[0].Timestamp.Format("2006-01-02 15:04:05"))
	}

	fmt.Println() // Blank line after header

	// Print each message
	for _, msg := range messages {
		// Format the role display
		roleDisplay := fmt.Sprintf("[%s]", msg.Role)
		if msg.Role == "assistant" && msg.Model != "" {
			roleDisplay = fmt.Sprintf("[%s(%s)]", msg.Role, msg.Model)
		}

		// Print message header
		fmt.Printf("%s %s %s\n", msg.ID, roleDisplay, msg.Timestamp.Format("2006-01-02 15:04:05"))

		// Print message content
		fmt.Println(msg.Content)
		fmt.Println() // Blank line between messages
	}

	return nil
}

// ShowMessage displays a single message with its metadata
func ShowMessage(messagePath string) error {
	// Extract directory and message ID
	dir := filepath.Dir(messagePath)
	msgID := filepath.Base(messagePath)

	// Read the message
	msg, err := ReadMessage(dir, msgID)
	if err != nil {
		return fmt.Errorf("failed to read message: %w", err)
	}

	// Get the relative path for display
	relPath, err := filepath.Rel(GetSessionsDir(), messagePath)
	if err != nil {
		relPath = messagePath
	}

	// Print message details
	fmt.Printf("Message: %s\n", relPath)
	fmt.Printf("Role: %s\n", msg.Role)
	if msg.Model != "" {
		fmt.Printf("Model: %s\n", msg.Model)
	}
	fmt.Printf("Timestamp: %s\n", msg.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Println()
	fmt.Println(msg.Content)

	return nil
}
