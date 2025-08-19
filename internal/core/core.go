// Package core contains core data structures and utilities for lmtools.
// It consolidates common types that were previously scattered across
// errors, request, response, and models packages.
package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/auth"
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
	DefaultEmbedModel = "v3large"

	// Provider-specific default chat models (big models)
	DefaultArgoChatModel      = "gpt5"
	DefaultOpenAIChatModel    = "gpt-5"
	DefaultAnthropicChatModel = "claude-opus-4-1-20250805"
	DefaultGoogleChatModel    = "gemini-2.5-pro"

	// Provider-specific default small models
	DefaultArgoSmallModel      = "gpt5mini"
	DefaultOpenAISmallModel    = "gpt-5-mini"
	DefaultAnthropicSmallModel = "claude-3-5-haiku-20241022"
	DefaultGoogleSmallModel    = "gemini-2.5-flash"
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

// GetDefaultChatModel returns the default chat model for the given provider
func GetDefaultChatModel(provider string) string {
	switch provider {
	case "openai":
		return DefaultOpenAIChatModel
	case "anthropic":
		return DefaultAnthropicChatModel
	case "google":
		return DefaultGoogleChatModel
	case "argo", "":
		return DefaultArgoChatModel
	default:
		// For unknown providers, default to argo's model
		return DefaultArgoChatModel
	}
}

// GetDefaultSmallModel returns the default small model for the given provider
func GetDefaultSmallModel(provider string) string {
	switch provider {
	case "openai":
		return DefaultOpenAISmallModel
	case "anthropic":
		return DefaultAnthropicSmallModel
	case "google":
		return DefaultGoogleSmallModel
	case "argo", "":
		return DefaultArgoSmallModel
	default:
		// For unknown providers, default to argo's model
		return DefaultArgoSmallModel
	}
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
	GetProvider() string
	GetProviderURL() string
	GetAPIKeyFile() string
}

// BuildRequest builds an HTTP request based on configuration and input
func BuildRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
	// Get provider, default to argo
	provider := cfg.GetProvider()
	if provider == "" {
		provider = "argo"
	}

	// Route to provider-specific builder
	switch provider {
	case "argo":
		return buildArgoRequest(cfg, input)
	case "openai":
		return buildOpenAIDirectRequest(cfg, input)
	case "google":
		return buildGoogleDirectRequest(cfg, input)
	case "anthropic":
		return buildAnthropicDirectRequest(cfg, input)
	default:
		return nil, nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// buildArgoRequest builds a request for the Argo proxy (existing logic)
func buildArgoRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
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
		// No longer validate embed model
		req := SimpleEmbedRequest{User: cfg.GetUser(), Model: model, Prompt: []string{input}}
		if body, err = json.Marshal(req); err != nil {
			return nil, nil, fmt.Errorf("failed to marshal embed request: %w", err)
		}
		endpoint = fmt.Sprintf("%s/embed/", urlBase)
	} else {
		if model == "" {
			model = GetDefaultChatModel("argo")
		}
		// No longer validate chat model
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

	// Use centralized header setting
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "argo")

	return httpReq, body, nil
}

