package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestToolInteractionPersistence tests saving and loading tool interactions
func TestToolInteractionPersistence(t *testing.T) {
	sessionPath := t.TempDir()

	// Create test tool interaction
	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{
				ID:   "call_1",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["echo","hello"]}`),
			},
			{
				ID:   "call_2",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["ls","-la"]}`),
			},
		},
		Results: []core.ToolResult{
			{
				ID:     "call_1",
				Output: "hello\n",
				Error:  "",
			},
			{
				ID:        "call_2",
				Output:    "total 24\ndrwxr-xr-x 2 user user 4096 Jan 1 00:00 .\n",
				Error:     "",
				Truncated: true,
			},
		},
	}

	// Save tool interaction
	if err := SaveToolInteraction(sessionPath, "0001", toolInteraction); err != nil {
		t.Fatalf("SaveToolInteraction failed: %v", err)
	}

	// Verify file exists
	toolsPath := filepath.Join(sessionPath, "0001.tools.json")
	if !fileExists(toolsPath) {
		t.Fatal("Tools file should exist")
	}

	// Load it back
	loaded, err := LoadToolInteraction(sessionPath, "0001")
	if err != nil {
		t.Fatalf("LoadToolInteraction failed: %v", err)
	}

	// Verify content
	if len(loaded.Calls) != 2 {
		t.Errorf("Expected 2 calls, got %d", len(loaded.Calls))
	}
	if len(loaded.Results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(loaded.Results))
	}

	// Check specific values
	if loaded.Calls[0].ID != "call_1" {
		t.Errorf("Expected call ID 'call_1', got %s", loaded.Calls[0].ID)
	}
	if loaded.Results[1].Truncated != true {
		t.Error("Expected second result to be truncated")
	}
}

