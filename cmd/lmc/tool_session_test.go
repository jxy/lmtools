//go:build integration
// +build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestToolMessageSequencing verifies that tool-related messages are saved with proper IDs
func TestToolMessageSequencing(t *testing.T) {
	// Create a temporary sessions directory
	tempDir, err := os.MkdirTemp("", "lmc-tool-session-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set custom sessions directory
	session.SetSessionsDir(tempDir)
	session.SetSkipFlockCheck(true) // Skip flock check for tests

	// Create a new session
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	var path string

	// Save user message (should be 0000)
	userMsg := session.Message{
		Role:    "user",
		Content: "List the files in the current directory",
	}
	result, err := session.AppendMessageWithToolInteraction(context.Background(), sess, userMsg, nil, nil)
	if err != nil {
		t.Fatalf("Failed to save user message: %v", err)
	}
	if result.MessageID != "0000" {
		t.Errorf("Expected user message ID to be 0000, got %s", result.MessageID)
	}

	// Save assistant response with tool calls (should be 0001)
	assistantText := "I'll list the files in the current directory for you."
	toolCalls := []core.ToolCall{
		{
			ID:   "call-123",
			Name: "universal_command",
			Args: json.RawMessage(`{"command":["ls","-la"]}`),
		},
	}

	result, err = session.SaveAssistantResponseWithTools(context.Background(), sess, assistantText, toolCalls, "gpt5")
	if err != nil {
		t.Fatalf("Failed to save assistant response with tools: %v", err)
	}
	path = result.Path
	if result.MessageID != "0001" {
		t.Errorf("Expected assistant response ID to be 0001, got %s", result.MessageID)
	}

	// Verify files exist for assistant response
	txtPath := filepath.Join(path, "0001.txt")
	jsonPath := filepath.Join(path, "0001.json")
	toolsPath := filepath.Join(path, "0001.tools.json")

	if _, err := os.Stat(txtPath); os.IsNotExist(err) {
		t.Errorf("Expected text file at %s", txtPath)
	}
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		t.Errorf("Expected json file at %s", jsonPath)
	}
	if _, err := os.Stat(toolsPath); os.IsNotExist(err) {
		t.Errorf("Expected tools.json file at %s", toolsPath)
	}

	// Save tool results (should be 0002)
	toolResults := []core.ToolResult{
		{
			ID:     "call-123",
			Output: "total 48\ndrwxr-xr-x 2 user user 4096 Jan 1 12:00 .",
		},
	}

	result, err = session.SaveToolResults(context.Background(), sess, toolResults, "")
	if err != nil {
		t.Fatalf("Failed to save tool results: %v", err)
	}
	path = result.Path
	if result.MessageID != "0002" {
		t.Errorf("Expected tool results ID to be 0002, got %s", result.MessageID)
	}

	// Verify files exist for tool results
	jsonPath = filepath.Join(path, "0002.json")
	toolsPath = filepath.Join(path, "0002.tools.json")

	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		t.Errorf("Expected json file at %s", jsonPath)
	}
	if _, err := os.Stat(toolsPath); os.IsNotExist(err) {
		t.Errorf("Expected tools.json file at %s", toolsPath)
	}

	// Save final assistant response after tool execution (should be 0003)
	finalResponse := "Here are the files in the current directory:\n\ntotal 48\ndrwxr-xr-x 2 user user 4096 Jan 1 12:00 ."

	result, err = session.SaveAssistantResponseWithTools(context.Background(), sess, finalResponse, nil, "gpt5")
	if err != nil {
		t.Fatalf("Failed to save final assistant response: %v", err)
	}
	path = result.Path
	if result.MessageID != "0003" {
		t.Errorf("Expected final assistant response ID to be 0003, got %s", result.MessageID)
	}

	// Verify files exist for final response
	txtPath = filepath.Join(path, "0003.txt")
	jsonPath = filepath.Join(path, "0003.json")

	if _, err := os.Stat(txtPath); os.IsNotExist(err) {
		t.Errorf("Expected text file at %s", txtPath)
	}
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		t.Errorf("Expected json file at %s", jsonPath)
	}

	// Verify no tools.json for final response (no tool calls)
	toolsPath = filepath.Join(path, "0003.tools.json")
	if _, err := os.Stat(toolsPath); !os.IsNotExist(err) {
		t.Errorf("Did not expect tools.json file at %s", toolsPath)
	}

	// Verify total message count
	files, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("Failed to read session directory: %v", err)
	}

	// Count message IDs (should have 0000, 0001, 0002, 0003)
	messageIDs := make(map[string]bool)
	for _, file := range files {
		name := file.Name()
		if strings.HasSuffix(name, ".json") && !strings.Contains(name, "tools") {
			id := strings.TrimSuffix(name, ".json")
			messageIDs[id] = true
		}
	}

	expectedIDs := []string{"0000", "0001", "0002", "0003"}
	for _, expectedID := range expectedIDs {
		if !messageIDs[expectedID] {
			t.Errorf("Missing expected message ID: %s", expectedID)
		}
	}

	if len(messageIDs) != len(expectedIDs) {
		t.Errorf("Expected %d message IDs, got %d", len(expectedIDs), len(messageIDs))
	}
}

