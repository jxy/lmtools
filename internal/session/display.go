package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ShowSessionTree displays the conversation tree for a session
func ShowSessionTree(sessionPath string) error {
	// Get session info
	sessionID := GetSessionID(sessionPath)

	// Build the conversation tree
	nodes, err := buildTree(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to build conversation tree: %w", err)
	}

	if len(nodes) == 0 {
		fmt.Println("No messages in session.")
		return nil
	}

	// Print session header
	fmt.Printf("Session %s:\n", sessionID)

	// Print the tree
	for i, node := range nodes {
		isLast := i == len(nodes)-1
		printTreeNode(node, "", isLast)
	}

	return nil
}

// printTreeNode recursively prints the message tree
func printTreeNode(node *TreeNode, prefix string, isLast bool) {
	if node == nil {
		return
	}

	// Determine the connector
	connector := "├── "
	if isLast {
		connector = "└── "
	}

	// Format the node display
	roleDisplay := fmt.Sprintf("[%s]", node.Message.Role)
	if node.Message.Role == "assistant" && node.Message.Model != "" {
		roleDisplay = fmt.Sprintf("[%s(%s)]", node.Message.Role, node.Message.Model)
	}

	// Print the node
	fmt.Printf("%s%s%s %s %s\n", prefix, connector, node.Message.ID, roleDisplay,
		node.Message.Timestamp.Format("2006-01-02 15:04:05"))

	// Prepare prefix for children
	childPrefix := prefix
	if isLast {
		childPrefix += "    "
	} else {
		childPrefix += "│   "
	}

	// Sort children by ID for consistent display
	children := make([]*TreeNode, 0, len(node.Children))
	children = append(children, node.Children...)
	sort.Slice(children, func(i, j int) bool {
		return children[i].Message.ID < children[j].Message.ID
	})

	// Print children
	for i, child := range children {
		isLastChild := i == len(children)-1
		printTreeNode(child, childPrefix, isLastChild)
	}
}

// FormatBytes formats byte count for display
func FormatBytes(bytes int) string {
	switch {
	case bytes < 1000:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 10*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	case bytes < 1024*1024:
		return fmt.Sprintf("%dKB", bytes/1024)
	case bytes < 10*1024*1024:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	default:
		return fmt.Sprintf("%dMB", bytes/(1024*1024))
	}
}

// FormatRole formats role with optional model
func FormatRole(role, model string) string {
	if role == "assistant" && model != "" {
		return fmt.Sprintf("%s/%s", role, model)
	}
	return role
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
	fmt.Printf("Type: %s\n", FormatRole(msg.Role, msg.Model))
	fmt.Printf("Created: %s\n", msg.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("Size: %d bytes\n", len(msg.Content))
	fmt.Printf("\n%s\n", msg.Content)

	return nil
}

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

	// Try without extension first (user provided just the ID)
	if _, err := os.Stat(contentPath); err == nil {
		if _, err := os.Stat(metaPath); err == nil {
			return ShowMessage(absPath)
		}
	}

	// Try with the path as-is (might have extension)
	// Remove extension if present
	ext := filepath.Ext(msgID)
	if ext == ".txt" || ext == ".json" {
		msgID = strings.TrimSuffix(msgID, ext)
		contentPath = filepath.Join(dir, msgID+".txt")
		metaPath = filepath.Join(dir, msgID+".json")

		if _, err := os.Stat(contentPath); err == nil {
			if _, err := os.Stat(metaPath); err == nil {
				msgPath := filepath.Join(dir, msgID)
				return ShowMessage(msgPath)
			}
		}
	}

	return fmt.Errorf("path not found: %s", showArg)
}

// ShowConversation shows a full conversation or branch
func ShowConversation(sessionPath string) error {
	// Read all messages in the conversation
	messages, err := GetLineage(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to read conversation: %w", err)
	}

	if len(messages) == 0 {
		fmt.Println("No messages in conversation.")
		return nil
	}

	// Get session info for the header
	sessionID := GetSessionID(sessionPath)
	fmt.Printf("Conversation %s:\n\n", sessionID)

	// Print each message
	for i, msg := range messages {
		// Print separator between messages (except for first)
		if i > 0 {
			fmt.Println("\n---")
		}

		// Print message header
		roleDisplay := FormatRole(msg.Role, msg.Model)
		fmt.Printf("[%s] %s\n", roleDisplay, msg.Timestamp.Format("2006-01-02 15:04:05"))

		// Print message content
		fmt.Println(msg.Content)
	}

	return nil
}
