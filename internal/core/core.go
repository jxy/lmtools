// Package core contains core data structures and utilities for lmtools.
// It consolidates common types that were previously scattered across
// errors, request, response, and models packages.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// ============================================================================
// Common Errors
// ============================================================================

var (
	ErrInterrupted  = errors.New("operation interrupted")
	ErrNoInput      = errors.New("no input provided")
	ErrInvalidInput = errors.New("invalid input")
)

// WrapError provides consistent error wrapping with context
func WrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// HTTPError represents an HTTP error response
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// NewHTTPError creates a new HTTP error
func NewHTTPError(statusCode int, body string) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Body:       body,
	}
}

// IsRetryable returns true if the HTTP error is retryable
func (e *HTTPError) IsRetryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		425,                            // Too Early
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

// ValidationError represents a validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("validation error for field %s: %s", e.Field, e.Message)
	}
	return e.Message
}

// ConfigError represents a configuration error
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("configuration error: %s", e.Message)
}

// SessionError represents a session-related error
type SessionError struct {
	Operation string
	Path      string
	Err       error
}

func (e *SessionError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("session %s error at %s: %v", e.Operation, e.Path, e.Err)
	}
	return fmt.Sprintf("session %s error: %v", e.Operation, e.Err)
}

func (e *SessionError) Unwrap() error {
	return e.Err
}

// ProxyError represents an API proxy error
type ProxyError struct {
	Provider string
	Message  string
	Err      error
}

func (e *ProxyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("proxy error for provider %s: %s: %v", e.Provider, e.Message, e.Err)
	}
	return fmt.Sprintf("proxy error for provider %s: %s", e.Provider, e.Message)
}

func (e *ProxyError) Unwrap() error {
	return e.Err
}

// ============================================================================
// Models and Constants
// ============================================================================

// Common role types for chat messages
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Default models
const (
	DefaultChatModel  = "gemini25pro"
	DefaultEmbedModel = "v3large"
)

// API endpoints
const (
	ArgoProdURL = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
	ArgoDevURL  = "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource"
)

// Input validation constants
const (
	MaxInputSizeBytes = 1024 * 1024 // 1MB
)

// Supported chat models
var ChatModels = []string{
	"gpt35",
	"gpt35large",
	"gpt4",
	"gpt4large",
	"gpt4turbo",
	"gpt4o",
	"gpt4olatest",
	"gpto1mini",
	"gpto3mini",
	"gpto1",
	"gpto3",
	"gpto4mini",
	"gpt41",
	"gpt41mini",
	"gpt41nano",
	"gpt5",
	"gpt5mini",
	"gpt5nano",
	"gemini25pro",
	"gemini25flash",
	"claudeopus4",
	"claudesonnet4",
	"claudesonnet37",
	"claudesonnet35v2",
}

// Supported embedding models
var EmbedModels = []string{
	"v3small",
	"v3large",
}

// IsValidChatModel checks if a model is valid for chat
func IsValidChatModel(model string) bool {
	for _, m := range ChatModels {
		if m == model {
			return true
		}
	}
	return false
}

// IsValidEmbedModel checks if a model is valid for embedding
func IsValidEmbedModel(model string) bool {
	for _, m := range EmbedModels {
		if m == model {
			return true
		}
	}
	return false
}

// NormalizeModel normalizes model names for consistency
func NormalizeModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// GetBaseURL returns the base URL for the given environment
func GetBaseURL(env string) string {
	switch strings.ToLower(env) {
	case "dev":
		return ArgoDevURL
	case "prod":
		return ArgoProdURL
	default:
		// If it looks like a URL, use it directly
		lowerEnv := strings.ToLower(env)
		if strings.HasPrefix(lowerEnv, "http://") || strings.HasPrefix(lowerEnv, "https://") {
			return env
		}
		// Default to prod
		return ArgoProdURL
	}
}

// ============================================================================
// Request/Response Types
// ============================================================================

// ChatMessage represents a message in a chat conversation
type ChatMessage struct {
	Role    Role            `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ChatRequest represents a generic chat request
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

// ChatResponse represents a generic chat response
type ChatResponse struct {
	ID      string `json:"id,omitempty"`
	Model   string `json:"model,omitempty"`
	Content string `json:"content,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
}

// EmbedRequest represents a generic embedding request
type EmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbedResponse represents a generic embedding response
type EmbedResponse struct {
	Model      string      `json:"model,omitempty"`
	Embeddings [][]float64 `json:"embeddings"`
	Usage      *Usage      `json:"usage,omitempty"`
}

