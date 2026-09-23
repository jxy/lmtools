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
	// ErrHeadMoved reports that another writer changed a session after the
	// head a write expected was captured.
	ErrHeadMoved = stdErrors.New("session head moved")
	// ErrHeadReplaced reports that the message a pinned head names was
	// deleted, or replaced by another under the same path and ID.
	ErrHeadReplaced = stdErrors.New("session head replaced")
)

// Session represents a conversation session
type Session struct {
	Path        string // Directory path (also serves as session ID)
	SessionsDir string // Base directory for sessions (optional, defaults to GetSessionsDir())
	// Head is the last message of the lineage this value was prepared
	// against, or nil when nothing pinned one. A pinned head makes every
	// write check that the lineage still ends there, and advance it to the
	// message it wrote. An empty ref pins a lineage with no messages.
	Head *MessageRef
	// ConflictForks lists the forks writes through this value made because
	// another writer had moved the head, oldest first.
	ConflictForks []ConflictFork
}

// ConflictFork records that a write moved to a new session, forked through
// the expected head, because another writer had changed the session.
type ConflictFork struct {
	From string // session ID the head moved in
	To   string // session ID of the fork
}

// HeadMovedError is the distinct error a write returns when the session no
// longer ends at the head the write expected.
type HeadMovedError struct {
	SessionPath string
	Expected    MessageRef
	// Found is the last message ID in the session's directory, empty when
	// it holds none.
	Found string
}

func (e *HeadMovedError) Error() string {
	expected := e.Expected.ID
	if expected == "" {
		expected = "no message"
	}
	found := e.Found
	if found == "" {
		found = "no message"
	}
	return "session " + GetSessionID(e.SessionPath) + " changed: expected it to end at " + expected + ", found " + found
}

// Is makes errors.Is(err, ErrHeadMoved) match every HeadMovedError.
func (e *HeadMovedError) Is(target error) bool {
	return target == ErrHeadMoved
}

// HeadReplacedError is the error a write, a request build, or a fork through
// a pinned head returns when the head's message is gone or is another message
// now. The history the head stood for no longer exists, so nothing continues
// it, not even a fork, which would copy the replacement's history.
type HeadReplacedError struct {
	Expected MessageRef
}

func (e *HeadReplacedError) Error() string {
	return "session " + GetSessionID(e.Expected.Path) + " no longer holds message " + e.Expected.ID + " as this turn read it: it was deleted or replaced"
}

// Is makes errors.Is(err, ErrHeadReplaced) match every HeadReplacedError.
func (e *HeadReplacedError) Is(target error) bool {
	return target == ErrHeadReplaced
}

// GetPath implements core.Session.
func (s *Session) GetPath() string {
	return s.Path
}

// Message represents a single message in a conversation
type Message struct {
	ID               string    // Message hex ID (e.g., "0002")
	Role             core.Role // "user" or "assistant"
	Content          string    // Message text
	ThoughtSignature string
	Timestamp        time.Time
	Model            string // Model name (empty for user messages)
	// Revision is the revision the message was committed with, read from
	// its metadata; empty for a message committed before revisions existed.
	// Writers ignore it: each commit gives its message a new one.
	Revision string
}

// SaveResult represents the result of a session save operation
type SaveResult struct {
	Path      string // Final path of saved message
	MessageID string // Unique message identifier
	Revision  string // Revision the commit gave the message
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

// CreateSession creates a new session with a sequential ID
// If systemPrompt is not empty, it will be saved as message 0000
func CreateSession(systemPrompt string, log core.Logger) (*Session, error) {
	return DefaultManager().CreateSession(systemPrompt, log)
}

// LoadSession loads an existing session by path
func LoadSession(sessionPath string) (*Session, error) {
	return DefaultManager().LoadSession(sessionPath)
}
