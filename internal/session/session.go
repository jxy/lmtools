package session

import (
	stdErrors "errors"
	"lmtools/internal/core"
	"time"
)

var (
	// ErrMaxRetriesExceeded is returned when AppendMessageWithToolInteraction fails after maximum retry attempts
	ErrMaxRetriesExceeded = stdErrors.New("exceeded maximum retry attempts")
	// ErrSiblingOverflow is returned when too many sibling branches exist
	ErrSiblingOverflow = stdErrors.New("too many sibling branches")
)

// Session represents a conversation session
type Session struct {
	Path        string // Directory path (also serves as session ID)
	SessionsDir string // Base directory for sessions (optional, defaults to GetSessionsDir())
}

// GetPath implements core.Session.
func (s *Session) GetPath() string {
	return s.Path
}

// Message represents a single message in a conversation
type Message struct {
	ID        string    // Message hex ID (e.g., "0002")
	Role      core.Role // "user" or "assistant"
	Content   string    // Message text
	Timestamp time.Time
	Model     string // Model name (empty for user messages)
}

// SaveResult represents the result of a session save operation
type SaveResult struct {
	Path      string // Final path of saved message
	MessageID string // Unique message identifier
}

// SetSessionsDir sets a custom sessions directory
func SetSessionsDir(dir string) {
	DefaultManager().SetSessionsDir(dir)
}

// SetSkipFlockCheck sets whether to skip the file locking check
func SetSkipFlockCheck(skip bool) {
	DefaultManager().SetSkipFlockCheck(skip)
}

// GetSessionsDir returns the base directory for all sessions
func GetSessionsDir() string {
	return DefaultManager().SessionsDir()
}

// TestFlockSupport checks if the filesystem supports flock.
func TestFlockSupport() error {
	return DefaultManager().TestFlockSupport()
}

// CreateSession creates a new session with a sequential ID
// If systemPrompt is not empty, it will be saved as message 0000
func CreateSession(systemPrompt string, log core.Logger) (*Session, error) {
	return DefaultManager().CreateSession(systemPrompt, log)
}

// LoadSession loads an existing session by path
func LoadSession(sessionPath string) (*Session, error) {
	return DefaultManager().LoadSession(sessionPath)
}
