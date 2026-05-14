//go:build integration

package main

import (
	"encoding/json"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelRecordingInSessions(t *testing.T) {
	// Build the binary first
	binPath := getLmcBinary(t)

	// Create temporary sessions directory
	tempDir := t.TempDir()
	sessionsDir := filepath.Join(tempDir, "sessions")

	// Start mock server
	ms := mockserver.NewMockServer(
		mockserver.WithDefaultModel("gemini25pro"),
		mockserver.WithDefaultResponse("Test response"),
	)
	defer ms.Close()

	tests := []struct {
		name          string
		modelFlag     string
		expectedModel string
		embedMode     bool
	}{
		{
			name:          "chat without model flag uses default",
			modelFlag:     "",
			expectedModel: "gpt5", // Default for argo provider
			embedMode:     false,
		},
		{
			name:          "chat with explicit model",
			modelFlag:     "gpt4o",
			expectedModel: "gpt4o",
			embedMode:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up sessions directory
			os.RemoveAll(sessionsDir)

			// Build command arguments
			args := []string{
				"-argo-user", "testuser",
				"-provider-url", ms.URL(),
				"-sessions-dir", sessionsDir,
			}

			if tt.modelFlag != "" {
				args = append(args, "-model", tt.modelFlag)
			}

			if tt.embedMode {
				args = append(args, "-embed")
			}

			// Run the command using test helper to isolate logs
			_, stderr, err := runLmcCommand(t, binPath, args, "test message")
			if err != nil {
				t.Fatalf("Failed to run command: %v\nStderr: %s", err, stderr)
			}

			// For embed mode, we don't save responses to sessions
			if tt.embedMode {
				return
			}

			// Find the created session
			sessionPath, err := findLatestSession(sessionsDir)
			if err != nil {
				t.Fatalf("Failed to find session: %v", err)
			}

			// Load the session (to verify it's valid)
			_, err = session.LoadSession(sessionPath)
			if err != nil {
				t.Fatalf("Failed to load session: %v", err)
			}

			// Get messages by reading files directly
			msgFiles, err := filepath.Glob(filepath.Join(sessionPath, "*.json"))
			if err != nil {
				t.Fatalf("Failed to glob message files: %v", err)
			}

			if len(msgFiles) < 2 {
				t.Fatalf("Expected at least 2 message files, got %d", len(msgFiles))
			}

			// Find the assistant message file
			var assistantMsgPath string
			for _, msgFile := range msgFiles {
				name := filepath.Base(msgFile)
				if strings.HasSuffix(name, ".tools.json") || strings.HasSuffix(name, ".blocks.json") {
					continue
				}

				data, err := os.ReadFile(msgFile)
				if err != nil {
					continue
				}

				// Parse JSON to check role
				var msg map[string]interface{}
				if err := json.Unmarshal(data, &msg); err == nil {
					if role, ok := msg["role"].(string); ok && role == "assistant" {
						assistantMsgPath = msgFile
						break
					}
				}
			}

			if assistantMsgPath == "" {
				t.Fatal("No assistant message file found")
			}

			// Read and verify the assistant message
			data, err := os.ReadFile(assistantMsgPath)
			if err != nil {
				t.Fatalf("Failed to read assistant message: %v", err)
			}

			var rawMsg map[string]interface{}
			if err := json.Unmarshal(data, &rawMsg); err != nil {
				t.Fatalf("Failed to unmarshal message: %v", err)
			}

			// Verify the model is recorded correctly
			if model, ok := rawMsg["model"].(string); !ok || model != tt.expectedModel {
				t.Errorf("Expected model %q, got %v", tt.expectedModel, rawMsg["model"])
			}
		})
	}
}

// findLatestSession finds the most recently created session
func findLatestSession(sessionsDir string) (string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		if entry.IsDir() && len(entry.Name()) == 4 {
			return filepath.Join(sessionsDir, entry.Name()), nil
		}
	}

	return "", os.ErrNotExist
}
