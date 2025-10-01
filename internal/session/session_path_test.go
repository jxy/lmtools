package session

import (
	"context"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSessionPath(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		tests := []struct {
			name          string
			path          string
			expectedRoot  string
			expectedComps []string
		}{
			{
				name:          "Simple session ID",
				path:          "001a",
				expectedRoot:  filepath.Join(sessionsDir, "001a"),
				expectedComps: []string{},
			},
			{
				name:          "Session with sibling",
				path:          "001a/0002.s.0000",
				expectedRoot:  filepath.Join(sessionsDir, "001a"),
				expectedComps: []string{"0002.s.0000"},
			},
			{
				name:          "Nested siblings",
				path:          "001a/0002.s.0000/0001.s.0000",
				expectedRoot:  filepath.Join(sessionsDir, "001a"),
				expectedComps: []string{"0002.s.0000", "0001.s.0000"},
			},
			{
				name:          "Absolute path",
				path:          filepath.Join(sessionsDir, "001a", "0002.s.0000"),
				expectedRoot:  filepath.Join(sessionsDir, "001a"),
				expectedComps: []string{"0002.s.0000"},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				root, comps := ParseSessionPath(tt.path)

				if root != tt.expectedRoot {
					t.Errorf("Expected root %s, got %s", tt.expectedRoot, root)
				}

				if len(comps) != len(tt.expectedComps) {
					t.Errorf("Expected %d components, got %d", len(tt.expectedComps), len(comps))
					return
				}

				for i, comp := range comps {
					if comp != tt.expectedComps[i] {
						t.Errorf("Component %d: expected %s, got %s", i, tt.expectedComps[i], comp)
					}
				}
			})
		}
	})
}

func TestIsSiblingDir(t *testing.T) {
	tests := []struct {
		name          string
		dir           string
		expectedIs    bool
		expectedMsgID string
		expectedNum   string
	}{
		{
			name:          "Valid sibling",
			dir:           "0002.s.0000",
			expectedIs:    true,
			expectedMsgID: "0002",
			expectedNum:   "0000",
		},
		{
			name:          "Another valid sibling",
			dir:           "00ff.s.0001",
			expectedIs:    true,
			expectedMsgID: "00ff",
			expectedNum:   "0001",
		},
		{
			name:          "Not a sibling - regular message",
			dir:           "0003",
			expectedIs:    false,
			expectedMsgID: "",
			expectedNum:   "",
		},
		{
			name:          "Not a sibling - wrong format",
			dir:           "0003.sibling.0000",
			expectedIs:    false,
			expectedMsgID: "",
			expectedNum:   "",
		},
		{
			name:          "Not a sibling - invalid hex",
			dir:           "0003.s.gggg",
			expectedIs:    false,
			expectedMsgID: "",
			expectedNum:   "",
		},
		{
			name:          "Not a sibling - too many parts",
			dir:           "0003.s.0000.extra",
			expectedIs:    false,
			expectedMsgID: "",
			expectedNum:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, msgID, num := IsSiblingDir(tt.dir)

			if is != tt.expectedIs {
				t.Errorf("Expected IsSiblingDir=%v, got %v", tt.expectedIs, is)
			}

			if msgID != tt.expectedMsgID {
				t.Errorf("Expected msgID=%s, got %s", tt.expectedMsgID, msgID)
			}

			if num != tt.expectedNum {
				t.Errorf("Expected num=%s, got %s", tt.expectedNum, num)
			}
		})
	}
}

func TestGetNextMessageID(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session directory
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Test empty directory
		msgID, err := GetNextMessageID(testSession)
		if err != nil {
			t.Fatalf("Failed to get next message ID: %v", err)
		}
		if msgID != "0000" {
			t.Errorf("Expected first message ID to be 0000, got %s", msgID)
		}

		// Create some messages
		messages := []Message{
			{ID: "0000", Role: "user", Content: "Test", Timestamp: time.Now()},
			{ID: "0001", Role: "assistant", Content: "Test", Timestamp: time.Now()},
			{ID: "0002", Role: "user", Content: "Test", Timestamp: time.Now()},
		}

		for _, msg := range messages {
			if err := writeMessage(testSession, msg.ID, msg); err != nil {
				t.Fatalf("Failed to write message: %v", err)
			}
		}

		// Test with existing messages
		msgID, err = GetNextMessageID(testSession)
		if err != nil {
			t.Fatalf("Failed to get next message ID: %v", err)
		}
		if msgID != "0003" {
			t.Errorf("Expected next message ID to be 0003, got %s", msgID)
		}

		// Test hex overflow (create message ffff)
		overflowMsg := Message{ID: "ffff", Role: "user", Content: "Test", Timestamp: time.Now()}
		if err := writeMessage(testSession, overflowMsg.ID, overflowMsg); err != nil {
			t.Fatalf("Failed to write overflow message: %v", err)
		}

		msgID, err = GetNextMessageID(testSession)
		if err != nil {
			t.Fatalf("Failed to get next message ID after overflow: %v", err)
		}
		if msgID != "10000" {
			t.Errorf("Expected next message ID after ffff to be 10000, got %s", msgID)
		}
	})
}