// TestToolContinuationSequencing tests multiple rounds of tool calls
func TestToolContinuationSequencing(t *testing.T) {
	// Create a temporary sessions directory
	tempDir, err := os.MkdirTemp("", "lmc-tool-continuation-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set custom sessions directory
	session.SetSessionsDir(tempDir)
	session.SetSkipFlockCheck(true)

	// Create a new session
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Message 0000: User request
	userMsg := session.Message{
		Role:    "user",
		Content: "Create a file test.txt and then list files",
	}
	result, err := session.AppendMessageWithToolInteraction(context.Background(), sess, userMsg, nil, nil)
	if err != nil {
		t.Fatalf("Failed to save user message: %v", err)
	}
	if result.MessageID != "0000" {
		t.Errorf("Expected message ID 0000, got %s", result.MessageID)
	}

	// Message 0001: Assistant response with first tool call
	result, err = session.SaveAssistantResponseWithTools(context.Background(), sess,
		"I'll create the file and then list the directory contents.",
		[]core.ToolCall{{
			ID:   "call-001",
			Name: "universal_command",
			Args: json.RawMessage(`{"command":["touch","test.txt"]}`),
		}},
		"gpt5",
	)
	if err != nil {
		t.Fatalf("Failed to save assistant response: %v", err)
	}
	if result.MessageID != "0001" {
		t.Errorf("Expected message ID 0001, got %s", result.MessageID)
	}

	// Message 0002: First tool results
	result, err = session.SaveToolResults(context.Background(), sess,
		[]core.ToolResult{{
			ID:     "call-001",
			Output: "",
		}},
		"",
	)
	if err != nil {
		t.Fatalf("Failed to save tool results: %v", err)
	}
	if result.MessageID != "0002" {
		t.Errorf("Expected message ID 0002, got %s", result.MessageID)
	}

	// Message 0003: Assistant response with second tool call
	result, err = session.SaveAssistantResponseWithTools(context.Background(), sess,
		"File created. Now listing the directory:",
		[]core.ToolCall{{
			ID:   "call-002",
			Name: "universal_command",
			Args: json.RawMessage(`{"command":["ls","-la"]}`),
		}},
		"gpt5",
	)
	if err != nil {
		t.Fatalf("Failed to save assistant response: %v", err)
	}
	if result.MessageID != "0003" {
		t.Errorf("Expected message ID 0003, got %s", result.MessageID)
	}

	// Message 0004: Second tool results
	result, err = session.SaveToolResults(context.Background(), sess,
		[]core.ToolResult{{
			ID:     "call-002",
			Output: "test.txt\nother.txt",
		}},
		"",
	)
	if err != nil {
		t.Fatalf("Failed to save tool results: %v", err)
	}
	if result.MessageID != "0004" {
		t.Errorf("Expected message ID 0004, got %s", result.MessageID)
	}

	// Message 0005: Final assistant response
	result, err = session.SaveAssistantResponseWithTools(context.Background(), sess,
		"Successfully created test.txt. The directory now contains:\n- test.txt\n- other.txt",
		nil, // No more tool calls
		"gpt5",
	)
	if err != nil {
		t.Fatalf("Failed to save final response: %v", err)
	}
	if result.MessageID != "0005" {
		t.Errorf("Expected message ID 0005, got %s", result.MessageID)
	}

	// Verify the sequence is correct
	sessionPath := sess.Path
	expectedSequence := []struct {
		id       string
		hasText  bool
		hasTools bool
		role     string
	}{
		{"0000", true, false, "user"},      // User request
		{"0001", true, true, "assistant"},  // Assistant with tool call
		{"0002", false, true, "user"},      // Tool results
		{"0003", true, true, "assistant"},  // Assistant with second tool call
		{"0004", false, true, "user"},      // Second tool results
		{"0005", true, false, "assistant"}, // Final assistant response
	}

	for _, expected := range expectedSequence {
		// Check metadata file
		metaPath := filepath.Join(sessionPath, fmt.Sprintf("%s.json", expected.id))
		metaData, err := os.ReadFile(metaPath)
		if err != nil {
			t.Errorf("Failed to read metadata for %s: %v", expected.id, err)
			continue
		}

		var metadata map[string]interface{}
		if err := json.Unmarshal(metaData, &metadata); err != nil {
			t.Errorf("Failed to parse metadata for %s: %v", expected.id, err)
			continue
		}

		if role, ok := metadata["role"].(string); !ok || role != expected.role {
			t.Errorf("Message %s: expected role %s, got %v", expected.id, expected.role, metadata["role"])
		}

		// Check text file
		txtPath := filepath.Join(sessionPath, fmt.Sprintf("%s.txt", expected.id))
		if expected.hasText {
			if _, err := os.Stat(txtPath); os.IsNotExist(err) {
				t.Errorf("Expected text file for %s", expected.id)
			}
		} else {
			if _, err := os.Stat(txtPath); !os.IsNotExist(err) {
				t.Errorf("Did not expect text file for %s", expected.id)
			}
		}

		// Check tools file
		toolsPath := filepath.Join(sessionPath, fmt.Sprintf("%s.tools.json", expected.id))
		if expected.hasTools {
			if _, err := os.Stat(toolsPath); os.IsNotExist(err) {
				t.Errorf("Expected tools.json file for %s", expected.id)
			}
		} else {
			if _, err := os.Stat(toolsPath); !os.IsNotExist(err) {
				t.Errorf("Did not expect tools.json file for %s", expected.id)
			}
		}
	}
}

// TestResumePendingTools tests that pending tool calls are detected when resuming a session
func TestResumePendingTools(t *testing.T) {
	// Create a temporary sessions directory
	tempDir := t.TempDir()
	session.SetSessionsDir(tempDir)
	defer session.SetSessionsDir("") // Reset
	session.SetSkipFlockCheck(true) // Skip flock check for tests

	// Step 1: Create a session with an assistant message containing tool calls
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a user message
	userMsg := session.Message{
		Role:      "user",
		Content:   "List files in examples directory",
		Timestamp: time.Now(),
	}
	_, err = session.AppendMessageWithToolInteraction(context.Background(), sess, userMsg, nil, nil)
	if err != nil {
		t.Fatalf("Failed to append user message: %v", err)
	}

	// Add assistant message with tool calls
	assistantText := "Let me check the examples directory:"
	toolCalls := []core.ToolCall{
		{
			ID:   "test-tool-1",
			Name: "universal_command",
			Args: json.RawMessage(`{"command": ["echo", "test output"]}`),
		},
	}

	result, err := session.SaveAssistantResponseWithTools(context.Background(), sess, assistantText, toolCalls, "test-model")
	if err != nil {
		t.Fatalf("Failed to save assistant response with tools: %v", err)
	}
	msgID := result.MessageID

	sessionPath := sess.Path
	t.Logf("Created session at %s with pending tool call in message %s", sessionPath, msgID)

	// Step 2: Check for pending tools (simulating what main.go does)
	pendingTools, err := session.CheckForPendingToolCalls(context.Background(), sessionPath)
	if err != nil {
		t.Fatalf("Failed to check for pending tools: %v", err)
	}

	// Should detect the pending tool call
	if len(pendingTools) != 1 {
		t.Fatalf("Expected 1 pending tool call, got %d", len(pendingTools))
	}

	if pendingTools[0].ID != "test-tool-1" {
		t.Errorf("Expected tool ID 'test-tool-1', got '%s'", pendingTools[0].ID)
	}

	if pendingTools[0].Name != "universal_command" {
		t.Errorf("Expected tool name 'universal_command', got '%s'", pendingTools[0].Name)
	}

	// Step 3: Simulate tool execution and save results
	toolResults := []core.ToolResult{
		{
			ID:      "test-tool-1",
			Output:  "test output\n",
			Elapsed: 5,
		},
	}

	result2, err := session.SaveToolResults(context.Background(), sess, toolResults, "")
	if err != nil {
		t.Fatalf("Failed to save tool results: %v", err)
	}
	resultPath := result2.Path
	resultMsgID := result2.MessageID

	t.Logf("Tool results saved as message %s", resultMsgID)

	// The results should be saved as the next message (0002)
	if resultMsgID != "0002" {
		t.Errorf("Expected tool results to be message 0002, got %s", resultMsgID)
	}

	// Step 4: Verify the session now has proper sequence
	messages, err := session.GetLineage(resultPath)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// Should have exactly 3 messages:
	// 0000: User message
	// 0001: Assistant with tool calls
	// 0002: Tool results
	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
		for i, msg := range messages {
			t.Logf("Message %d (%s): Role=%s, Content=%q", i, msg.ID, msg.Role, msg.Content)
		}
	}

	// Verify the tool results message
	if len(messages) == 3 {
		toolResultMsg := messages[2]
		if toolResultMsg.Role != "user" {
			t.Errorf("Tool results message should have role 'user', got '%s'", toolResultMsg.Role)
		}

		// Load and verify tool interaction
		toolInteraction, err := session.LoadToolInteraction(resultPath, toolResultMsg.ID)
		if err != nil {
			t.Fatalf("Failed to load tool interaction: %v", err)
		}

		if toolInteraction == nil || len(toolInteraction.Results) != 1 {
			t.Error("Expected tool interaction with 1 result")
		} else {
			if toolInteraction.Results[0].ID != "test-tool-1" {
				t.Errorf("Expected tool result ID 'test-tool-1', got '%s'", toolInteraction.Results[0].ID)
			}
			if toolInteraction.Results[0].Output != "test output\n" {
				t.Errorf("Expected tool output 'test output\\n', got '%s'", toolInteraction.Results[0].Output)
			}
		}
	}

	// Step 5: After tool results, check that there are no more pending tools
	pendingToolsAfter, err := session.CheckForPendingToolCalls(context.Background(), resultPath)
	if err != nil {
		t.Fatalf("Failed to check for pending tools after execution: %v", err)
	}

	if len(pendingToolsAfter) != 0 {
		t.Errorf("Expected no pending tools after execution, got %d", len(pendingToolsAfter))
	}
}

