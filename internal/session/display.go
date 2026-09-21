package session

import (
	stdErrors "errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/format"
	"os"
	"path/filepath"
	"strings"
)

// FormatRole formats role with optional model
func FormatRole(role, model string) string {
	if role == "assistant" && model != "" {
		return fmt.Sprintf("%s/%s", role, model)
	}
	return role
}

// ShowMessageWithManager displays a single message with its metadata using the provided manager.
func ShowMessageWithManager(manager *Manager, messagePath string, notifier core.Notifier) error {
	if manager == nil {
		manager = DefaultManager()
	}
	// Extract directory and message ID
	dir := filepath.Dir(messagePath)
	msgID := filepath.Base(messagePath)

	// Read the message
	msg, err := readMessage(dir, msgID)
	if err != nil {
		return errors.WrapError("read message", err)
	}

	// Get the relative path for display
	relPath, err := filepath.Rel(manager.SessionsDir(), messagePath)
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
	images := loadDisplayImages(dir, msgID, notifier)
	printImageLines("", images.user)

	// Load and display tool interactions if present
	toolInteraction, err := LoadToolInteraction(dir, msgID)
	if err != nil {
		// Log error but continue (tool interactions are optional)
		if notifier != nil {
			notifier.Warnf("Failed to load tool interactions: %v", err)
		}
	} else if toolInteraction != nil {
		printToolInteraction(toolInteraction, images)
	}

	return nil
}

// ShowDispatcher routes the show command to the appropriate handler
func ShowDispatcher(showArg string, notifier core.Notifier) error {
	return ShowDispatcherWithManager(DefaultManager(), showArg, notifier)
}

// ShowDispatcherWithManager routes the show command using the provided manager.
func ShowDispatcherWithManager(manager *Manager, showArg string, notifier core.Notifier) error {
	if manager == nil {
		manager = DefaultManager()
	}
	// Clean and validate the path
	showArg = strings.TrimSpace(showArg)
	if showArg == "" {
		return errors.WrapError("validate show argument", stdErrors.New("show flag requires a non-empty argument"))
	}

	// Clean the path
	cleanPath := filepath.Clean(showArg)

	// Make it absolute if it's not already
	absPath := manager.ResolveSessionPath(cleanPath)

	// Security check: ensure path is within sessions directory
	if !manager.IsWithinSessionsDir(absPath) {
		return errors.WrapError("validate path", stdErrors.New("invalid path: must be within sessions directory"))
	}

	// Check if it's a directory (session or branch)
	info, err := os.Stat(absPath)
	if err == nil && info.IsDir() {
		// It's a session or branch directory
		return ShowConversation(absPath, notifier)
	}

	// Check if it's a message. A message exists if and only if its .json
	// metadata exists; the .txt is absent for a turn with no text, such as an
	// image-only user message or an assistant message that only called tools.
	dir := filepath.Dir(absPath)
	msgID := filepath.Base(absPath)

	// Try without extension first (user provided just the ID)
	if fileExists(buildMessageFilePaths(dir, msgID).JSONPath) {
		return ShowMessageWithManager(manager, absPath, notifier)
	}

	// Try with the path as-is (might have extension)
	// Remove extension if present
	ext := filepath.Ext(msgID)
	if ext == ".txt" || ext == ".json" {
		msgID = strings.TrimSuffix(msgID, ext)
		if fileExists(buildMessageFilePaths(dir, msgID).JSONPath) {
			msgPath := filepath.Join(dir, msgID)
			return ShowMessageWithManager(manager, msgPath, notifier)
		}
	}

	return errors.WrapError("find path", stdErrors.New("path not found: "+showArg))
}

// displayImages is what -show learns about a message's images from its blocks
// file: the images of the user turn itself, and the images each tool result
// carries, keyed by tool_use_id. Both live only in .blocks.json — the text
// file holds the prompt and .tools.json holds a result's text — so this is
// the one read that finds them. The bytes are never printed.
type displayImages struct {
	user     []core.ImageBlock
	byResult map[string][]core.ImageBlock
}

func loadDisplayImages(msgDir, msgID string, notifier core.Notifier) displayImages {
	var images displayImages
	blocks, ok, err := loadMessageBlocks(msgDir, msgID)
	if err != nil {
		if notifier != nil {
			notifier.Warnf("Failed to load message blocks: %v", err)
		}
		return images
	}
	if !ok {
		return images
	}
	for _, block := range blocks {
		switch value := block.(type) {
		case core.ImageBlock:
			images.user = append(images.user, value)
		case core.ToolResultBlock:
			if len(value.Images) == 0 {
				continue
			}
			if images.byResult == nil {
				images.byResult = make(map[string][]core.ImageBlock)
			}
			images.byResult[value.ToolUseID] = append(images.byResult[value.ToolUseID], value.Images...)
		}
	}
	return images
}

// printImageLines lists images one line each, in the one form the user turn
// and the tool results share.
func printImageLines(indent string, images []core.ImageBlock) {
	for _, image := range images {
		fmt.Printf("%s[image: %s]\n", indent, core.DescribeImageBlock(image))
	}
}

// printToolInteraction renders a message's tool calls and results. A result's
// text comes from .tools.json and its images from the blocks file, printed
// under the output they arrived with.
func printToolInteraction(toolInteraction *core.ToolInteraction, images displayImages) {
	// Display tool calls if present
	if len(toolInteraction.Calls) > 0 {
		fmt.Println("\n>>> Tool Calls:")
		for _, call := range toolInteraction.Calls {
			// Pretty print the arguments
			args := format.PrettyJSONArgs(call.Args, "     ")
			fmt.Printf("  • %s (ID: %s)\n", call.Name, call.ID)
			if call.MCPServer != "" {
				fmt.Printf("     MCP: %s/%s\n", call.MCPServer, call.MCPTool)
			}
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
				continue
			}
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
			printImageLines("     ", images.byResult[result.ID])
		}
	}
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
		if err := displayMessageWithTools(sessionPath, msg, messageIndex, notifier); err != nil {
			// Fall back to simple display on error
			roleDisplay := FormatRole(string(msg.Role), msg.Model)
			fmt.Printf("[%s] %s\n", roleDisplay, msg.Timestamp.Format("2006-01-02 15:04:05"))
			fmt.Println(msg.Content)
		}
	}

	return nil
}

// displayMessageWithTools displays a message with any associated tool interactions
func displayMessageWithTools(sessionPath string, msg Message, messageIndex map[string]string, notifier core.Notifier) error {
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
	images := loadDisplayImages(msgDir, msg.ID, notifier)
	printImageLines("", images.user)

	// Load and display tool interactions if present
	toolInteraction, err := LoadToolInteraction(msgDir, msg.ID)
	if err != nil {
		return err
	}

	if toolInteraction != nil {
		printToolInteraction(toolInteraction, images)
	}

	return nil
}
