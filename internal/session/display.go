package session

import (
	"context"
	stdErrors "errors"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/ui"
	"lmtools/internal/ui/tools"
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
	return showMessageTo(os.Stdout, manager, messagePath, notifier)
}

func showMessageTo(w io.Writer, manager *Manager, messagePath string, notifier core.Notifier) error {
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
	fmt.Fprintf(w, "Message: %s\n", relPath)
	fmt.Fprintf(w, "Type: %s\n", FormatRole(string(msg.Role), msg.Model))
	fmt.Fprintf(w, "Created: %s\n", msg.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "Size: %d bytes\n", len(msg.Content))

	// Print message content if present
	if msg.Content != "" {
		fmt.Fprintf(w, "\n%s\n", msg.Content)
	}
	images := loadDisplayImages(dir, msgID, notifier)
	printImageLinesTo(w, "", images.user)

	// Load and display tool interactions if present
	toolInteraction, err := LoadToolInteraction(dir, msgID)
	if err != nil {
		// Log error but continue (tool interactions are optional)
		if notifier != nil {
			notifier.Warnf("Failed to load tool interactions: %v", err)
		}
	} else if toolInteraction != nil {
		printToolInteractionTo(w, toolInteraction, images, nil, nil)
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
func printImageLinesTo(w io.Writer, indent string, images []core.ImageBlock) {
	for _, image := range images {
		fmt.Fprintf(w, "%s[image: %s]\n", indent, core.DescribeImageBlock(image))
	}
}

// printToolInteraction renders a message's tool calls and results. A result's
// text comes from .tools.json and its images from the blocks file, printed
// under the output they arrived with.
func printToolInteractionTo(w io.Writer, interaction *core.ToolInteraction, images displayImages, byID map[string]core.ToolCall, columns func() int) {
	renderer := tools.NewCLIToolUIWithOptions(ui.NewNotifierWithWriter(w), tools.Options{Columns: columns})
	if byID == nil {
		byID = make(map[string]core.ToolCall, len(interaction.Calls))
	}
	for i, call := range interaction.Calls {
		renderer.ShowCall(i, len(interaction.Calls), call, nil)
		byID[call.ID] = call
	}
	if len(interaction.Results) > 0 {
		calls := make([]core.ToolCall, len(interaction.Results))
		for i, result := range interaction.Results {
			calls[i] = byID[result.ID]
			if calls[i].Name == "" {
				calls[i].Name = result.ID
			}
		}
		results := append([]core.ToolResult(nil), interaction.Results...)
		for i := range results {
			results[i].Images = images.byResult[results[i].ID]
		}
		renderer.AfterExecute(calls, results)
	}
}

// ShowConversation shows a full conversation or branch
func ShowConversation(sessionPath string, notifier core.Notifier) error {
	return ShowConversationTo(os.Stdout, sessionPath, notifier)
}

// ShowConversationTo renders saved history on the requested surface.
func ShowConversationTo(w io.Writer, sessionPath string, notifier core.Notifier) error {
	return ShowSessionTo(context.Background(), w, &Session{Path: sessionPath}, notifier)
}

// displayMessageWithToolsTo reads sidecars from the message's own directory.
// IDs are local to a directory; branches routinely reuse their ancestors' IDs.
// interaction is the message's tool calls or results, nil when it has none.
func displayMessageWithToolsTo(w io.Writer, ref lineageMessageRef, interaction *core.ToolInteraction, notifier core.Notifier, knownCalls map[string]core.ToolCall, options ShowOptions) error {
	msg := ref.message
	// Print message header
	roleDisplay := FormatRole(string(msg.Role), msg.Model)
	if isToolResults(msg, interaction) {
		roleDisplay = "tool results"
	}
	if msg.Role == core.RoleSystem {
		// The system message is written when the session is, after the
		// first answer; a time on it would read as later than the prompt
		// it precedes.
		fmt.Fprintf(w, "[%s]\n", roleDisplay)
	} else {
		fmt.Fprintf(w, "[%s] %s\n", roleDisplay, msg.Timestamp.Format("2006-01-02 15:04:05"))
	}

	if msg.Role == core.RoleSystem && options.SystemSummary && strings.Count(msg.Content, "\n") >= 3 {
		fmt.Fprintf(w, "(%d lines; /show system prints it)\n", 1+strings.Count(msg.Content, "\n"))
		return nil
	}
	if msg.Role == core.RoleAssistant && options.Thinking {
		printSavedThinking(w, ref, notifier)
	}

	// Print message content if present
	if msg.Content != "" {
		fmt.Fprintln(w, msg.Content)
	}

	images := loadDisplayImages(ref.path, msg.ID, notifier)
	printImageLinesTo(w, "", images.user)

	if interaction != nil {
		printToolInteractionTo(w, interaction, images, knownCalls, options.Columns)
	}
	return nil
}

// printSavedThinking shows the visible reasoning text saved with an answer,
// quoted between the markers a live turn uses. Opaque reasoning shows as
// omitted, as it did live.
func printSavedThinking(w io.Writer, ref lineageMessageRef, notifier core.Notifier) {
	blocks, ok, err := loadMessageBlocks(ref.path, ref.message.ID)
	if err != nil {
		if notifier != nil {
			notifier.Warnf("Failed to load message blocks: %v", err)
		}
		return
	}
	if !ok {
		return
	}
	for _, block := range blocks {
		reasoning, isReasoning := block.(core.ReasoningBlock)
		if !isReasoning {
			continue
		}
		text := strings.TrimRight(core.ReasoningTextForDisplay(reasoning), "\n")
		if text == "" {
			text = "[omitted by provider]"
		}
		fmt.Fprintln(w, "--- thinking ---")
		for _, line := range strings.Split(text, "\n") {
			if line == "" {
				fmt.Fprintln(w, "|")
			} else {
				fmt.Fprintf(w, "| %s\n", line)
			}
		}
		fmt.Fprintln(w, "--- end thinking ---")
	}
}