// Usage represents token usage information
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

// StreamChunk represents a chunk in a streaming response
type StreamChunk struct {
	ID      string `json:"id,omitempty"`
	Model   string `json:"model,omitempty"`
	Content string `json:"content,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

// ============================================================================
// Request Building (from request package)
// ============================================================================

// SimpleChatMessage is the simple format for chat messages used by lmc CLI
type SimpleChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SimpleChatRequest is the simple format used by lmc CLI
type SimpleChatRequest struct {
	User     string              `json:"user"`
	Model    string              `json:"model"`
	Messages []SimpleChatMessage `json:"messages"`
}

// SimpleEmbedRequest is the simple format used by lmc CLI
type SimpleEmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

// Config represents the configuration needed for building requests
// This is a subset of the full config to avoid circular dependencies
type RequestConfig interface {
	GetUser() string
	GetModel() string
	GetSystem() string
	GetEnv() string
	IsEmbed() bool
	IsStreamChat() bool
}

// BuildRequest builds an HTTP request based on configuration and input
func BuildRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
	urlBase := GetBaseURL(cfg.GetEnv())

	model := cfg.GetModel()
	var (
		body     []byte
		endpoint string
		err      error
	)
	if cfg.IsEmbed() {
		if model == "" {
			model = DefaultEmbedModel
		}
		// Validate embed model
		if !IsValidEmbedModel(model) {
			return nil, nil, fmt.Errorf("invalid embed model: %s", model)
		}
		req := SimpleEmbedRequest{User: cfg.GetUser(), Model: model, Prompt: []string{input}}
		if body, err = json.Marshal(req); err != nil {
			return nil, nil, fmt.Errorf("failed to marshal embed request: %w", err)
		}
		endpoint = fmt.Sprintf("%s/embed/", urlBase)
	} else {
		if model == "" {
			model = DefaultChatModel
		}
		// Validate chat model
		if !IsValidChatModel(model) {
			return nil, nil, fmt.Errorf("invalid chat model: %s", model)
		}
		req := SimpleChatRequest{
			User:  cfg.GetUser(),
			Model: model,
			Messages: []SimpleChatMessage{
				{Role: "system", Content: cfg.GetSystem()},
				{Role: "user", Content: input},
			},
		}
		if body, err = json.Marshal(req); err != nil {
			return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
		}
		if cfg.IsStreamChat() {
			endpoint = fmt.Sprintf("%s/streamchat/", urlBase)
		} else {
			endpoint = fmt.Sprintf("%s/chat/", urlBase)
		}
	}

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("Accept-Encoding", "identity")
	return httpReq, body, nil
}

// ============================================================================
// Response Handling (from response package)
// ============================================================================

// Logger interface to avoid circular dependency
type Logger interface {
	GetLogDir() string
	LogJSON(logDir, prefix string, data []byte) error
	CreateLogFile(logDir, prefix string) (*os.File, string, error)
}

// HandleResponse processes an HTTP response based on configuration
func HandleResponse(ctx context.Context, cfg RequestConfig, resp *http.Response, logger Logger) (string, error) {
	defer resp.Body.Close()

	// Validate HTTP status first
	if resp.StatusCode != http.StatusOK {
		// Read limited body for error message
		limitedBody := io.LimitReader(resp.Body, 1024) // 1KB limit
		errorData, err := io.ReadAll(limitedBody)
		if err != nil {
			errorData = []byte("failed to read error response")
		}
		return "", NewHTTPError(resp.StatusCode, string(errorData))
	}

	if cfg.IsEmbed() {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read response body: %w", err)
		}
		if err := logger.LogJSON(logger.GetLogDir(), "embed_output", data); err != nil {
			return "", fmt.Errorf("failed to log embed response: %w", err)
		}
		var embedResp struct {
			Embedding [][]float64 `json:"embedding"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			// Don't include full data in error to avoid memory issues with large responses
			dataPreview := string(data)
			if len(dataPreview) > 100 {
				dataPreview = dataPreview[:100] + "..."
			}
			return "", fmt.Errorf("failed to unmarshal embed response (preview: %s): %w", dataPreview, err)
		}
		// Convert the embedding array to a string representation
		if len(embedResp.Embedding) == 0 {
			return "", fmt.Errorf("empty embedding response")
		}
		// Check if the first embedding vector is empty
		if len(embedResp.Embedding[0]) == 0 {
			return "[]", nil
		}
		// Marshal the first embedding vector to JSON string
		embeddingJSON, err := json.Marshal(embedResp.Embedding[0])
		if err != nil {
			return "", fmt.Errorf("failed to marshal embedding: %w", err)
		}
		return string(embeddingJSON), nil
	}

	if cfg.IsStreamChat() {
		f, path, err := logger.CreateLogFile(logger.GetLogDir(), "stream_chat_output")
		if err != nil {
			return "", fmt.Errorf("failed to create log file: %w", err)
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "failed to close log file %s: %v\n", path, closeErr)
			}
		}()

		// Use context-aware copy to handle interrupts
		done := make(chan error, 1)
		go func() {
			_, err := io.Copy(io.MultiWriter(os.Stdout, f), resp.Body)
			done <- err
		}()

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("streaming interrupted: %w", ctx.Err())
		case err := <-done:
			if err != nil {
				return "", fmt.Errorf("error streaming response to stdout and log file %s: %w", path, err)
			}
			return "", nil
		}
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}
	if err := logger.LogJSON(logger.GetLogDir(), "chat_output", data); err != nil {
		return "", fmt.Errorf("failed to log chat response: %w", err)
	}
	var chatResp struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &chatResp); err != nil {
		// Don't include full data in error to avoid memory issues with large responses
		dataPreview := string(data)
		if len(dataPreview) > 100 {
			dataPreview = dataPreview[:100] + "..."
		}
		return "", fmt.Errorf("failed to unmarshal chat response (preview: %s): %w", dataPreview, err)
	}
	return chatResp.Response, nil
}