// buildOpenAIDirectRequest builds a request for OpenAI API directly
func buildOpenAIDirectRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
	var apiKey string

	// Only require API key for standard endpoints
	if cfg.GetProviderURL() == "" {
		// Read API key (required for standard OpenAI endpoint)
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read API key: %w", err)
		}

		// Validate API key
		if err := auth.ValidateAPIKey(apiKey, "openai"); err != nil {
			return nil, nil, fmt.Errorf("invalid API key: %w", err)
		}
	} else if cfg.GetAPIKeyFile() != "" {
		// API key is optional for custom URLs, but use it if provided
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			// Log warning but don't fail
			fmt.Fprintf(os.Stderr, "Warning: failed to read API key file: %v\n", err)
		}
	}

	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel("openai")
	}

	// No longer validate model for provider

	// Build OpenAI format request
	var req map[string]interface{}

	if cfg.IsEmbed() {
		// OpenAI embedding format
		req = map[string]interface{}{
			"model": model,
			"input": input,
		}
	} else {
		// OpenAI chat format
		req = map[string]interface{}{
			"model": model,
			"messages": []map[string]string{
				{"role": "system", "content": cfg.GetSystem()},
				{"role": "user", "content": input},
			},
			"stream": cfg.IsStreamChat(),
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Determine endpoint
	url := cfg.GetProviderURL()
	if url == "" {
		// Use default OpenAI base URL
		url = "https://api.openai.com/v1"
	}

	// Append appropriate endpoint
	if cfg.IsEmbed() {
		url = strings.TrimRight(url, "/") + "/embeddings"
	} else {
		url = strings.TrimRight(url, "/") + "/chat/completions"
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use centralized header setting
	auth.SetProviderHeaders(httpReq, "openai", apiKey)
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "openai")

	return httpReq, body, nil
}

// buildGoogleDirectRequest builds a request for Google Gemini API directly
func buildGoogleDirectRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
	var apiKey string

	// Only require API key for standard endpoints
	if cfg.GetProviderURL() == "" {
		// Read API key (required for standard Google endpoint)
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read API key: %w", err)
		}

		// Validate API key
		if err := auth.ValidateAPIKey(apiKey, "google"); err != nil {
			return nil, nil, fmt.Errorf("invalid API key: %w", err)
		}
	} else if cfg.GetAPIKeyFile() != "" {
		// API key is optional for custom URLs, but use it if provided
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			// Log warning but don't fail
			fmt.Fprintf(os.Stderr, "Warning: failed to read API key file: %v\n", err)
		}
	}

	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel("google")
	}

	// No longer validate model for provider

	// Google doesn't have separate embedding endpoint
	if cfg.IsEmbed() {
		return nil, nil, fmt.Errorf("google provider does not support embedding mode")
	}

	// Build Google Gemini format request
	req := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"text": input},
				},
			},
		},
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]string{
				{"text": cfg.GetSystem()},
			},
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Determine endpoint
	url := cfg.GetProviderURL()
	if url == "" {
		// Standard Google endpoint requires API key
		if apiKey == "" {
			return nil, nil, fmt.Errorf("API key is required for standard Google endpoint")
		}
		// Use default Google Gemini base URL
		url = "https://generativelanguage.googleapis.com/v1beta"
	}

	// Build complete URL with model and method
	url = strings.TrimRight(url, "/")
	if cfg.IsStreamChat() {
		url = fmt.Sprintf("%s/models/%s:streamGenerateContent", url, model)
	} else {
		url = fmt.Sprintf("%s/models/%s:generateContent", url, model)
	}

	// Add API key as query parameter
	if apiKey != "" {
		if strings.Contains(url, "?") {
			url += "&key=" + apiKey
		} else {
			url += "?key=" + apiKey
		}
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use centralized header setting
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "google")

	return httpReq, body, nil
}

// buildAnthropicDirectRequest builds a request for Anthropic API directly
func buildAnthropicDirectRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
	var apiKey string

	// Only require API key for standard endpoints
	if cfg.GetProviderURL() == "" {
		// Read API key (required for standard Anthropic endpoint)
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read API key: %w", err)
		}

		// Validate API key
		if err := auth.ValidateAPIKey(apiKey, "anthropic"); err != nil {
			return nil, nil, fmt.Errorf("invalid API key: %w", err)
		}
	} else if cfg.GetAPIKeyFile() != "" {
		// API key is optional for custom URLs, but use it if provided
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			// Log warning but don't fail
			fmt.Fprintf(os.Stderr, "Warning: failed to read API key file: %v\n", err)
		}
	}

	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel("anthropic")
	}

	// No longer validate model for provider

	// Anthropic doesn't have separate embedding endpoint
	if cfg.IsEmbed() {
		return nil, nil, fmt.Errorf("anthropic provider does not support embedding mode")
	}

	// Build Anthropic format request
	req := map[string]interface{}{
		"model":      model,
		"max_tokens": 4096,
		"messages": []map[string]string{
			{"role": "user", "content": input},
		},
		"system": cfg.GetSystem(),
		"stream": cfg.IsStreamChat(),
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Determine endpoint
	url := cfg.GetProviderURL()
	if url == "" {
		// Use default Anthropic base URL
		url = "https://api.anthropic.com/v1"
	}

	// Append messages endpoint
	url = strings.TrimRight(url, "/") + "/messages"

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use centralized header setting
	auth.SetProviderHeaders(httpReq, "anthropic", apiKey)
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "anthropic")

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

