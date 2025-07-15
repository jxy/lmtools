package argo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShowDispatcher(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a test session with messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add some messages
		msg1 := Message{
			Role:      "user",
			Content:   "Hello, world!",
			Timestamp: time.Now(),
		}
		msgID1, err := AppendMessage(session, msg1)
		if err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}

		msg2 := Message{
			Role:      "assistant",
			Content:   "Hello! How can I help you?",
			Model:     "gpt4o",
			Timestamp: time.Now(),
		}
		_, err = AppendMessage(session, msg2)
		if err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}

		sessionID := filepath.Base(session.Path)

		tests := []struct {
			name        string
			showArg     string
			shouldError bool
			errorMsg    string
		}{
			{
				name:        "show session",
				showArg:     sessionID,
				shouldError: false,
			},
			{
				name:        "show message",
				showArg:     sessionID + "/" + msgID1,
				shouldError: false,
			},
			{
				name:        "show non-existent session",
				showArg:     "9999",
				shouldError: true,
				errorMsg:    "not found",
			},
			{
				name:        "show non-existent message",
				showArg:     sessionID + "/9999",
				shouldError: true,
				errorMsg:    "not found",
			},
			{
				name:        "empty argument",
				showArg:     "",
				shouldError: true,
				errorMsg:    "non-empty argument",
			},
			{
				name:        "path traversal attempt",
				showArg:     "../../../etc/passwd",
				shouldError: true,
				errorMsg:    "must be within sessions directory",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := ShowDispatcher(tt.showArg)
				if tt.shouldError {
					if err == nil {
						t.Errorf("Expected error but got none")
					} else if tt.errorMsg != "" && !strings.Contains(err.Error(), tt.errorMsg) {
						t.Errorf("Expected error containing %q, got %q", tt.errorMsg, err.Error())
					}
				} else {
					if err != nil {
						t.Errorf("Unexpected error: %v", err)
					}
				}
			})
		}
	})
}

func TestShowConversation(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a test session with messages
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add messages
		messages := []Message{
			{
				Role:      "user",
				Content:   "What is Go?",
				Timestamp: time.Now(),
			},
			{
				Role:      "assistant",
				Content:   "Go is a statically typed, compiled programming language.",
				Model:     "gpt4o",
				Timestamp: time.Now(),
			},
			{
				Role:      "user",
				Content:   "Tell me more",
				Timestamp: time.Now(),
			},
		}

		for _, msg := range messages {
			if _, err := AppendMessage(session, msg); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// Test showing the conversation
		err = ShowConversation(session.Path)
		if err != nil {
			t.Errorf("ShowConversation failed: %v", err)
		}
	})
}

func TestShowMessage(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a test session
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add a message
		msg := Message{
			Role:      "assistant",
			Content:   "This is a test message.",
			Model:     "gpt4o",
			Timestamp: time.Now(),
		}
		msgID, err := AppendMessage(session, msg)
		if err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}

		// Test showing the message
		messagePath := filepath.Join(session.Path, msgID)
		err = ShowMessage(messagePath)
		if err != nil {
			t.Errorf("ShowMessage failed: %v", err)
		}
	})
}

func TestShowWithBranches(t *testing.T) {
	withTestSessionDir(t, func(sessionsDir string) {
		// Create a session with branches
		session, err := CreateSession()
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Add initial messages
		msg1 := Message{
			Role:      "user",
			Content:   "Hello",
			Timestamp: time.Now(),
		}
		_, err = AppendMessage(session, msg1)
		if err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}

		msg2 := Message{
			Role:      "assistant",
			Content:   "Hi there!",
			Model:     "gpt4o",
			Timestamp: time.Now(),
		}
		msgID2, err := AppendMessage(session, msg2)
		if err != nil {
			t.Fatalf("Failed to append message: %v", err)
		}

		// Create a branch from the assistant message
		branchPath, err := CreateSibling(session.Path, msgID2)
		if err != nil {
			t.Fatalf("Failed to create branch: %v", err)
		}

		// Load the branch as a session
		branchSession := &Session{Path: branchPath}

		// Add a message to the branch
		msg3 := Message{
			Role:      "assistant",
			Content:   "Hello! How are you?",
			Model:     "gpt35",
			Timestamp: time.Now(),
		}
		_, err = AppendMessage(branchSession, msg3)
		if err != nil {
			t.Fatalf("Failed to append message to branch: %v", err)
		}

		// Test showing the branch conversation
		err = ShowConversation(branchPath)
		if err != nil {
			t.Errorf("ShowConversation for branch failed: %v", err)
		}

		// Extract branch ID for testing
		sessionID := filepath.Base(session.Path)
		branchID := filepath.Base(branchPath)

		// Test showing branch using full path
		fullBranchPath := sessionID + "/" + branchID
		err = ShowDispatcher(fullBranchPath)
		if err != nil {
			t.Errorf("ShowDispatcher for branch path failed: %v", err)
		}
	})
}