// ============================================================================
// Session-related Request Building
// ============================================================================

// Session represents a conversation session (simplified interface to avoid circular deps)
type Session interface {
	GetPath() string
}

// Message represents a conversation message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GetLineageFunc is a function type for getting conversation history
type GetLineageFunc func(path string) ([]Message, error)

// BuildRequestWithSession builds a request that includes conversation history from a session
func BuildRequestWithSession(cfg RequestConfig, sess Session, getLineage GetLineageFunc) (*http.Request, []byte, error) {
	if cfg.IsEmbed() {
		return nil, nil, fmt.Errorf("embed mode does not support sessions")
	}

	// Get conversation history
	messages, err := getLineage(sess.GetPath())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get conversation history: %w", err)
	}

	// Convert to ChatMessage format
	chatMessages := []SimpleChatMessage{{Role: "system", Content: cfg.GetSystem()}}
	for _, msg := range messages {
		chatMessages = append(chatMessages, SimpleChatMessage(msg))
	}

	urlBase := GetBaseURL(cfg.GetEnv())
	model := cfg.GetModel()
	if model == "" {
		model = DefaultChatModel
	}

	// Validate chat model
	if !IsValidChatModel(model) {
		return nil, nil, fmt.Errorf("invalid chat model: %s", model)
	}

	req := SimpleChatRequest{
		User:     cfg.GetUser(),
		Model:    model,
		Messages: chatMessages,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/chat/", urlBase)
	if cfg.IsStreamChat() {
		endpoint = fmt.Sprintf("%s/streamchat/", urlBase)
	}

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("Accept-Encoding", "identity")
	return httpReq, body, nil
}

// BuildRegenerationRequest builds a request for regenerating a response
func BuildRegenerationRequest(cfg RequestConfig, sess Session, getLineage GetLineageFunc) (*http.Request, []byte, error) {
	// The session has already been created in handleSession, so we use it directly
	// Get the lineage for this new branch
	messages, err := getLineage(sess.GetPath())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get lineage: %w", err)
	}

	// Convert to chat messages
	chatMessages := []SimpleChatMessage{{Role: "system", Content: cfg.GetSystem()}}
	for _, msg := range messages {
		chatMessages = append(chatMessages, SimpleChatMessage(msg))
	}

	// Build the request
	urlBase := GetBaseURL(cfg.GetEnv())
	model := cfg.GetModel()
	if model == "" {
		model = DefaultChatModel
	}

	if !IsValidChatModel(model) {
		return nil, nil, fmt.Errorf("invalid chat model: %s", model)
	}

	req := SimpleChatRequest{
		User:     cfg.GetUser(),
		Model:    model,
		Messages: chatMessages,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/chat/", urlBase)
	if cfg.IsStreamChat() {
		endpoint = fmt.Sprintf("%s/streamchat/", urlBase)
	}

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("Accept-Encoding", "identity")
	return httpReq, body, nil
}
