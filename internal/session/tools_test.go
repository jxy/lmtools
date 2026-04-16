package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAssistantResponseWithTools(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Test data
		text := "I'll help you with that task."
		toolCalls := []core.ToolCall{
			{
				ID:   "call-123",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["echo","hello"]}`),
			},
		}
		model := "test-model"

		// Save assistant response with tools
		result, err := SaveAssistantResponseWithTools(context.Background(), sess, text, toolCalls, model)
		if err != nil {
			t.Fatalf("Failed to save assistant response: %v", err)
		}

		if result.Path != sess.Path {
			t.Errorf("Expected path %s, got %s", sess.Path, result.Path)
		}

		if result.MessageID == "" {
			t.Error("Expected non-empty message ID")
		}

		// Verify files were created
		files := []string{
			result.MessageID + ".json",       // metadata
			result.MessageID + ".txt",        // text content
			result.MessageID + ".tools.json", // tool calls
		}

		for _, file := range files {
			fullPath := filepath.Join(sess.Path, file)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				t.Errorf("Expected file %s to exist", file)
			}
		}

		// Load and verify tool interaction
		interaction, err := LoadToolInteraction(sess.Path, result.MessageID)
		if err != nil {
			t.Fatalf("Failed to load tool interaction: %v", err)
		}

		if interaction == nil {
			t.Fatal("Expected non-nil tool interaction")
		}

		if len(interaction.Calls) != 1 {
			t.Errorf("Expected 1 tool call, got %d", len(interaction.Calls))
		}

		if interaction.Calls[0].ID != "call-123" {
			t.Errorf("Expected tool call ID 'call-123', got %s", interaction.Calls[0].ID)
		}

		// Verify text content
		content, err := os.ReadFile(filepath.Join(sess.Path, result.MessageID+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != text {
			t.Errorf("Expected content '%s', got '%s'", text, string(content))
		}
	})
}

func TestSaveToolResults(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Test data
		results := []core.ToolResult{
			{
				ID:        "call-123",
				Output:    "hello\n",
				Elapsed:   125,
				Truncated: false,
			},
			{
				ID:      "call-456",
				Error:   "command not found",
				Elapsed: 10,
			},
		}
		additionalText := "Note: Some tools had issues"

		// Save tool results
		result, err := SaveToolResults(context.Background(), sess, results, additionalText)
		if err != nil {
			t.Fatalf("Failed to save tool results: %v", err)
		}

		if result.Path != sess.Path {
			t.Errorf("Expected path %s, got %s", sess.Path, result.Path)
		}

		// Verify files were created
		files := []string{
			result.MessageID + ".json",       // metadata
			result.MessageID + ".txt",        // additional text
			result.MessageID + ".tools.json", // tool results
		}

		for _, file := range files {
			fullPath := filepath.Join(sess.Path, file)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				t.Errorf("Expected file %s to exist", file)
			}
		}

		// Load and verify tool interaction
		interaction, err := LoadToolInteraction(sess.Path, result.MessageID)
		if err != nil {
			t.Fatalf("Failed to load tool interaction: %v", err)
		}

		if len(interaction.Results) != 2 {
			t.Errorf("Expected 2 tool results, got %d", len(interaction.Results))
		}

		// Verify first result
		if interaction.Results[0].ID != "call-123" {
			t.Errorf("Expected result ID 'call-123', got %s", interaction.Results[0].ID)
		}
		if interaction.Results[0].Output != "hello\n" {
			t.Errorf("Expected output 'hello\\n', got %s", interaction.Results[0].Output)
		}

		// Verify second result with error
		if interaction.Results[1].Error != "command not found" {
			t.Errorf("Expected error 'command not found', got %s", interaction.Results[1].Error)
		}

		// Verify additional text
		content, err := os.ReadFile(filepath.Join(sess.Path, result.MessageID+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != additionalText {
			t.Errorf("Expected content '%s', got '%s'", additionalText, string(content))
		}
	})
}

func TestSaveAssistantResponseWithThoughtSignature(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		result, err := SaveAssistantResponseWithMetadata(
			context.Background(),
			sess,
			"Hello from Gemini.",
			nil,
			"gemini-3.1-flash-lite-preview",
			"sig-text-123",
		)
		if err != nil {
			t.Fatalf("SaveAssistantResponseWithMetadata() error = %v", err)
		}

		metaPath := filepath.Join(sess.Path, result.MessageID+".json")
		metaData, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", metaPath, err)
		}

		var metadata MessageMetadata
		if err := json.Unmarshal(metaData, &metadata); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		if metadata.ThoughtSignature == nil {
			t.Fatal("metadata thought_signature = nil, want non-nil")
		}
		if got := *metadata.ThoughtSignature; got != "sig-text-123" {
			t.Fatalf("metadata thought_signature = %q, want %q", got, "sig-text-123")
		}

		typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), sess.Path)
		if err != nil {
			t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
		}
		if len(typedMessages) != 1 {
			t.Fatalf("len(typedMessages) = %d, want 1", len(typedMessages))
		}
		if len(typedMessages[0].Blocks) != 1 {
			t.Fatalf("len(blocks) = %d, want 1", len(typedMessages[0].Blocks))
		}

		textBlock, ok := typedMessages[0].Blocks[0].(core.TextBlock)
		if !ok {
			t.Fatalf("block type = %T, want core.TextBlock", typedMessages[0].Blocks[0])
		}
		if got := textBlock.ThoughtSignature; got != "sig-text-123" {
			t.Fatalf("TextBlock.ThoughtSignature = %q, want %q", got, "sig-text-123")
		}
	})
}

func TestBuildMessagesWithThoughtSignatureOnlyMessage(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		if _, err := SaveAssistantResponseWithMetadata(
			context.Background(),
			sess,
			"",
			nil,
			"gemini-3.1-flash-lite-preview",
			"sig-only-456",
		); err != nil {
			t.Fatalf("SaveAssistantResponseWithMetadata() error = %v", err)
		}

		typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), sess.Path)
		if err != nil {
			t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
		}
		if len(typedMessages) != 1 {
			t.Fatalf("len(typedMessages) = %d, want 1", len(typedMessages))
		}
		if len(typedMessages[0].Blocks) != 1 {
			t.Fatalf("len(blocks) = %d, want 1", len(typedMessages[0].Blocks))
		}

		textBlock, ok := typedMessages[0].Blocks[0].(core.TextBlock)
		if !ok {
			t.Fatalf("block type = %T, want core.TextBlock", typedMessages[0].Blocks[0])
		}
		if got := textBlock.Text; got != "" {
			t.Fatalf("TextBlock.Text = %q, want empty string", got)
		}
		if got := textBlock.ThoughtSignature; got != "sig-only-456" {
			t.Fatalf("TextBlock.ThoughtSignature = %q, want %q", got, "sig-only-456")
		}
	})
}

func TestSaveAssistantResponseTextOnly(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Save assistant response with text only (no tools)
		text := "Here's the answer to your question."
		result, err := SaveAssistantResponseWithTools(context.Background(), sess, text, nil, "test-model")
		if err != nil {
			t.Fatalf("Failed to save assistant response: %v", err)
		}

		// Verify only text and metadata files were created
		if _, err := os.Stat(filepath.Join(sess.Path, result.MessageID+".txt")); err != nil {
			t.Error("Expected text file to exist")
		}
		if _, err := os.Stat(filepath.Join(sess.Path, result.MessageID+".json")); err != nil {
			t.Error("Expected metadata file to exist")
		}

		// Tool file should NOT exist
		if _, err := os.Stat(filepath.Join(sess.Path, result.MessageID+".tools.json")); !os.IsNotExist(err) {
			t.Error("Expected tools file to NOT exist when no tools present")
		}

		// Verify no tool interaction
		interaction, err := LoadToolInteraction(sess.Path, result.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		if interaction != nil {
			t.Error("Expected nil interaction when no tools file")
		}
	})
}

func TestSaveAssistantResponseToolsOnly(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Save assistant response with tools only (no text)
		toolCalls := []core.ToolCall{
			{
				ID:   "call-789",
				Name: "test_tool",
				Args: json.RawMessage(`{}`),
			},
		}

		result, err := SaveAssistantResponseWithTools(context.Background(), sess, "", toolCalls, "test-model")
		if err != nil {
			t.Fatalf("Failed to save assistant response: %v", err)
		}

		// Text file should NOT exist when no text content
		if _, err := os.Stat(filepath.Join(sess.Path, result.MessageID+".txt")); !os.IsNotExist(err) {
			t.Error("Expected text file to NOT exist when no text content")
		}

		// Metadata and tools files should exist
		if _, err := os.Stat(filepath.Join(sess.Path, result.MessageID+".json")); err != nil {
			t.Error("Expected metadata file to exist")
		}
		if _, err := os.Stat(filepath.Join(sess.Path, result.MessageID+".tools.json")); err != nil {
			t.Error("Expected tools file to exist")
		}
	})
}

func TestLoadToolInteractionNonExistent(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		sessionPath := filepath.Join(sessionsDir, "test-session")
		if err := os.MkdirAll(sessionPath, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Try to load non-existent tool interaction
		interaction, err := LoadToolInteraction(sessionPath, "nonexistent")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if interaction != nil {
			t.Error("Expected nil interaction for non-existent file")
		}
	})
}

func TestLoadToolInteractionCorrupted(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		sessionPath := filepath.Join(sessionsDir, "test-session")
		if err := os.MkdirAll(sessionPath, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Create corrupted tools file
		toolsPath := filepath.Join(sessionPath, "0001.tools.json")
		if err := os.WriteFile(toolsPath, []byte("not valid json"), constants.FilePerm); err != nil {
			t.Fatal(err)
		}

		// Try to load corrupted tool interaction
		interaction, err := LoadToolInteraction(sessionPath, "0001")
		if err == nil {
			t.Error("Expected error for corrupted JSON")
		}

		if interaction != nil {
			t.Error("Expected nil interaction for corrupted file")
		}

		if !strings.Contains(err.Error(), "unmarshal") {
			t.Errorf("Expected unmarshal error, got: %v", err)
		}
	})
}

func TestSaveToolResultsTruncation(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Create a large output that was truncated
		largeOutput := strings.Repeat("x", 1024*1024) // 1MB

		results := []core.ToolResult{
			{
				ID:        "call-large",
				Output:    largeOutput,
				Elapsed:   500,
				Truncated: true,
			},
		}
		additionalText := "Note: Output for tool 'test' was truncated to 1MB"

		// Save tool results
		result, err := SaveToolResults(context.Background(), sess, results, additionalText)
		if err != nil {
			t.Fatalf("Failed to save tool results: %v", err)
		}

		// Load and verify
		interaction, err := LoadToolInteraction(sess.Path, result.MessageID)
		if err != nil {
			t.Fatal(err)
		}

		if !interaction.Results[0].Truncated {
			t.Error("Expected truncated flag to be preserved")
		}

		// Verify additional text mentions truncation
		content, err := os.ReadFile(filepath.Join(sess.Path, result.MessageID+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "truncated") {
			t.Error("Expected additional text to mention truncation")
		}
	})
}

func TestSaveWithNilSession(t *testing.T) {
	// Test nil session handling
	_, err := SaveAssistantResponseWithTools(context.Background(), nil, "test", nil, "model")
	if err == nil {
		t.Error("Expected error for nil session")
	}

	_, err = SaveToolResults(context.Background(), nil, nil, "")
	if err == nil {
		t.Error("Expected error for nil session")
	}
}

func TestComplexToolArgs(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		sess := &Session{
			Path: filepath.Join(sessionsDir, "test-session"),
		}
		if err := os.MkdirAll(sess.Path, constants.DirPerm); err != nil {
			t.Fatal(err)
		}

		// Complex nested arguments
		complexArgs := map[string]interface{}{
			"command": []string{"curl", "-X", "POST", "https://api.example.com"},
			"environ": map[string]string{
				"API_KEY": "secret",
				"DEBUG":   "true",
			},
			"workdir": "/tmp/work",
			"timeout": 30,
		}

		argsJSON, _ := json.Marshal(complexArgs)

		toolCalls := []core.ToolCall{
			{
				ID:   "complex-call",
				Name: "universal_command",
				Args: argsJSON,
			},
		}

		// Save and reload
		result, err := SaveAssistantResponseWithTools(context.Background(), sess, "", toolCalls, "model")
		if err != nil {
			t.Fatal(err)
		}

		interaction, err := LoadToolInteraction(sess.Path, result.MessageID)
		if err != nil {
			t.Fatal(err)
		}

		// Verify complex args are preserved
		var loadedArgs map[string]interface{}
		if err := json.Unmarshal(interaction.Calls[0].Args, &loadedArgs); err != nil {
			t.Fatal(err)
		}

		// Check command array
		if cmd, ok := loadedArgs["command"].([]interface{}); ok {
			if len(cmd) != 4 {
				t.Errorf("Expected 4 command parts, got %d", len(cmd))
			}
		} else {
			t.Error("Command not found in loaded args")
		}

		// Check environment map
		if env, ok := loadedArgs["environ"].(map[string]interface{}); ok {
			if env["API_KEY"] != "secret" {
				t.Error("API_KEY not preserved")
			}
		} else {
			t.Error("Environment not found in loaded args")
		}
	})
}
