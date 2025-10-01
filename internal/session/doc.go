// Package session manages conversation persistence and tree-structured conversation history.
//
// # Overview
//
// The session package provides a file-based storage system for managing conversations with
// language models. It implements a tree structure that supports branching conversations,
// atomic message writes, and safe concurrent access through file locking.
//
// # Session Structure
//
// Sessions are organized as a tree of messages stored in the filesystem:
//
//	~/.lmc/sessions/
//	├── 0001/                    # Session ID
//	│   ├── 0001.txt            # Message content
//	│   ├── 0001.json           # Message metadata
//	│   ├── 0002.txt
//	│   ├── 0002.json
//	│   ├── 0002.tools.json     # Optional tool interactions
//	│   ├── 0003.txt
//	│   ├── 0003.json
//	│   └── 0003.s.0001/        # Sibling branch
//	│       ├── 0004.txt
//	│       └── 0004.json
//
// # Key Concepts
//
// Message Storage:
//
// Each message consists of up to three files:
//   - .txt: The message content (optional for tool-only messages)
//   - .json: Message metadata (role, timestamp, model) - REQUIRED
//   - .tools.json: Tool calls or results (optional)
//
// CRITICAL INVARIANT: A message exists if and only if its .json file exists.
// This enables atomic message creation through careful file ordering.
//
// Branching:
//
// The session tree supports branching through "sibling" directories. When a conflict
// occurs (e.g., resuming from a non-leaf node), a sibling branch is created:
//   - Format: MSGID.s.XXXX (e.g., "0003.s.0001")
//   - Siblings maintain independent message sequences
//   - Multiple siblings can exist for the same parent
//
// Atomic Operations:
//
// Message writes are atomic through a specific file creation order:
//  1. Write .txt file (if content exists)
//  2. Write .tools.json file (if tool interactions exist)
//  3. Write .json file last (commits the message)
//
// If the process is interrupted, the absence of .json means the message doesn't exist,
// preventing partial message states.
//
// Locking:
//
// The package uses file locking (flock) to ensure safe concurrent access:
//   - Session-level locks for operations like appending messages
//   - Directory creation is synchronized to prevent race conditions
//   - Lock files are created in each session directory
//
// # Core Operations
//
// Session Management:
//   - NewSession: Creates a new conversation session
//   - GetOrCreateSession: Retrieves existing or creates new session
//   - GetLineage: Retrieves the message history for a session path
//
// Message Operations:
//   - AppendMessage: Adds a new message to the session
//   - AppendMessageWithToolInteraction: Adds a message with tool calls/results
//   - ReadMessage: Reads a specific message from disk
//   - ListMessages: Lists all messages in a directory
//
// Tree Navigation:
//   - ShowSessions: Displays all sessions as a tree structure
//   - FindMessageDirectory: Locates a message within the session tree
//   - IndexMessageDirectories: Builds an index for efficient message lookups
//
// Tool Support:
//   - SaveToolInteraction: Stores tool calls or results with a message
//   - LoadToolInteraction: Retrieves tool interactions for a message
//   - CheckForPendingToolCalls: Identifies tool calls awaiting execution
//
// # Usage Example
//
//	// Create or get a session
//	session, isNew, err := GetOrCreateSession("my-session")
//	if err != nil {
//	    return err
//	}
//
//	// Append a message
//	result, err := session.AppendMessage(Message{
//	    Role:    "user",
//	    Content: "Hello, world!",
//	})
//	if err != nil {
//	    return err
//	}
//
//	// Get conversation history
//	messages, err := GetLineage(session.Path)
//	if err != nil {
//	    return err
//	}
//
// # Error Handling
//
// The package handles various error conditions:
//   - Lock acquisition timeouts
//   - Filesystem permission errors
//   - Corrupted message files (logged but skipped)
//   - Sibling overflow (too many branches)
//
// All operations that modify state use proper error propagation and cleanup
// on failure to maintain consistency.
package session
