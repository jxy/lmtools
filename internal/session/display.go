package session

import (
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ShowSessionTree displays the conversation tree for a session
func ShowSessionTree(sessionPath string, notifier core.Notifier) error {
	// Get session info
	sessionID := GetSessionID(sessionPath)

	// Build the conversation tree
	nodes, err := buildTree(sessionPath)
	if err != nil {
		return errors.WrapError("build conversation tree", err)
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
func ShowMessage(messagePath string, notifier core.Notifier) error {
	// Extract directory and message ID
	dir := filepath.Dir(messagePath)
	msgID := filepath.Base(messagePath)

	// Read the message
	msg, err := readMessage(dir, msgID)
	if err != nil {
		return errors.WrapError("read message", err)
	}

	// Get the relative path for display
	relPath, err := filepath.Rel(GetSessionsDir(), messagePath)
	if err != nil {
		relPath = messagePath
	}

	// Print message details
	fmt.Printf("Message: %s\n", relPath)
	fmt.Printf("Type: %s\n", FormatRole(string(msg.Role), msg.Model))
	fmt.Printf("Created: %s\n", msg.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("Size: %d bytes\n", len(msg.Content))

	// Print message content if present
	if msg.Content != "" {
		fmt.Printf("\n%s\n", msg.Content)
	}

	// Load and display tool interactions if present
	toolInteraction, err := LoadToolInteraction(dir, msgID)
	if err != nil {
		// Log error but continue (tool interactions are optional)
		if notifier != nil {
			notifier.Warnf("Failed to load tool interactions: %v", err)
		}
	} else if toolInteraction != nil {
		// Display tool calls if present
		if len(toolInteraction.Calls) > 0 {
			fmt.Println("\n>>> Tool Calls:")
			for _, call := range toolInteraction.Calls {
				// Pretty print the arguments
				args := format.PrettyJSONArgs(call.Args, "     ")
				fmt.Printf("  • %s (ID: %s)\n", call.Name, call.ID)
				if args != "" {
					fmt.Printf("     Args: %s\n", args)
				}
			}
		}

		// Display tool results if present
		if len(toolInteraction.Results) > 0 {
			fmt.Println("\n>>> Tool Results:")
			for _, result := range toolInteraction.Results {
				if result.Error != "" {
					fmt.Printf("  [ERROR] (ID: %s): %s\n", result.ID, result.Error)
				} else {
					// Truncate long output for display
					output := format.Truncate(result.Output, format.MaxToolOutputDisplay)
					status := "[OK]"
					if result.Truncated {
						status = "[TRUNCATED]"
					}
					fmt.Printf("  %s Result (ID: %s, %dms):\n", status, result.ID, result.Elapsed)
					// Indent the output
					lines := strings.Split(output, "\n")
					for _, line := range lines {
						if line != "" {
							fmt.Printf("     %s\n", line)
						}
					}
				}
			}
		}
	}

	return nil
}

// ShowDispatcher routes the show command to the appropriate handler
func ShowDispatcher(showArg string, notifier core.Notifier) error {
	// Clean and validate the path
	showArg = strings.TrimSpace(showArg)
	if showArg == "" {
		return errors.WrapError("validate show argument", stdErrors.New("show flag requires a non-empty argument"))
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
		return errors.WrapError("validate path", stdErrors.New("invalid path: must be within sessions directory"))
	}

	// Check if it's a directory (session or branch)
	info, err := os.Stat(absPath)
	if err == nil && info.IsDir() {
		// It's a session or branch directory
		return ShowConversation(absPath, notifier)
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
			return ShowMessage(absPath, notifier)
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
				return ShowMessage(msgPath, notifier)
			}
		}
	}

	return errors.WrapError("find path", stdErrors.New("path not found: "+showArg))
}

// ShowConversation shows a full conversation or branch
func ShowConversation(sessionPath string, notifier core.Notifier) error {
	// Read all messages in the conversation
	messages, err := GetLineage(sessionPath)
	if err != nil {
		return errors.WrapError("read conversation", err)
	}

	if len(messages) == 0 {
		fmt.Println("No messages in conversation.")
		return nil
	}

	// Build index of message locations for O(1) lookups
	messageIndex, err := indexMessageDirectories(sessionPath)
	if err != nil {
		return errors.WrapError("index message directories", err)
	}

	// Get session info for the header
	sessionID := GetSessionID(sessionPath)
	fmt.Printf("Conversation %s:\n\n", sessionID)

	// Print each message with tool interactions
	for i, msg := range messages {
		// Print separator between messages (except for first)
		if i > 0 {
			fmt.Println("\n---")
		}

		// Display the message with tool interactions
		if err := displayMessageWithTools(sessionPath, msg, messageIndex); err != nil {
			// Fall back to simple display on error
			roleDisplay := FormatRole(string(msg.Role), msg.Model)
			fmt.Printf("[%s] %s\n", roleDisplay, msg.Timestamp.Format("2006-01-02 15:04:05"))
			fmt.Println(msg.Content)
		}
	}

	return nil
}

// displayMessageWithTools displays a message with any associated tool interactions
func displayMessageWithTools(sessionPath string, msg Message, messageIndex map[string]string) error {
	// Print message header
	roleDisplay := FormatRole(string(msg.Role), msg.Model)
	fmt.Printf("[%s] %s\n", roleDisplay, msg.Timestamp.Format("2006-01-02 15:04:05"))

	// Print message content if present
	if msg.Content != "" {
		fmt.Println(msg.Content)
	}

	// Determine the directory containing this message
	msgDir := messageIndex[msg.ID]
	if msgDir == "" {
		// Fallback to session path if not found in index
		msgDir = sessionPath
	}

	// Load and display tool interactions if present
	toolInteraction, err := LoadToolInteraction(msgDir, msg.ID)
	if err != nil {
		return err
	}

	if toolInteraction != nil {
		// Display tool calls if present
		if len(toolInteraction.Calls) > 0 {
			fmt.Println("\n>>> Tool Calls:")
			for _, call := range toolInteraction.Calls {
				// Pretty print the arguments
				args := format.PrettyJSONArgs(call.Args, "     ")
				fmt.Printf("  • %s (ID: %s)\n", call.Name, call.ID)
				if args != "" {
					fmt.Printf("     Args: %s\n", args)
				}
			}
		}

		// Display tool results if present
		if len(toolInteraction.Results) > 0 {
			fmt.Println("\n>>> Tool Results:")
			for _, result := range toolInteraction.Results {
				if result.Error != "" {
					fmt.Printf("  [ERROR] (ID: %s): %s\n", result.ID, result.Error)
				} else {
					// Truncate long output for display
					output := format.Truncate(result.Output, format.MaxToolOutputDisplay)
					status := "[OK]"
					if result.Truncated {
						status = "[TRUNCATED]"
					}
					fmt.Printf("  %s Result (ID: %s, %dms):\n", status, result.ID, result.Elapsed)
					// Indent the output
					lines := strings.Split(output, "\n")
					for _, line := range lines {
						if line != "" {
							fmt.Printf("     %s\n", line)
						}
					}
				}
			}
		}
	}

	return nil
}
