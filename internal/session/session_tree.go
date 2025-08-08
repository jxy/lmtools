package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TreeNode represents a node in the conversation tree
type TreeNode struct {
	Message    *Message
	Children   []*TreeNode
	IsSibling  bool
	SiblingNum string
}

// formatBytes formats byte count for display
func formatBytes(bytes int) string {
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

// formatRole formats role with optional model
func formatRole(role, model string) string {
	if role == "assistant" && model != "" {
		return fmt.Sprintf("%s/%s", role, model)
	}
	return role
}

// ShowSessions displays all conversation trees
func ShowSessions() error {
	sessionsDir := GetSessionsDir()

	// Check if sessions directory exists
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		fmt.Println("No sessions found.")
		return nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return fmt.Errorf("failed to read sessions directory: %w", err)
	}

	// Filter for session directories
	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			sessions = append(sessions, entry.Name())
		}
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	sort.Strings(sessions)

	// Display each session
	for i, sessionID := range sessions {
		if i > 0 {
			fmt.Println() // Blank line between sessions
		}

		sessionPath := filepath.Join(sessionsDir, sessionID)

		// Get messages to show creation time and calculate total size
		messages, err := ListMessages(sessionPath)
		var created time.Time
		var totalSize int
		messageCount := len(messages)

		if err == nil && len(messages) > 0 {
			if msg, err := ReadMessage(sessionPath, messages[0]); err == nil {
				created = msg.Timestamp
			}
			// Calculate total size
			for _, msgID := range messages {
				if msg, err := ReadMessage(sessionPath, msgID); err == nil {
					totalSize += len(msg.Content)
				}
			}
		}

		if !created.IsZero() {
			fmt.Printf("%s • %s • %d messages • %s\n", sessionID, created.Format("2006-01-02 15:04:05"), messageCount, formatBytes(totalSize))
		} else {
			fmt.Printf("%s • %d messages • %s\n", sessionID, messageCount, formatBytes(totalSize))
		}

		// Build and display tree
		tree, err := buildTree(sessionPath)
		if err != nil {
			fmt.Printf("  Error reading session: %v\n", err)
			continue
		}

		displayTree(tree, "", true)
	}

	return nil
}

// ShowTree displays a specific session tree
func ShowTree(sessionID string) error {
	sessionPath := filepath.Join(GetSessionsDir(), sessionID)

	// Check if session exists
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Build and display tree
	tree, err := buildTree(sessionPath)
	if err != nil {
		return fmt.Errorf("failed to build tree: %w", err)
	}

	fmt.Printf("%s/\n", sessionID)
	displayTree(tree, "", true)

	return nil
}

// buildTree recursively builds a tree from a session directory
func buildTree(dirPath string) ([]*TreeNode, error) {
	// Get all messages in this directory
	messages, err := loadMessagesInDir(dirPath)
	if err != nil {
		return nil, err
	}

	// Create nodes for messages
	nodes := make([]*TreeNode, 0, len(messages))
	messageMap := make(map[string]*TreeNode)

	for i := range messages {
		node := &TreeNode{
			Message:  &messages[i],
			Children: []*TreeNode{},
		}
		nodes = append(nodes, node)
		messageMap[messages[i].ID] = node
	}

	// Check for sibling directories
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nodes, nil // Return what we have
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if isSibling, msgID, sibNum := IsSiblingDir(name); isSibling {
			// Find the parent message node
			if parentNode, exists := messageMap[msgID]; exists {
				// Build sibling subtree
				sibPath := filepath.Join(dirPath, name)
				sibTree, err := buildTree(sibPath)
				if err != nil {
					continue // Skip problematic siblings
				}

				// Create a container node for the sibling branch
				sibContainer := &TreeNode{
					IsSibling:  true,
					SiblingNum: sibNum,
					Children:   sibTree,
				}

				parentNode.Children = append(parentNode.Children, sibContainer)
			}
		}
	}

	return nodes, nil
}

// displayTree recursively displays a tree with proper formatting
func displayTree(nodes []*TreeNode, prefix string, isLast bool) {
	for i, node := range nodes {
		isLastNode := (i == len(nodes)-1)

		if node.IsSibling {
			// Display sibling branch indicator
			fmt.Printf("%s", prefix)
			if isLastNode {
				fmt.Printf("└─ ")
			} else {
				fmt.Printf("├─ ")
			}
			fmt.Printf(".s.%s/\n", node.SiblingNum)

			// Display sibling children
			childPrefix := prefix
			if isLastNode {
				childPrefix += "   "
			} else {
				childPrefix += "│  "
			}
			displayTree(node.Children, childPrefix, isLastNode)
		} else {
			// Display message
			fmt.Printf("%s", prefix)
			if isLastNode {
				fmt.Printf("└─ ")
			} else {
				fmt.Printf("├─ ")
			}

			// Format message
			content := truncateContent(node.Message.Content, 60)

			// Format role with model and size
			roleDisplay := formatRole(node.Message.Role, node.Message.Model)
			size := formatBytes(len(node.Message.Content))

			fmt.Printf("%s • %s • %s • \"%s\"\n", node.Message.ID, roleDisplay, size, content)

			// Display children
			if len(node.Children) > 0 {
				childPrefix := prefix
				if isLastNode {
					childPrefix += "   "
				} else {
					childPrefix += "│  "
				}
				displayTree(node.Children, childPrefix, isLastNode)
			}
		}
	}
}

// truncateContent truncates content to a maximum length
func truncateContent(content string, maxLen int) string {
	// Remove newlines and extra spaces
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.Join(strings.Fields(content), " ")

	if len(content) <= maxLen {
		return content
	}

	return content[:maxLen-3] + "..."
}

// CountSessions returns the number of sessions
func CountSessions() (int, error) {
	sessionsDir := GetSessionsDir()

	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return 0, nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			count++
		}
	}

	return count, nil
}