func TestGetNextSiblingPath(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session directory
		testSession := filepath.Join(sessionsDir, "test")
		if err := os.MkdirAll(testSession, constants.DirPerm); err != nil {
			t.Fatalf("Failed to create test session dir: %v", err)
		}

		// Test with no existing siblings
		sibPath, err := GetNextSiblingPath(testSession, "0002")
		if err != nil {
			t.Fatalf("Failed to get next sibling path: %v", err)
		}
		if sibPath != "0002.s.0000" {
			t.Errorf("Expected first sibling path to be 0002.s.0000, got %s", sibPath)
		}

		// Create some siblings
		siblings := []string{"0002.s.0000", "0002.s.0001", "0002.s.0002"}
		for _, sib := range siblings {
			sibDir := filepath.Join(testSession, sib)
			if err := os.MkdirAll(sibDir, constants.DirPerm); err != nil {
				t.Fatalf("Failed to create sibling dir: %v", err)
			}
		}

		// Test with existing siblings
		sibPath, err = GetNextSiblingPath(testSession, "0002")
		if err != nil {
			t.Fatalf("Failed to get next sibling path: %v", err)
		}
		if sibPath != "0002.s.0003" {
			t.Errorf("Expected next sibling path to be 0002.s.0003, got %s", sibPath)
		}

		// Test with different message ID
		sibPath, err = GetNextSiblingPath(testSession, "0005")
		if err != nil {
			t.Fatalf("Failed to get next sibling path: %v", err)
		}
		if sibPath != "0005.s.0000" {
			t.Errorf("Expected first sibling path for 0005 to be 0005.s.0000, got %s", sibPath)
		}
	})
}

func TestParseMessageID(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		tests := []struct {
			name              string
			messageIDPath     string
			expectedSession   string
			expectedMessageID string
		}{
			{
				name:              "Simple message ID",
				messageIDPath:     "001a/0002",
				expectedSession:   filepath.Join(sessionsDir, "001a"),
				expectedMessageID: "0002",
			},
			{
				name:              "Message in sibling",
				messageIDPath:     "001a/0002.s.0000/0001",
				expectedSession:   filepath.Join(sessionsDir, "001a", "0002.s.0000"),
				expectedMessageID: "0001",
			},
			{
				name:              "Just session ID",
				messageIDPath:     "001a",
				expectedSession:   filepath.Join(sessionsDir, "001a"),
				expectedMessageID: "",
			},
			{
				name:              "Absolute path",
				messageIDPath:     filepath.Join(sessionsDir, "001a", "0002"),
				expectedSession:   filepath.Join(sessionsDir, "001a"),
				expectedMessageID: "0002",
			},
			{
				name:              "Deep nesting",
				messageIDPath:     "001a/0002.s.0000/0001.s.0001/0003",
				expectedSession:   filepath.Join(sessionsDir, "001a", "0002.s.0000", "0001.s.0001"),
				expectedMessageID: "0003",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				sessionPath, messageID := ParseMessageID(tt.messageIDPath)

				if sessionPath != tt.expectedSession {
					t.Errorf("Expected session path %s, got %s", tt.expectedSession, sessionPath)
				}

				if messageID != tt.expectedMessageID {
					t.Errorf("Expected message ID %s, got %s", tt.expectedMessageID, messageID)
				}
			})
		}
	})
}

func TestIsSessionRoot(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a test session
		session, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Test session root
		if !IsSessionRoot(session.Path) {
			t.Errorf("Expected %s to be a session root", session.Path)
		}

		// Create a sibling
		siblingPath, err := CreateSibling(context.Background(), session.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create sibling: %v", err)
		}

		// Test sibling is not root
		if IsSessionRoot(siblingPath) {
			t.Errorf("Expected %s to NOT be a session root", siblingPath)
		}

		// Test sessions directory itself is not a root
		if IsSessionRoot(sessionsDir) {
			t.Errorf("Expected sessions directory to NOT be a session root")
		}
	})
}

