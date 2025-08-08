package display

import (
	"fmt"
	"lmtools/internal/session"
	"path/filepath"
)

// ShowMessage displays a single message with its metadata
func ShowMessage(messagePath string) error {
	// Extract directory and message ID
	dir := filepath.Dir(messagePath)
	msgID := filepath.Base(messagePath)

	// Read the message
	msg, err := session.ReadMessage(dir, msgID)
	if err != nil {
		return fmt.Errorf("failed to read message: %w", err)
	}

	// Get the relative path for display
	relPath, err := filepath.Rel(session.GetSessionsDir(), messagePath)
	if err != nil {
		relPath = messagePath
	}

	// Print message details
	fmt.Printf("Message: %s\n", relPath)
	fmt.Printf("Type: %s\n", FormatRole(msg.Role, msg.Model))
	fmt.Printf("Created: %s\n", msg.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("Size: %d bytes\n", len(msg.Content))
	fmt.Printf("\n%s\n", msg.Content)

	return nil
}
