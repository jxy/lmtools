package core

import (
	"context"
	"encoding/json"
	"lmtools/internal/prompts"
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

// RequestOptions is the concrete request configuration consumed by core and
// session code after CLI flag parsing has applied defaults and validation.
type RequestOptions struct {
	User                string
	Model               string
	System              string
	EffectiveSystem     string
	SystemExplicitlySet bool
	Env                 string
	ArgoLegacy          bool
	Embed               bool
	StreamChat          bool
	Provider            string
	ProviderURL         string
	APIKeyFile          string
	Effort              string
	JSONMode            bool
	JSONSchema          json.RawMessage
	OpenAIResponses     bool
	ToolEnabled         bool
	ToolTimeout         time.Duration
	ToolWhitelist       string
	ToolBlacklist       string
	ToolAutoApprove     bool
	ToolNonInteractive  bool
	MaxToolRounds       int
	MaxToolParallel     int
	ToolMaxOutputBytes  int
	Resume              string
	Branch              string
}

func (o RequestOptions) GetUser() string   { return o.User }
func (o RequestOptions) GetModel() string  { return o.Model }
func (o RequestOptions) GetSystem() string { return o.System }

func (o RequestOptions) GetEffectiveSystem() string {
	if o.EffectiveSystem != "" {
		return o.EffectiveSystem
	}
	if o.SystemExplicitlySet {
		return o.System
	}
	if o.System == "" {
		return prompts.DefaultSystemPrompt
	}
	return o.System
}

func (o RequestOptions) IsSystemExplicitlySet() bool { return o.SystemExplicitlySet }
func (o RequestOptions) GetEnv() string              { return o.Env }
func (o RequestOptions) IsArgoLegacy() bool          { return o.ArgoLegacy }
func (o RequestOptions) IsEmbed() bool               { return o.Embed }
func (o RequestOptions) IsStreamChat() bool          { return o.StreamChat }
func (o RequestOptions) GetProvider() string         { return o.Provider }
func (o RequestOptions) GetProviderURL() string      { return o.ProviderURL }
func (o RequestOptions) GetAPIKeyFile() string       { return o.APIKeyFile }
func (o RequestOptions) GetEffort() string           { return o.Effort }
func (o RequestOptions) IsJSONMode() bool            { return o.JSONMode }
func (o RequestOptions) GetJSONSchema() json.RawMessage {
	return append(json.RawMessage(nil), o.JSONSchema...)
}
func (o RequestOptions) UseOpenAIResponses() bool { return o.OpenAIResponses }
func (o RequestOptions) IsToolEnabled() bool      { return o.ToolEnabled }

func (o RequestOptions) GetToolTimeout() time.Duration {
	if o.ToolTimeout <= 0 {
		return DefaultToolTimeout
	}
	return o.ToolTimeout
}

func (o RequestOptions) GetToolWhitelist() string    { return o.ToolWhitelist }
func (o RequestOptions) GetToolBlacklist() string    { return o.ToolBlacklist }
func (o RequestOptions) GetToolAutoApprove() bool    { return o.ToolAutoApprove }
func (o RequestOptions) GetToolNonInteractive() bool { return o.ToolNonInteractive }

func (o RequestOptions) GetMaxToolRounds() int {
	if o.MaxToolRounds <= 0 {
		return DefaultMaxToolRounds
	}
	return o.MaxToolRounds
}

func (o RequestOptions) GetMaxToolParallel() int {
	if o.MaxToolParallel <= 0 {
		return DefaultMaxToolParallel
	}
	return o.MaxToolParallel
}

func (o RequestOptions) GetToolMaxOutputBytes() int {
	if o.ToolMaxOutputBytes <= 0 {
		return int(DefaultMaxOutputSize)
	}
	const maxAllowed = 100 * 1024 * 1024
	if o.ToolMaxOutputBytes > maxAllowed {
		return maxAllowed
	}
	return o.ToolMaxOutputBytes
}

func (o RequestOptions) GetResume() string { return o.Resume }
func (o RequestOptions) GetBranch() string { return o.Branch }

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

type AssistantResponseStore interface {
	SaveAssistantResponse(ctx context.Context, response Response, model string) (path, messageID string, err error)
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