func TestGetAnchorForBranching(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		tests := []struct {
			name         string
			sessionPath  string
			messageID    string
			expectedPath string
			expectedID   string
		}{
			{
				name:         "Root level message",
				sessionPath:  filepath.Join(sessionsDir, "abc123"),
				messageID:    "0001",
				expectedPath: filepath.Join(sessionsDir, "abc123"),
				expectedID:   "0001",
			},
			{
				name:         "Message in sibling directory",
				sessionPath:  filepath.Join(sessionsDir, "abc123", "0001.s.0000"),
				messageID:    "0000",
				expectedPath: filepath.Join(sessionsDir, "abc123"),
				expectedID:   "0001",
			},
			{
				name:         "Message in nested sibling",
				sessionPath:  filepath.Join(sessionsDir, "abc123", "0001.s.0002", "0003.s.0001"),
				messageID:    "0000",
				expectedPath: filepath.Join(sessionsDir, "abc123"), // Now bubbles all the way up
				expectedID:   "0001",                               // Original message from the topmost sibling
			},
			{
				name:         "Deep nesting",
				sessionPath:  filepath.Join(sessionsDir, "abc123", "0001.s.0000", "0002.s.0000", "0003.s.0000"),
				messageID:    "0004",
				expectedPath: filepath.Join(sessionsDir, "abc123"), // Bubbles all the way up
				expectedID:   "0001",                               // Original message from the topmost sibling
			},
			{
				name:         "Non-sibling nested path - bubble up",
				sessionPath:  filepath.Join(sessionsDir, "abc123", "0001.s.0000"),
				messageID:    "0002",
				expectedPath: filepath.Join(sessionsDir, "abc123"),
				expectedID:   "0001",
			},
			{
				name:         "Mixed sibling and non-sibling path",
				sessionPath:  filepath.Join(sessionsDir, "abc123", "notes", "0001.s.0000", "0002.s.0001"),
				messageID:    "0003",
				expectedPath: filepath.Join(sessionsDir, "abc123", "notes"), // Stops at non-sibling 'notes'
				expectedID:   "0001",                                        // From the first sibling encountered
			},
			{
				name:         "Empty path components",
				sessionPath:  filepath.Join(sessionsDir, "abc123"),
				messageID:    "0001",
				expectedPath: filepath.Join(sessionsDir, "abc123"),
				expectedID:   "0001",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				gotPath, gotID := GetAnchorForBranching(tt.sessionPath, tt.messageID)
				if gotPath != tt.expectedPath {
					t.Errorf("Path mismatch: got %s, want %s", gotPath, tt.expectedPath)
				}
				if gotID != tt.expectedID {
					t.Errorf("ID mismatch: got %s, want %s", gotID, tt.expectedID)
				}
			})
		}
	})
}

func TestValidationHelpers(t *testing.T) {
	t.Run("isValidMessageID", func(t *testing.T) {
		tests := []struct {
			id       string
			expected bool
		}{
			{"0000", true},
			{"0001", true},
			{"ffff", true},
			{"dead", true},
			{"beef", true},
			{"00000", true},    // 5 chars is valid
			{"10000", true},    // 5 chars for IDs > 0xffff
			{"ffffff", true},   // 6 chars is valid
			{"fffffff", true},  // 7 chars is valid
			{"ffffffff", true}, // 8 chars is valid
			{"FFFF", false},    // uppercase not allowed
			{"000", false},     // too short (< 4)
			{"000g", false},    // invalid hex char
			{"00 1", false},    // space
			{"00.1", false},    // dot
			{"", false},        // empty
		}

		for _, tt := range tests {
			t.Run(tt.id, func(t *testing.T) {
				if got := IsValidMessageID(tt.id); got != tt.expected {
					t.Errorf("isValidMessageID(%q) = %v, want %v", tt.id, got, tt.expected)
				}
			})
		}
	})

	t.Run("isValidSessionID", func(t *testing.T) {
		tests := []struct {
			id       string
			expected bool
		}{
			{"abc123", true},
			{"deadbeef", true},
			{"a", true},                // single char is valid
			{"1234567890abcdef", true}, // long hex is valid
			{"ABC123", false},          // uppercase not allowed
			{"abc-123", false},         // dash not allowed
			{"abc 123", false},         // space not allowed
			{"abc.123", false},         // dot not allowed
			{"", false},                // empty not allowed
		}

		for _, tt := range tests {
			t.Run(tt.id, func(t *testing.T) {
				if got := isValidSessionID(tt.id); got != tt.expected {
					t.Errorf("isValidSessionID(%q) = %v, want %v", tt.id, got, tt.expected)
				}
			})
		}
	})
}
