package session

import (
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateSessionWithSystemPrompt(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	originalDir := GetSessionsDir()
	defer SetSessionsDir(originalDir)
	SetSessionsDir(tempDir)

	testCases := []struct {
		name         string
		systemPrompt string
		expectFile   bool
	}{
		{
			name:         "with system prompt",
			systemPrompt: "You are a helpful assistant that specializes in Go programming.",
			expectFile:   true,
		},
		{
			name:         "without system prompt",
			systemPrompt: "",
			expectFile:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create session
			session, err := CreateSession(tc.systemPrompt, core.NewTestLogger(false))
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}

			// Check if system message files exist
			txtPath := filepath.Join(session.Path, "0000.txt")
			jsonPath := filepath.Join(session.Path, "0000.json")

			if tc.expectFile {
				// Verify text file exists and contains correct content
				content, err := os.ReadFile(txtPath)
				if err != nil {
					t.Errorf("Expected system message text file, but got error: %v", err)
				} else if string(content) != tc.systemPrompt {
					t.Errorf("System message content mismatch. Got %q, want %q", string(content), tc.systemPrompt)
				}

				// Verify JSON metadata exists and is valid
				metaBytes, err := os.ReadFile(jsonPath)
				if err != nil {
					t.Errorf("Expected system message metadata file, but got error: %v", err)
				} else {
					var metadata MessageMetadata
					if err := json.Unmarshal(metaBytes, &metadata); err != nil {
						t.Errorf("Failed to parse metadata: %v", err)
					} else if metadata.Role != "system" {
						t.Errorf("Expected role 'system', got %q", metadata.Role)
					}
				}
			} else {
				// Verify files don't exist when no system prompt
				if _, err := os.Stat(txtPath); !os.IsNotExist(err) {
					t.Errorf("Unexpected system message text file exists")
				}
				if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
					t.Errorf("Unexpected system message metadata file exists")
				}
			}
		})
	}
}

func TestSystemMessageInLineage(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	originalDir := GetSessionsDir()
	defer SetSessionsDir(originalDir)
	SetSessionsDir(tempDir)

	systemPrompt := "You are an expert in testing."

	// Create session with system prompt
	session, err := CreateSession(systemPrompt, core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a user message
	userMsg := Message{
		Role:    "user",
		Content: "Hello, world!",
	}
	if err := writeMessage(session.Path, "0001", userMsg); err != nil {
		t.Fatalf("Failed to write user message: %v", err)
	}

	// Add an assistant message
	assistantMsg := Message{
		Role:    "assistant",
		Content: "Hello! How can I help you today?",
	}
	if err := writeMessage(session.Path, "0002", assistantMsg); err != nil {
		t.Fatalf("Failed to write assistant message: %v", err)
	}

	// Get lineage
	lineage, err := GetLineage(session.Path)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	// Verify lineage includes system message as first message
	if len(lineage) != 3 {
		t.Fatalf("Expected 3 messages in lineage, got %d", len(lineage))
	}

	// Check first message is the system message
	if lineage[0].Role != "system" {
		t.Errorf("Expected first message to be system, got %q", lineage[0].Role)
	}
	if lineage[0].Content != systemPrompt {
		t.Errorf("System message content mismatch. Got %q, want %q", lineage[0].Content, systemPrompt)
	}

	// Verify order of remaining messages
	if lineage[1].Role != "user" || lineage[1].Content != userMsg.Content {
		t.Errorf("Second message should be user message")
	}
	if lineage[2].Role != "assistant" || lineage[2].Content != assistantMsg.Content {
		t.Errorf("Third message should be assistant message")
	}
}

func TestLoadMessagesWithSystemPrompt(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	sessionPath := filepath.Join(tempDir, "test-session")
	if err := os.MkdirAll(sessionPath, constants.DirPerm); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Manually create system message files
	systemContent := "You are a code review assistant."
	if err := os.WriteFile(filepath.Join(sessionPath, "0000.txt"), []byte(systemContent), constants.FilePerm); err != nil {
		t.Fatalf("Failed to write system message content: %v", err)
	}

	metadata := MessageMetadata{
		Role:      "system",
		Timestamp: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	metaBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(sessionPath, "0000.json"), metaBytes, constants.FilePerm); err != nil {
		t.Fatalf("Failed to write system message metadata: %v", err)
	}

	// Load messages
	messages, err := loadMessagesInDir(sessionPath)
	if err != nil {
		t.Fatalf("Failed to load messages: %v", err)
	}

	// Verify system message is loaded
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("Expected role 'system', got %q", messages[0].Role)
	}
	if messages[0].Content != systemContent {
		t.Errorf("Content mismatch. Got %q, want %q", messages[0].Content, systemContent)
	}
}

func TestListMessagesIncludesSystemMessage(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	sessionPath := filepath.Join(tempDir, "test-session")
	if err := os.MkdirAll(sessionPath, constants.DirPerm); err != nil {
		t.Fatalf("Failed to create session directory: %v", err)
	}

	// Create system message files
	if err := os.WriteFile(filepath.Join(sessionPath, "0000.txt"), []byte("system prompt"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to write system message content: %v", err)
	}
	metadata := MessageMetadata{Role: "system"}
	metaBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(sessionPath, "0000.json"), metaBytes, constants.FilePerm); err != nil {
		t.Fatalf("Failed to write system message metadata: %v", err)
	}

	// Create regular message files
	if err := os.WriteFile(filepath.Join(sessionPath, "0001.txt"), []byte("user message"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to write user message content: %v", err)
	}
	userMeta := MessageMetadata{Role: "user"}
	userMetaBytes, _ := json.MarshalIndent(userMeta, "", "  ")
	if err := os.WriteFile(filepath.Join(sessionPath, "0001.json"), userMetaBytes, constants.FilePerm); err != nil {
		t.Fatalf("Failed to write user message metadata: %v", err)
	}

	// List messages
	msgIDs, err := listMessages(sessionPath)
	if err != nil {
		t.Fatalf("Failed to list messages: %v", err)
	}

	// Verify both messages are listed and in correct order
	if len(msgIDs) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(msgIDs))
	}

	if msgIDs[0] != "0000" {
		t.Errorf("Expected first message ID to be '0000', got %q", msgIDs[0])
	}
	if msgIDs[1] != "0001" {
		t.Errorf("Expected second message ID to be '0001', got %q", msgIDs[1])
	}
}