// TestToolInteractionWithMessageCommit tests atomic commit with tool interactions
func TestToolInteractionWithMessageCommit(t *testing.T) {
	sessionPath := t.TempDir()

	msg := Message{
		ID:        "0001",
		Role:      "assistant",
		Content:   "I'll run those commands for you.",
		Timestamp: time.Now(),
		Model:     "gpt-4",
	}

	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{
				ID:   "call_1",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["pwd"]}`),
			},
		},
		Results: []core.ToolResult{
			{
				ID:      "call_1",
				Output:  "/home/user\n",
				Elapsed: 15,
			},
		},
	}

	// Stage all files
	staging, err := stageMessageFiles(sessionPath, msg, toolInteraction)
	if err != nil {
		t.Fatalf("stageMessageFiles failed: %v", err)
	}
	defer staging.Close()

	// Commit atomically
	files := []filePair{
		{Tmp: staging.TxtPath, Final: filepath.Join(sessionPath, "0001.txt")},
		{Tmp: staging.ToolsPath, Final: filepath.Join(sessionPath, "0001.tools.json")},
		{Tmp: staging.JsonPath, Final: filepath.Join(sessionPath, "0001.json")},
	}

	if _, err := commitFiles(context.Background(), files); err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// Verify all files exist
	if !fileExists(filepath.Join(sessionPath, "0001.txt")) {
		t.Error("Text file should exist")
	}
	if !fileExists(filepath.Join(sessionPath, "0001.tools.json")) {
		t.Error("Tools file should exist")
	}
	if !fileExists(filepath.Join(sessionPath, "0001.json")) {
		t.Error("JSON file should exist")
	}

	// Load and verify
	loadedMsg, err := readMessage(sessionPath, "0001")
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}
	if loadedMsg.Model != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got %s", loadedMsg.Model)
	}

	loadedTools, err := LoadToolInteraction(sessionPath, "0001")
	if err != nil {
		t.Fatalf("LoadToolInteraction failed: %v", err)
	}
	if len(loadedTools.Calls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(loadedTools.Calls))
	}
}

// TestEmptyToolInteraction tests handling of empty tool interactions
func TestEmptyToolInteraction(t *testing.T) {
	sessionPath := t.TempDir()

	// Try to load non-existent tool interaction
	loaded, err := LoadToolInteraction(sessionPath, "0001")
	if err != nil {
		t.Errorf("Unexpected error loading non-existent tool interaction: %v", err)
	}
	if loaded != nil {
		t.Error("Expected nil result for non-existent file")
	}

	// Save empty tool interaction - should not create file
	empty := &core.ToolInteraction{
		Calls:   []core.ToolCall{},
		Results: []core.ToolResult{},
	}

	if err := SaveToolInteraction(sessionPath, "0002", empty); err != nil {
		t.Fatalf("SaveToolInteraction failed: %v", err)
	}

	// Verify file was not created (due to empty validation)
	toolPath := filepath.Join(sessionPath, "0002.tools.json")
	if _, err := os.Stat(toolPath); !os.IsNotExist(err) {
		t.Error("Expected no file to be created for empty tool interaction")
	}

	// Load should return nil for non-existent file
	loaded, err = LoadToolInteraction(sessionPath, "0002")
	if err != nil {
		t.Errorf("Unexpected error loading non-existent tool interaction: %v", err)
	}
	if loaded != nil {
		t.Error("Expected nil result for non-existent file")
	}
}

// TestToolInteractionRollback tests rollback behavior with tool files
func TestToolInteractionRollback(t *testing.T) {
	sessionPath := t.TempDir()

	// Create existing committed message with tools
	existingJson := filepath.Join(sessionPath, "0001.json")
	existingTools := filepath.Join(sessionPath, "0001.tools.json")

	if err := os.WriteFile(existingJson, []byte(`{"role":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingTools, []byte(`{"calls":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Try to overwrite (should fail and rollback)
	msg := Message{
		ID:      "0001",
		Role:    "assistant",
		Content: "New content",
	}

	staging, err := stageMessageFiles(sessionPath, msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()

	files := []filePair{
		{Tmp: staging.TxtPath, Final: filepath.Join(sessionPath, "0001.txt")},
		{Tmp: staging.JsonPath, Final: existingJson}, // This should fail
	}

	_, err = commitFiles(context.Background(), files)
	if err == nil {
		t.Error("Expected error when overwriting existing message")
	}

	// Verify no partial commit occurred
	if fileExists(filepath.Join(sessionPath, "0001.txt")) {
		t.Error("Text file should not exist after rollback")
	}

	// Original files should be unchanged
	content, _ := os.ReadFile(existingJson)
	if string(content) != `{"role":"user"}` {
		t.Error("Original json should be unchanged")
	}
}

// TestBuildMessagesWithToolInteractions tests building messages with tool data
func TestBuildMessagesWithToolInteractions(t *testing.T) {
	// Create a proper session structure
	baseDir := t.TempDir()

	// Set the sessions directory for this test
	oldDir := GetSessionsDir()
	SetSessionsDir(baseDir)
	defer SetSessionsDir(oldDir)

	sessionPath := filepath.Join(baseDir, "0001")
	t.Logf("Using sessionPath: %s", sessionPath)

	// Create the session directory
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a message with tools
	msg1 := Message{
		ID:        "0001",
		Role:      "user",
		Content:   "Please list files",
		Timestamp: time.Now(),
	}

	// Write message
	if err := writeMessage(sessionPath, "0001", msg1); err != nil {
		t.Fatal(err)
	}

	// Create assistant response with tools
	msg2 := Message{
		ID:        "0002",
		Role:      "assistant",
		Content:   "I'll list the files for you.",
		Timestamp: time.Now().Add(time.Second),
	}

	toolCalls := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{
				ID:   "call_1",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["ls"]}`),
			},
		},
	}

	// Write assistant message with tool calls
	if err := writeMessage(sessionPath, "0002", msg2); err != nil {
		t.Fatal(err)
	}
	if err := SaveToolInteraction(sessionPath, "0002", toolCalls); err != nil {
		t.Fatal(err)
	}

	// Create user message with tool results
	msg3 := Message{
		ID:        "0003",
		Role:      "user",
		Content:   "",
		Timestamp: time.Now().Add(2 * time.Second),
	}

	toolResults := &core.ToolInteraction{
		Results: []core.ToolResult{
			{
				ID:     "call_1",
				Output: "file1.txt\nfile2.txt\n",
			},
		},
	}

	// Write user message with tool results
	if err := writeMessage(sessionPath, "0003", msg3); err != nil {
		t.Fatal(err)
	}
	if err := SaveToolInteraction(sessionPath, "0003", toolResults); err != nil {
		t.Fatal(err)
	}

	// Load messages
	messages, err := loadMessagesInDir(sessionPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
	}

	// Build with tool interactions
	typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), sessionPath)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions failed: %v", err)
	}

	// Should have 3 typed messages: user, assistant with tool use, user with tool result
	if len(typedMessages) != 3 {
		t.Errorf("Expected 3 typed messages, got %d", len(typedMessages))
	}

	// Verify message types
	if typedMessages[0].Role != "user" {
		t.Errorf("First message should be user, got %s", typedMessages[0].Role)
	}
	if typedMessages[1].Role != "assistant" {
		t.Errorf("Second message should be assistant, got %s", typedMessages[1].Role)
	}

	// Check for tool use block in assistant message
	hasToolUse := false
	for _, block := range typedMessages[1].Blocks {
		if _, ok := block.(core.ToolUseBlock); ok {
			hasToolUse = true
			break
		}
	}
	if !hasToolUse {
		t.Error("Assistant message should contain tool use block")
	}

	// Third message should be user with tool result
	if typedMessages[2].Role != "user" {
		t.Errorf("Third message should be user (tool result), got %s", typedMessages[2].Role)
	}

	// Check for tool result block in third message
	hasToolResult := false
	for _, block := range typedMessages[2].Blocks {
		if _, ok := block.(core.ToolResultBlock); ok {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		t.Error("Third message should contain tool result block")
	}

	// Check tool result block
	if len(typedMessages[2].Blocks) != 1 {
		t.Errorf("Tool result message should have 1 block, got %d", len(typedMessages[2].Blocks))
	}
	if _, ok := typedMessages[2].Blocks[0].(core.ToolResultBlock); !ok {
		t.Error("Third message should contain tool result block")
	}
}

