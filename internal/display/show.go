package display

import (
	"fmt"
	"lmtools/internal/session"
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
		absPath = filepath.Join(session.GetSessionsDir(), cleanPath)
	}

	// Security check: ensure path is within sessions directory
	sessionsDir := session.GetSessionsDir()
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
	sessionID := session.GetSessionID(sessionPath)

	// Get the lineage for this path
	messages, err := session.GetLineage(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to get conversation: %w", err)
	}

	// Calculate total size
	var totalSize int
	for _, msg := range messages {
		totalSize += len(msg.Content)
	}

	// Print session header
	fmt.Printf("Session: %s\n", sessionID)

	// Try to get creation time from first message
	if len(messages) > 0 {
		fmt.Printf("Created: %s\n", messages[0].Timestamp.Format("2006-01-02 15:04:05"))
	}

	fmt.Printf("Messages: %d\n", len(messages))
	fmt.Printf("Total: %s\n", FormatBytes(totalSize))

	fmt.Println() // Blank line after header

	// Print each message
	for _, msg := range messages {
		// Format the role display
		roleDisplay := FormatRole(msg.Role, msg.Model)
		size := FormatBytes(len(msg.Content))

		// Print message header
		fmt.Printf("%s • %s • %s • %s\n", msg.ID, roleDisplay, msg.Timestamp.Format("2006-01-02 15:04:05"), size)

		// Print message content
		fmt.Println(msg.Content)
		fmt.Println() // Blank line between messages
	}

	return nil
}