// TestPendingToolsIntegration is a more comprehensive integration test
// that requires the binary to be built
func TestPendingToolsIntegration(t *testing.T) {
	// Get lmc binary
	binPath := getLmcBinary(t)

	// Create temp directories
	tempDir := t.TempDir()
	sessionsDir := filepath.Join(tempDir, "sessions")
	logDir := filepath.Join(tempDir, "logs")
	if err := os.MkdirAll(sessionsDir, constants.DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, constants.DirPerm); err != nil {
		t.Fatal(err)
	}

	// Create whitelist
	whitelistPath := filepath.Join(tempDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["echo"]`+"\n"), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	// Start mock server
	ms := mockserver.NewMockServer()
	ms.SetResponseFunc(func(req *http.Request) (interface{}, int, error) {
		// Simple response for resuming session
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "Continuing the conversation after executing the pending tool.",
				},
			},
		}, 200, nil
	})
	defer ms.Close()
	serverURL := ms.Server.URL

	// Set sessions directory for this test
	session.SetSessionsDir(sessionsDir)
	defer session.SetSessionsDir("")
	session.SetSkipFlockCheck(true)

	// Create a session with pending tools
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add messages
	_, err = session.AppendMessageWithToolInteraction(context.Background(), sess, session.Message{
		Role:      "user",
		Content:   "Echo hello",
		Timestamp: time.Now(),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = session.SaveAssistantResponseWithTools(context.Background(), sess, 
		"I'll echo that for you:",
		[]core.ToolCall{{
			ID:   "call-1",
			Name: "universal_command",
			Args: json.RawMessage(`{"command": ["echo", "hello from tool"]}`),
		}},
		"claude-3-opus-20240229",
	)
	if err != nil {
		t.Fatal(err)
	}

	sessionID := session.GetSessionID(sess.Path)

	// Resume with lmc binary
	args := []string{
		"-provider", "anthropic",  // Specify provider type
		"-provider-url", serverURL + "/messages",
		"-model", "claude-3-opus-20240229",  // Use a valid model name
		"-resume", sessionID,
		"-sessions-dir", sessionsDir,
		"-log-dir", logDir,
		"-tool-whitelist", whitelistPath,
		"-tool-auto-approve",
	}

	stdout, stderr, err := runLmcCommandWithSpecificLogDir(t, binPath, args, "continuing", logDir)

	// Log output for debugging
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr (first 500 chars): %.500s", stderr)

	// Check for tool execution message
	if !strings.Contains(stderr, "Executing 1 pending tool call(s) from previous session") {
		t.Error("Expected to see pending tool execution message")
	}

	// Check for tool output
	if !strings.Contains(stderr, ">>> Tool: universal_command") {
		t.Error("Expected to see tool execution header")
	}

	if !strings.Contains(stderr, "hello from tool") {
		t.Error("Expected to see tool output 'hello from tool'")
	}

	// Verify tool results were saved
	// First check the original session
	reloadedSess, err := session.LoadSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}

	// Debug: List all files in the session directory
	files, _ := os.ReadDir(reloadedSess.Path)
	t.Logf("Files in session directory %s:", sessionID)
	for _, f := range files {
		t.Logf("  %s", f.Name())
	}

	// Check if a sibling was created
	sessionParent := filepath.Dir(reloadedSess.Path)
	siblings, _ := os.ReadDir(sessionParent)
	t.Logf("Sibling directories in %s:", sessionParent)
	for _, s := range siblings {
		if s.IsDir() {
			t.Logf("  %s", s.Name())
			// Check files in sibling
			sibPath := filepath.Join(sessionParent, s.Name())
			sibFiles, _ := os.ReadDir(sibPath)
			t.Logf("    Files in %s:", s.Name())
			for _, f := range sibFiles {
				t.Logf("      %s", f.Name())
			}
		}
	}

	// The tool results should be in the original session (0001) since no fork occurs
	// Tool calls are in 0001.tools.json (message 0001), results should be in 0002.tools.json

	// Check for tool results in message 0002 of session 0001
	toolResultsPath := filepath.Join(reloadedSess.Path, "0002.tools.json")
	if _, err := os.Stat(toolResultsPath); os.IsNotExist(err) {
		t.Error("Tool results file (0002.tools.json) was not created in session 0001")
	} else {
		// Read and verify content
		data, _ := os.ReadFile(toolResultsPath)
		t.Logf("Tool results content: %s", string(data))
		
		var interaction core.ToolInteraction
		if err := json.Unmarshal(data, &interaction); err == nil {
			if len(interaction.Results) != 1 {
				t.Errorf("Expected 1 tool result, got %d", len(interaction.Results))
			} else if !strings.Contains(interaction.Results[0].Output, "hello from tool") {
				t.Errorf("Tool output doesn't contain expected text, got: %s", interaction.Results[0].Output)
			}
		}
	}
}