// HandleResponse processes an HTTP response based on configuration.
// For streaming responses, it has dual behavior:
// 1. Prints the streamed content directly to os.Stdout in real-time
// 2. Returns the full accumulated content as a string for session storage
// The response body is closed by this function - callers should not close it.
func HandleResponse(ctx context.Context, cfg RequestConfig, resp *http.Response, logger Logger) (string, error) {
	defer resp.Body.Close()

	// Get provider, default to argo
	provider := cfg.GetProvider()
	if provider == "" {
		provider = "argo"
	}

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

	// Handle streaming responses with provider-specific parsing
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

		// Stream and parse based on provider
		switch provider {
		case "argo":
			return handleArgoStream(ctx, resp.Body, f)
		case "openai":
			return handleOpenAIStream(ctx, resp.Body, f)
		case "anthropic":
			return handleAnthropicStream(ctx, resp.Body, f)
		case "google":
			return handleGoogleStream(ctx, resp.Body, f)
		default:
			// Fallback: just copy raw stream
			var buf bytes.Buffer
			done := make(chan error, 1)
			go func() {
				_, err := io.Copy(io.MultiWriter(os.Stdout, f, &buf), resp.Body)
				done <- err
			}()

			select {
			case <-ctx.Done():
				return buf.String(), fmt.Errorf("streaming interrupted: %w", ctx.Err())
			case err := <-done:
				if err != nil {
					return buf.String(), fmt.Errorf("error streaming response: %w", err)
				}
				return buf.String(), nil
			}
		}
	}

	// Read response body
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Log response
	logPrefix := "chat_output"
	if cfg.IsEmbed() {
		logPrefix = "embed_output"
	}
	if err := logger.LogJSON(logger.GetLogDir(), logPrefix, data); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to log response: %v\n", err)
	}

	// Route to provider-specific parser
	switch provider {
	case "argo":
		return parseArgoResponse(data, cfg.IsEmbed())
	case "openai":
		return parseOpenAIResponse(data, cfg.IsEmbed())
	case "google":
		return parseGeminiResponse(data, cfg.IsEmbed())
	case "anthropic":
		return parseAnthropicResponse(data, cfg.IsEmbed())
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// parseArgoResponse parses response from Argo API
func parseArgoResponse(data []byte, isEmbed bool) (string, error) {
	if isEmbed {
		var embedResp struct {
			Embedding [][]float64 `json:"embedding"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			return "", fmt.Errorf("failed to unmarshal Argo embed response: %w", err)
		}
		if len(embedResp.Embedding) == 0 {
			return "", fmt.Errorf("empty embedding response")
		}
		if len(embedResp.Embedding[0]) == 0 {
			return "[]", nil
		}
		embeddingJSON, err := json.Marshal(embedResp.Embedding[0])
		if err != nil {
			return "", fmt.Errorf("failed to marshal embedding: %w", err)
		}
		return string(embeddingJSON), nil
	}

	var chatResp struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &chatResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal Argo chat response: %w", err)
	}
	return chatResp.Response, nil
}

// parseOpenAIResponse parses response from OpenAI API
func parseOpenAIResponse(data []byte, isEmbed bool) (string, error) {
	if isEmbed {
		var embedResp struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			return "", fmt.Errorf("failed to unmarshal OpenAI embed response: %w", err)
		}
		if len(embedResp.Data) == 0 {
			return "", fmt.Errorf("empty embedding response")
		}
		if len(embedResp.Data[0].Embedding) == 0 {
			return "[]", nil
		}
		embeddingJSON, err := json.Marshal(embedResp.Data[0].Embedding)
		if err != nil {
			return "", fmt.Errorf("failed to marshal embedding: %w", err)
		}
		return string(embeddingJSON), nil
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chatResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal OpenAI chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in OpenAI response")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// parseGeminiResponse parses response from Gemini API
func parseGeminiResponse(data []byte, isEmbed bool) (string, error) {
	if isEmbed {
		// Gemini doesn't support embeddings through this interface
		return "", fmt.Errorf("gemini provider does not support embedding mode")
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &geminiResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal Gemini response: %w", err)
	}
	if len(geminiResp.Candidates) == 0 {
		return "", fmt.Errorf("no candidates in Gemini response")
	}
	if len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no parts in Gemini response")
	}
	return geminiResp.Candidates[0].Content.Parts[0].Text, nil
}

// parseAnthropicResponse parses response from Anthropic API
func parseAnthropicResponse(data []byte, isEmbed bool) (string, error) {
	if isEmbed {
		// Anthropic doesn't support embeddings
		return "", fmt.Errorf("anthropic provider does not support embedding mode")
	}

	var anthropicResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &anthropicResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal Anthropic response: %w", err)
	}
	if len(anthropicResp.Content) == 0 {
		return "", fmt.Errorf("no content in Anthropic response")
	}
	return anthropicResp.Content[0].Text, nil
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

	// Get provider, default to argo
	provider := cfg.GetProvider()
	if provider == "" {
		provider = "argo"
	}

	// Route to provider-specific builder
	switch provider {
	case "argo":
		return buildArgoRequestWithSession(cfg, messages)
	case "openai":
		return buildOpenAIRequestWithSession(cfg, messages)
	case "google":
		return buildGoogleRequestWithSession(cfg, messages)
	case "anthropic":
		return buildAnthropicRequestWithSession(cfg, messages)
	default:
		return nil, nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// buildArgoRequestWithSession builds an Argo request with session history
func buildArgoRequestWithSession(cfg RequestConfig, messages []Message) (*http.Request, []byte, error) {
	// Convert to ChatMessage format
	chatMessages := []SimpleChatMessage{{Role: "system", Content: cfg.GetSystem()}}
	for _, msg := range messages {
		chatMessages = append(chatMessages, SimpleChatMessage(msg))
	}

	urlBase := GetBaseURL(cfg.GetEnv())
	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel("argo")
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

	// Use centralized header setting
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "argo")

	return httpReq, body, nil
}

// buildOpenAIRequestWithSession builds an OpenAI request with session history
func buildOpenAIRequestWithSession(cfg RequestConfig, messages []Message) (*http.Request, []byte, error) {
	var apiKey string

	// Only require API key for standard endpoints
	if cfg.GetProviderURL() == "" {
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read API key: %w", err)
		}

		if err := auth.ValidateAPIKey(apiKey, "openai"); err != nil {
			return nil, nil, fmt.Errorf("invalid API key: %w", err)
		}
	} else if cfg.GetAPIKeyFile() != "" {
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read API key file: %v\n", err)
		}
	}

	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel("openai")
	}

	// Build OpenAI format messages
	openAIMessages := []map[string]string{
		{"role": "system", "content": cfg.GetSystem()},
	}
	for _, msg := range messages {
		openAIMessages = append(openAIMessages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// OpenAI chat format
	req := map[string]interface{}{
		"model":    model,
		"messages": openAIMessages,
		"stream":   cfg.IsStreamChat(),
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Determine endpoint
	url := cfg.GetProviderURL()
	if url == "" {
		url = "https://api.openai.com/v1"
	}
	url = strings.TrimRight(url, "/") + "/chat/completions"

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use centralized header setting
	auth.SetProviderHeaders(httpReq, "openai", apiKey)
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "openai")

	return httpReq, body, nil
}

// buildGoogleRequestWithSession builds a Google request with session history
func buildGoogleRequestWithSession(cfg RequestConfig, messages []Message) (*http.Request, []byte, error) {
	var apiKey string

	// Only require API key for standard endpoints
	if cfg.GetProviderURL() == "" {
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read API key: %w", err)
		}

		if err := auth.ValidateAPIKey(apiKey, "google"); err != nil {
			return nil, nil, fmt.Errorf("invalid API key: %w", err)
		}
	} else if cfg.GetAPIKeyFile() != "" {
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read API key file: %v\n", err)
		}
	}

	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel("google")
	}

	// Build Google Gemini format - combine all messages into contents
	var contents []map[string]interface{}
	for _, msg := range messages {
		if msg.Role == "user" || msg.Role == "assistant" {
			role := "user"
			if msg.Role == "assistant" {
				role = "model"
			}
			contents = append(contents, map[string]interface{}{
				"role": role,
				"parts": []map[string]string{
					{"text": msg.Content},
				},
			})
		}
	}

	req := map[string]interface{}{
		"contents": contents,
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]string{
				{"text": cfg.GetSystem()},
			},
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Determine endpoint
	url := cfg.GetProviderURL()
	if url == "" {
		if apiKey == "" {
			return nil, nil, fmt.Errorf("API key is required for standard Google endpoint")
		}
		url = "https://generativelanguage.googleapis.com/v1beta"
	}

	url = strings.TrimRight(url, "/")
	if cfg.IsStreamChat() {
		url = fmt.Sprintf("%s/models/%s:streamGenerateContent", url, model)
	} else {
		url = fmt.Sprintf("%s/models/%s:generateContent", url, model)
	}

	// Add API key as query parameter
	if apiKey != "" {
		if strings.Contains(url, "?") {
			url += "&key=" + apiKey
		} else {
			url += "?key=" + apiKey
		}
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use centralized header setting
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "google")

	return httpReq, body, nil
}

// buildAnthropicRequestWithSession builds an Anthropic request with session history
func buildAnthropicRequestWithSession(cfg RequestConfig, messages []Message) (*http.Request, []byte, error) {
	var apiKey string

	// Only require API key for standard endpoints
	if cfg.GetProviderURL() == "" {
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read API key: %w", err)
		}

		if err := auth.ValidateAPIKey(apiKey, "anthropic"); err != nil {
			return nil, nil, fmt.Errorf("invalid API key: %w", err)
		}
	} else if cfg.GetAPIKeyFile() != "" {
		var err error
		apiKey, err = auth.ReadKeyFile(cfg.GetAPIKeyFile())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read API key file: %v\n", err)
		}
	}

	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel("anthropic")
	}

	// Build Anthropic format messages
	anthropicMessages := []map[string]string{}
	for _, msg := range messages {
		if msg.Role == "user" || msg.Role == "assistant" {
			anthropicMessages = append(anthropicMessages, map[string]string{
				"role":    msg.Role,
				"content": msg.Content,
			})
		}
	}

	// Anthropic format request
	req := map[string]interface{}{
		"model":      model,
		"max_tokens": 4096,
		"messages":   anthropicMessages,
		"system":     cfg.GetSystem(),
		"stream":     cfg.IsStreamChat(),
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Determine endpoint
	url := cfg.GetProviderURL()
	if url == "" {
		url = "https://api.anthropic.com/v1"
	}
	url = strings.TrimRight(url, "/") + "/messages"

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use centralized header setting
	auth.SetProviderHeaders(httpReq, "anthropic", apiKey)
	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), "anthropic")

	return httpReq, body, nil
}

// BuildRegenerationRequest builds a request for regenerating a response
func BuildRegenerationRequest(cfg RequestConfig, sess Session, getLineage GetLineageFunc) (*http.Request, []byte, error) {
	return BuildRequestWithSession(cfg, sess, getLineage)
}

// ============================================================================
// Streaming Response Handlers
// ============================================================================

// streamParser defines a function that processes a line from a stream
// and returns the content to be printed/accumulated, whether parsing is done,
// and any error encountered
type streamParser func(line string, state interface{}) (content string, done bool, err error)

// handleGenericStream is a reusable stream handler that processes streaming responses
// using a provided parser function
func handleGenericStream(ctx context.Context, body io.ReadCloser, logFile *os.File, parser streamParser, initialState interface{}) (string, error) {
	// Body is closed by HandleResponse, not here

	var accumulated strings.Builder
	scanner := bufio.NewScanner(body)
	// Increase buffer size to handle large SSE lines (default is ~64KB)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024) // 2MB max

	state := initialState
	var sseBuf []string

	// Helper to flush accumulated SSE data lines
	flushSSE := func() (bool, error) {
		if len(sseBuf) == 0 {
			return false, nil
		}

		// Join accumulated data lines
		joined := strings.Join(sseBuf, "\n")
		sseBuf = sseBuf[:0]

		// Log the complete data event
		_, _ = logFile.WriteString("data: " + joined + "\n\n")

		// Parse the complete data
		content, done, err := parser("data: "+joined, state)
		if err != nil {
			// Log parsing error to file, not stderr
			_, _ = fmt.Fprintf(logFile, "# parsing error: %v\n", err)
			return false, nil
		}

		if content != "" {
			fmt.Print(content)
			accumulated.WriteString(content)
		}

		return done, nil
	}

scanLoop:
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return accumulated.String(), ctx.Err()
		default:
			line := scanner.Text()

			// Handle different SSE line types
			if line == "" {
				// Empty line signals end of SSE event
				if done, _ := flushSSE(); done {
					break scanLoop
				}
				continue
			}

			if strings.HasPrefix(line, ":") {
				// SSE comment line - log but ignore
				_, _ = logFile.WriteString(line + "\n")
				continue
			}

			if strings.HasPrefix(line, "data: ") {
				// Accumulate data line (strip "data: " prefix)
				sseBuf = append(sseBuf, strings.TrimPrefix(line, "data: "))
				continue
			}

			// Other SSE fields (event:, id:, retry:) - log as-is
			_, _ = logFile.WriteString(line + "\n")

			// For non-data lines, still try to parse them with the parser
			// This maintains backward compatibility with parsers expecting raw lines
			content, done, err := parser(line, state)
			if err != nil {
				_, _ = fmt.Fprintf(logFile, "# parsing error: %v\n", err)
				continue
			}
			if done {
				break scanLoop
			}
			if content != "" {
				fmt.Print(content)
				accumulated.WriteString(content)
			}
		}
	}

	// Flush any remaining SSE data
	_, _ = flushSSE()

	if err := scanner.Err(); err != nil {
		return accumulated.String(), err
	}

	return accumulated.String(), nil
}

// handleArgoStream handles Argo's plain text streaming format
func handleArgoStream(ctx context.Context, body io.ReadCloser, logFile *os.File) (string, error) {
	// Body is closed by HandleResponse, not here

	var accumulated strings.Builder
	buffer := make([]byte, 4096) // 4KB chunks for real-time streaming

	for {
		select {
		case <-ctx.Done():
			return accumulated.String(), ctx.Err()
		default:
			n, err := body.Read(buffer)
			if n > 0 {
				chunk := string(buffer[:n])
				fmt.Print(chunk)
				accumulated.WriteString(chunk)
				_, _ = logFile.WriteString(chunk)
			}
			if err == io.EOF {
				return accumulated.String(), nil
			}
			if err != nil {
				return accumulated.String(), err
			}
		}
	}
}

// parseOpenAIStreamLine parses a line from OpenAI's SSE stream
func parseOpenAIStreamLine(line string, state interface{}) (string, bool, error) {
	if strings.HasPrefix(line, "data: ") {
		data := strings.TrimPrefix(line, "data: ")

		// Check for completion marker
		if data == "[DONE]" {
			return "", true, nil // done
		}

		// Parse JSON chunk
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", false, err
		}

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			return chunk.Choices[0].Delta.Content, false, nil
		}
	}

	return "", false, nil
}

// handleOpenAIStream handles OpenAI's SSE format with JSON payloads
func handleOpenAIStream(ctx context.Context, body io.ReadCloser, logFile *os.File) (string, error) {
	return handleGenericStream(ctx, body, logFile, parseOpenAIStreamLine, nil)
}

// anthropicStreamState tracks the current event type for Anthropic's SSE format
type anthropicStreamState struct {
	currentEvent string
}

// parseAnthropicStreamLine parses a line from Anthropic's SSE stream
func parseAnthropicStreamLine(line string, state interface{}) (string, bool, error) {
	s := state.(*anthropicStreamState)

	if strings.HasPrefix(line, "event: ") {
		s.currentEvent = strings.TrimPrefix(line, "event: ")
		return "", false, nil
	}

	if strings.HasPrefix(line, "data: ") && s.currentEvent == "content_block_delta" {
		data := strings.TrimPrefix(line, "data: ")

		// Parse JSON delta
		var delta struct {
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}

		if err := json.Unmarshal([]byte(data), &delta); err != nil {
			return "", false, err
		}

		return delta.Delta.Text, false, nil
	}

	return "", false, nil
}

// handleAnthropicStream handles Anthropic's SSE format with event types
func handleAnthropicStream(ctx context.Context, body io.ReadCloser, logFile *os.File) (string, error) {
	state := &anthropicStreamState{}
	return handleGenericStream(ctx, body, logFile, parseAnthropicStreamLine, state)
}

// parseGoogleStreamLine parses a line from Google's SSE stream
func parseGoogleStreamLine(line string, state interface{}) (string, bool, error) {
	if strings.HasPrefix(line, "data: ") {
		data := strings.TrimPrefix(line, "data: ")

		// Parse JSON chunk
		var chunk struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", false, err
		}

		// Extract text from first candidate's first part
		if len(chunk.Candidates) > 0 &&
			len(chunk.Candidates[0].Content.Parts) > 0 &&
			chunk.Candidates[0].Content.Parts[0].Text != "" {
			return chunk.Candidates[0].Content.Parts[0].Text, false, nil
		}
	}

	return "", false, nil
}

// handleGoogleStream handles Google's SSE format for Gemini
func handleGoogleStream(ctx context.Context, body io.ReadCloser, logFile *os.File) (string, error) {
	return handleGenericStream(ctx, body, logFile, parseGoogleStreamLine, nil)
}
