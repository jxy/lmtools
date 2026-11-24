//go:build integration

package main

import (
	"lmtools/internal/mockserver"
	"os"
	"path/filepath"
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

			// The answer records the model that produced it.
			sess, _ := completionSession(t, sessionsDir)
			m, err := sess.Store.Message(sess.Ref())
			if err != nil {
				t.Fatalf("read the answer: %v", err)
			}
			if m.Role != "assistant" || m.Model != tt.expectedModel {
				t.Errorf("Expected an answer from model %q, got %s from %q", tt.expectedModel, m.Role, m.Model)
			}
		})
	}
}
