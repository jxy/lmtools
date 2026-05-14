package session

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TreeNode represents a node in the conversation tree
type TreeNode struct {
	Message         *Message
	Children        []*TreeNode
	IsSibling       bool
	SiblingNum      string
	ToolInteraction *core.ToolInteraction // Tool calls or results associated with this message
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

// ShowSessions displays all conversation trees
func ShowSessions(notifier core.Notifier) error {
	return ShowSessionsWithManager(DefaultManager(), notifier)
}

// ShowSessionsWithManager displays all conversation trees using the provided manager.
func ShowSessionsWithManager(manager *Manager, notifier core.Notifier) error {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionsDir := manager.SessionsDir()

	// Check if sessions directory exists
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		fmt.Println("No sessions found.")
		return nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return errors.WrapError("read sessions directory", err)
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
		messages, err := listMessages(sessionPath)
		var created time.Time
		var totalSize int
		messageCount := len(messages)

		if err == nil && len(messages) > 0 {
			if msg, err := readMessage(sessionPath, messages[0]); err == nil {
				created = msg.Timestamp
			}
			// Calculate total size using file sizes instead of reading content
			// This is much faster as it avoids loading file content into memory
			for _, msgID := range messages {
				txtPath := filepath.Join(sessionPath, msgID+".txt")
				if info, err := os.Stat(txtPath); err == nil {
					totalSize += int(info.Size())
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
			if notifier != nil {
				notifier.Warnf("Error reading session %s: %v", sessionID, err)
			}
			continue
		}

		displayTree(tree, "", true)
	}

	return nil
}

// ShowTree displays a specific session tree
func ShowTree(sessionID string) error {
	return ShowTreeWithManager(DefaultManager(), sessionID)
}

// ShowTreeWithManager displays a specific session tree using the provided manager.
func ShowTreeWithManager(manager *Manager, sessionID string) error {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionPath := manager.ResolveSessionPath(sessionID)

	// Check if session exists
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		return errors.WrapError("find session", fmt.Errorf("session not found: %s", sessionID))
	}

	// Build and display tree
	tree, err := buildTree(sessionPath)
	if err != nil {
		return errors.WrapError("build tree", err)
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
		// Load tool interaction if it exists
		toolInteraction, _ := LoadToolInteraction(dirPath, messages[i].ID)

		node := &TreeNode{
			Message:         &messages[i],
			Children:        []*TreeNode{},
			ToolInteraction: toolInteraction,
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
			content := format.Truncate(node.Message.Content, format.MaxArgPreview*3) // Use 3x arg preview for content

			// Format role with model and size
			roleDisplay := FormatRole(string(node.Message.Role), node.Message.Model)
			size := formatBytes(len(node.Message.Content))

			// Add tool interaction indicators
			toolIndicator := ""
			if node.ToolInteraction != nil {
				if len(node.ToolInteraction.Calls) > 0 {
					// Assistant message with tool calls - show tool names and brief args
					toolIndicator = formatToolCallsInline(node.ToolInteraction.Calls)
				} else if len(node.ToolInteraction.Results) > 0 {
					// User message with tool results - show result preview and size
					toolIndicator = formatToolResultsInline(node.ToolInteraction.Results)
				}
			}

			fmt.Printf("%s • %s • %s • \"%s\"%s\n", node.Message.ID, roleDisplay, size, content, toolIndicator)

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

// formatToolCallsInline formats tool calls as a brief inline summary
func formatToolCallsInline(calls []core.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}

	// Format: [tool: name(args...)]
	summaries := []string{}
	for _, call := range calls {
		argSummary := ""
		if len(call.Args) > 0 {
			// Try to extract key information from args
			var args map[string]interface{}
			if err := json.Unmarshal(call.Args, &args); err == nil {
				// Build brief arg summary
				parts := []string{}
				// Prioritize common fields
				if path, ok := args["path"].(string); ok {
					parts = append(parts, format.Truncate(path, format.MaxArgPreview))
				}
				if cmd, ok := args["command"]; ok {
					switch v := cmd.(type) {
					case []interface{}:
						if len(v) > 0 {
							parts = append(parts, fmt.Sprintf("%v", v[0]))
						}
					case string:
						parts = append(parts, format.Truncate(v, format.MaxArgPreview))
					}
				}
				if query, ok := args["query"].(string); ok {
					parts = append(parts, format.Truncate(query, format.MaxArgPreview))
				}
				// If no known fields, show first field
				if len(parts) == 0 && len(args) > 0 {
					for _, v := range args {
						parts = append(parts, format.Truncate(fmt.Sprintf("%v", v), format.MaxArgPreview))
						break
					}
				}
				if len(parts) > 0 {
					argSummary = strings.Join(parts, ", ")
				}
			} else {
				// Not a map, just show truncated raw
				argSummary = format.Truncate(string(call.Args), format.MaxArgPreview)
			}
		}

		if argSummary != "" {
			summaries = append(summaries, fmt.Sprintf("%s(%s)", call.Name, argSummary))
		} else {
			summaries = append(summaries, call.Name)
		}
	}

	return fmt.Sprintf(" [tool: %s]", strings.Join(summaries, "; "))
}

// formatToolResultsInline formats tool results as a brief inline summary
func formatToolResultsInline(results []core.ToolResult) string {
	if len(results) == 0 {
		return ""
	}

	// Format: [result: preview... (size)]
	summaries := []string{}
	for _, result := range results {
		if result.Error != "" {
			summaries = append(summaries, fmt.Sprintf("error: %s", format.Truncate(result.Error, format.MaxArgPreview)))
		} else {
			// Clean up output for display
			output := strings.TrimSpace(result.Output)
			// Replace newlines with spaces for inline display
			output = strings.ReplaceAll(output, "\n", " ")
			// Collapse multiple spaces
			for strings.Contains(output, "  ") {
				output = strings.ReplaceAll(output, "  ", " ")
			}

			preview := format.Truncate(output, format.MaxResultPreview)
			size := formatBytes(len(result.Output))
			summaries = append(summaries, fmt.Sprintf("%s (%s)", preview, size))
		}
	}

	return fmt.Sprintf(" [result: %s]", strings.Join(summaries, "; "))
}

// CountSessions returns the number of sessions
func CountSessions() (int, error) {
	return CountSessionsWithManager(DefaultManager())
}

// CountSessionsWithManager returns the number of sessions visible to the provided manager.
func CountSessionsWithManager(manager *Manager) (int, error) {
	if manager == nil {
		manager = DefaultManager()
	}
	sessionsDir := manager.SessionsDir()

	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return 0, nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return 0, errors.WrapError("read sessions directory", err)
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			count++
		}
	}

	return count, nil
}