// TestOrphanedToolsFile tests that orphaned .tools.json files are handled correctly
func TestOrphanedToolsFile(t *testing.T) {
	sessionPath := t.TempDir()

	// Create orphaned tools file (no matching .json)
	orphanedTools := filepath.Join(sessionPath, "0001.tools.json")
	if err := os.WriteFile(orphanedTools, []byte(`{"calls":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// List messages should not include this
	messages, err := listMessages(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Error("Orphaned tools file should not create a message")
	}

	// Now create a proper message and overwrite the orphaned file
	msg := Message{
		ID:      "0001",
		Role:    "assistant",
		Content: "Test",
	}

	toolInteraction := &core.ToolInteraction{
		Calls: []core.ToolCall{
			{ID: "new_call", Name: "test"},
		},
	}

	staging, err := stageMessageFiles(sessionPath, msg, toolInteraction)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()

	files := []filePair{
		{Tmp: staging.TxtPath, Final: filepath.Join(sessionPath, "0001.txt")},
		{Tmp: staging.ToolsPath, Final: orphanedTools}, // Should overwrite
		{Tmp: staging.JsonPath, Final: filepath.Join(sessionPath, "0001.json")},
	}

	if _, err := commitFiles(context.Background(), files); err != nil {
		t.Fatalf("commitFiles failed: %v", err)
	}

	// Verify new content
	loaded, err := LoadToolInteraction(sessionPath, "0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Calls) != 1 || loaded.Calls[0].ID != "new_call" {
		t.Error("Orphaned file should have been overwritten")
	}
}
