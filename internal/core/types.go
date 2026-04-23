package core

import (
	"context"
	"net/http"
	"os"
	"time"
)

// Role represents a message role in a conversation
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// RequestConfig interface defines the contract for request configuration
type UserConfig interface {
	GetUser() string
}

type ModelConfig interface {
	GetModel() string
}

type SystemConfig interface {
	GetSystem() string
	GetEffectiveSystem() string
	IsSystemExplicitlySet() bool
}

type ProviderConfig interface {
	GetProvider() string
	GetProviderURL() string
	GetAPIKeyFile() string
	GetEnv() string
}

type StreamModeConfig interface {
	IsEmbed() bool
	IsStreamChat() bool
}

type ToolConfig interface {
	IsToolEnabled() bool
	GetToolTimeout() time.Duration
	GetToolWhitelist() string
	GetToolBlacklist() string
	GetToolAutoApprove() bool
	GetToolNonInteractive() bool
	GetMaxToolRounds() int
	GetMaxToolParallel() int
	GetToolMaxOutputBytes() int
}

type SessionResumeConfig interface {
	GetResume() string
	GetBranch() string
}

type ChatRequestConfig interface {
	UserConfig
	ModelConfig
	SystemConfig
	ProviderConfig
	StreamModeConfig
	ToolConfig
}

type EmbedRequestConfig interface {
	UserConfig
	ModelConfig
	ProviderConfig
	StreamModeConfig
}

type ResponseConfig interface {
	ProviderConfig
	StreamModeConfig
}

type RequestConfig interface {
	ChatRequestConfig
	SessionResumeConfig
}

// Logger interface for logging operations
type Logger interface {
	GetLogDir() string
	LogJSON(logDir, prefix string, data []byte) error
	CreateLogFile(logDir, prefix string) (*os.File, string, error)
	Debugf(format string, args ...interface{})
	IsDebugEnabled() bool
}

// Session interface represents a conversation session
type Session interface {
	GetPath() string
}

// Message represents a message in a session
type Message struct {
	ID      string
	Role    string
	Content string
}

// GetLineageFunc is a function type for retrieving message history
type GetLineageFunc func(path string) ([]Message, error)

// SessionStore provides an abstraction for session storage operations
// This interface replaces the multiple callback functions in ToolExecutionConfig
type SessionStore interface {
	// SaveAssistant saves an assistant message with optional tool calls
	SaveAssistant(ctx context.Context, text string, calls []ToolCall, model string) (path, messageID string, err error)

	// SaveToolResults saves tool execution results with optional additional text
	SaveToolResults(ctx context.Context, results []ToolResult, additionalText string) (path, messageID string, err error)

	// UpdatePath updates the current session path (e.g., when a sibling is created)
	UpdatePath(newPath string)

	// GetPath returns the current session path
	GetPath() string
}

// AssistantThoughtSignatureStore optionally extends SessionStore with provider-
// specific assistant metadata persistence.
type AssistantThoughtSignatureStore interface {
	SaveAssistantWithThoughtSignature(ctx context.Context, text string, calls []ToolCall, model string, thoughtSignature string) (path, messageID string, err error)
}

// RequestBuild encapsulates the result of building an HTTP request
// This struct replaces the multiple return values from buildHTTPRequest
type RequestBuild struct {
	Request  *http.Request    // The HTTP request
	Body     []byte           // The request body (for logging)
	Model    string           // The model to use
	ToolDefs []ToolDefinition // Tool definitions (if any)
}

// Notifier interface for user notifications
// This interface decouples core from CLI-specific output concerns
type Notifier interface {
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Promptf(format string, args ...interface{})
}

// Approver interface for command approval
// This interface allows different approval mechanisms (TTY, GUI, auto-approve)
type Approver interface {
	// Approve prompts for approval of a command
	// Returns true if approved, false if denied
	// The context allows cancellation of blocking approval prompts
	Approve(ctx context.Context, command []string) (bool, error)
}
