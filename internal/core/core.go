// Package core contains core data structures and utilities for lmtools.
// It consolidates common types that were previously scattered across
// errors, request, response, and models packages.
package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"lmtools/internal/limitio"
	"lmtools/internal/providers"
	"net/http"
	"os"
	"strings"
)

// ============================================================================
// Common Errors
// ============================================================================

var (
	ErrInterrupted  = stdErrors.New("operation interrupted")
	ErrNoInput      = stdErrors.New("no input provided")
	ErrInvalidInput = stdErrors.New("invalid input")
)

// ============================================================================
// Models and Constants
// ============================================================================

// Default models
const (
	DefaultEmbedModel = "v3large"

	// Provider-specific default chat models (big models)
	DefaultArgoChatModel      = providers.DefaultArgoChatModel
	DefaultOpenAIChatModel    = providers.DefaultOpenAIChatModel
	DefaultAnthropicChatModel = providers.DefaultAnthropicChatModel
	DefaultGoogleChatModel    = providers.DefaultGoogleChatModel

	// Provider-specific default small models
	DefaultArgoSmallModel      = providers.DefaultArgoSmallModel
	DefaultOpenAISmallModel    = providers.DefaultOpenAISmallModel
	DefaultAnthropicSmallModel = providers.DefaultAnthropicSmallModel
	DefaultGoogleSmallModel    = providers.DefaultGoogleSmallModel
)

// API endpoints
const (
	ArgoProdURL = providers.ArgoProdURL
	ArgoDevURL  = providers.ArgoDevURL
)

// ============================================================================
// Request Building
// ============================================================================

// getProviderWithDefault returns the provider from config or a default value
func getProviderWithDefault(cfg ProviderConfig, defaultProvider string) string {
	return providers.ResolveProviderWithFallback(cfg.GetProvider(), defaultProvider)
}

// BuildRequest builds an HTTP request based on configuration and input
func BuildRequest(cfg ChatRequestConfig, input string) (*http.Request, []byte, error) {
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)

	// Handle embed mode separately
	if cfg.IsEmbed() {
		return buildEmbedRequest(cfg, provider, input)
	}

	// Build typed messages from input
	var messages []TypedMessage

	// Add system message only for providers that carry it inline.
	system := resolvedSystemPrompt(cfg, "")
	if system != "" && !providerUsesOutOfBandSystemPrompt(provider) {
		messages = append(messages, NewTextMessage("system", system))
	}

	// Add user message
	if input != "" {
		messages = append(messages, NewTextMessage("user", input))
	}

	// Prepare options
	opts := ChatBuildOptions{
		Stream: cfg.IsStreamChat(),
	}

	// Add tools if enabled
	if cfg.IsToolEnabled() {
		opts.ToolDefs = GetBuiltinUniversalCommandTool()
	}

	// Use the unified BuildChatRequest
	return BuildChatRequest(cfg, messages, opts)
}

// buildEmbedRequest handles embedding requests for providers that support it
func buildEmbedRequest(cfg EmbedRequestConfig, provider, input string) (*http.Request, []byte, error) {
	spec, err := providerSpecForName(provider)
	if err != nil {
		return nil, nil, err
	}
	buildEmbed, err := spec.requireEmbedBuilder()
	if err != nil {
		return nil, nil, err
	}
	return buildEmbed(cfg, input)
}

// buildArgoEmbedRequest handles Argo embedding requests
func buildArgoEmbedRequest(cfg EmbedRequestConfig, input string) (*http.Request, []byte, error) {
	model := cfg.GetModel()
	if model == "" {
		model = DefaultEmbedModel
	}

	req := map[string]interface{}{
		"user":   cfg.GetUser(),
		"model":  model,
		"prompt": []string{input},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}

	endpoint, err := providers.ResolveEmbedURL(constants.ProviderArgo, "", cfg.GetEnv())
	if err != nil {
		return nil, nil, err
	}
	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), constants.ProviderArgo)
	return httpReq, body, nil
}

// buildOpenAIEmbedRequest handles OpenAI embedding requests
func buildOpenAIEmbedRequest(cfg EmbedRequestConfig, input string) (*http.Request, []byte, error) {
	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel(constants.ProviderOpenAI)
	}

	// OpenAI embedding format
	req := map[string]interface{}{
		"model": model,
		"input": input,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url, err := providers.ResolveEmbedURL(constants.ProviderOpenAI, cfg.GetProviderURL(), "")
	if err != nil {
		return nil, nil, err
	}

	// Use unified request builder
	return buildProviderRequest(cfg, url, body, constants.ProviderOpenAI, false)
}

// ============================================================================
// Response Handling
// ============================================================================

// Response represents a unified response structure from any provider
type Response struct {
	Text      string
	ToolCalls []ToolCall
}

// HandleResponse processes an HTTP response based on configuration.
// For streaming responses, it has dual behavior:
// 1. Prints the streamed content directly to os.Stdout in real-time
// 2. Returns the full accumulated content as a string for session storage
// The response body is closed by this function - callers should not close it.
// Returns: (Response, error)
func HandleResponse(ctx context.Context, cfg ResponseConfig, resp *http.Response, logger Logger, notifier Notifier) (Response, error) {
	defer resp.Body.Close()

	// Get provider, default to argo
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)

	// Validate HTTP status first
	if resp.StatusCode != http.StatusOK {
		// Read limited body for error message
		errorData, err := limitio.ReadLimitedWithKind(resp.Body, constants.MaxErrorResponseSize, "API error response")
		if err != nil {
			errorData = []byte("failed to read error response")
		}
		return Response{}, errors.NewHTTPError(resp.StatusCode, string(errorData))
	}

	var response Response
	var err error

	// Handle streaming responses with provider-specific parsing.
	// Argo request planning already downgrades tool-enabled requests to non-streaming.
	if cfg.IsStreamChat() {
		response, err = handleStreamingResponse(ctx, cfg, resp, provider, logger, notifier)
	} else {
		response, err = handleNonStreamingResponse(cfg, resp, provider, logger, notifier)
	}

	if err != nil {
		return Response{}, err
	}

	return response, nil
}

// handleStreamingResponse handles streaming responses and returns accumulated content
func handleStreamingResponse(ctx context.Context, cfg ResponseConfig, resp *http.Response, provider string, logger Logger, notifier Notifier) (Response, error) {
	spec, err := providerSpecForName(provider)
	if err != nil {
		spec = unknownProviderSpec(provider)
	}
	return spec.handleStreamResponse(ctx, resp.Body, logger, notifier)
}

// handleNonStreamingResponse handles non-streaming responses
func handleNonStreamingResponse(cfg ResponseConfig, resp *http.Response, provider string, logger Logger, notifier Notifier) (Response, error) {
	// Read response body with size limit from constants
	data, err := limitio.ReadLimitedWithKind(resp.Body, constants.DefaultMaxResponseBodySize, "API response body")
	if err != nil {
		return Response{}, fmt.Errorf("failed to read response body: %w", err)
	}

	// Log response
	logPrefix := "chat_output"
	if cfg.IsEmbed() {
		logPrefix = "embed_output"
	}
	if err := logger.LogJSON(logger.GetLogDir(), logPrefix, data); err != nil {
		notifier.Warnf("Failed to log response: %v", err)
	}

	// Parse response based on provider
	spec, err := providerSpecForName(provider)
	if err != nil {
		return Response{}, err
	}
	return spec.parseResponseData(data, cfg.IsEmbed())
}

// parseResponse routes to provider-specific parser
func parseResponse(provider string, data []byte, isEmbed bool) (Response, error) {
	spec, err := providerSpecForName(provider)
	if err != nil {
		return Response{}, err
	}
	return spec.parseResponseData(data, isEmbed)
}

// ConvertedTools represents the result of converting tools for a specific provider
type ConvertedTools struct {
	Tools      interface{}
	ToolChoice interface{}
}

// ConvertToolsForProvider converts tools to the appropriate format based on the model
// Exported for use by proxy converter
// Returns ConvertedTools with typed structures for each provider
func ConvertToolsForProvider(model string, tools []ToolDefinition, toolChoice *ToolChoice) ConvertedTools {
	spec := providerSpecForModel(model)
	return spec.convertToolsForRequest(tools, toolChoice)
}

// ChatBuildOptions contains options for building chat requests
type ChatBuildOptions struct {
	ModelOverride  string           // Override the model from config
	SystemOverride string           // Override the system prompt from config
	ToolDefs       []ToolDefinition // Tool definitions to include
	Stream         bool             // Whether to stream the response
}

// BuildChatRequest is the unified entry point for building chat requests
// It handles all providers and configurations through a single interface
func BuildChatRequest(cfg ChatRequestConfig, typedMessages []TypedMessage, opts ChatBuildOptions) (*http.Request, []byte, error) {
	// Determine provider and model
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)
	model := opts.ModelOverride
	if model == "" {
		model = cfg.GetModel()
		if model == "" {
			model = GetDefaultChatModel(provider)
		}
	}

	// Determine system prompt
	system := resolvedSystemPrompt(cfg, opts.SystemOverride)

	// Validate tool support if tools are requested
	if len(opts.ToolDefs) > 0 {
		if err := ValidateToolSupport(provider, model); err != nil {
			return nil, nil, err
		}
	}

	// Use the existing unified builder
	return buildChatRequestFromTyped(cfg, typedMessages, model, system, opts.ToolDefs, nil, opts.Stream)
}

// BuildToolResultRequest builds a request containing tool execution results
func BuildToolResultRequest(cfg ChatRequestConfig, model string, system string, toolDefs []ToolDefinition, typedMessages []TypedMessage) (*http.Request, []byte, error) {
	// Preserve follow-up request behavior for callers that rely on tool mode in the
	// config rather than passing tool definitions explicitly.
	if len(toolDefs) == 0 && cfg.IsToolEnabled() {
		toolDefs = GetBuiltinUniversalCommandTool()
	}

	// Use the new unified BuildChatRequest
	opts := ChatBuildOptions{
		ModelOverride:  model,
		SystemOverride: system,
		ToolDefs:       toolDefs,
		Stream:         false, // Tool results never stream
	}
	return BuildChatRequest(cfg, typedMessages, opts)
}

// ============================================================================
// Session-related Request Building
// ============================================================================

// BuildRequestWithToolInteractions builds a request using messages that include tool interactions
func BuildRequestWithToolInteractions(ctx context.Context, cfg ChatRequestConfig, sess Session, getMessagesWithTools func(context.Context, string) ([]TypedMessage, error)) (RequestBuild, error) {
	if cfg.IsEmbed() {
		return RequestBuild{}, fmt.Errorf("embed mode does not support sessions")
	}

	// Get messages with tool interactions
	typedMessages, err := getMessagesWithTools(ctx, sess.GetPath())
	if err != nil {
		return RequestBuild{}, fmt.Errorf("failed to get messages with tools: %w", err)
	}

	// Prepare options
	opts := ChatBuildOptions{
		Stream: cfg.IsStreamChat(),
	}

	// Load tool if configured
	if cfg.IsToolEnabled() {
		opts.ToolDefs = GetBuiltinUniversalCommandTool()
	}

	// Use the unified BuildChatRequest
	req, body, err := BuildChatRequest(cfg, typedMessages, opts)
	if err != nil {
		return RequestBuild{}, err
	}

	// Get the actual model used
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)
	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel(provider)
	}

	return RequestBuild{
		Request:  req,
		Body:     body,
		Model:    model,
		ToolDefs: opts.ToolDefs,
	}, nil
}

// ============================================================================
// Streaming Response Handlers
// ============================================================================

// streamParser defines a unified function that processes a line from a stream
// and returns content, tool calls (may be nil), done status, and any error
type streamParser func(line string, state interface{}) (content string, toolCalls []ToolCall, done bool, err error)

// handleGenericStream is a unified stream handler that processes streaming responses
// using a provided parser function that may return tool calls
func handleGenericStream(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer, notifier Notifier, parser streamParser, initialState interface{}, provider string) (string, []ToolCall, error) {
	// Body is closed by HandleResponse, not here

	var accumulated strings.Builder
	var allToolCalls []ToolCall
	scanner := bufio.NewScanner(body)
	// Increase buffer size to handle large SSE lines (default is ~64KB)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024) // 2MB max

	state := initialState
	var sseBuf []string
	var parseErrorCount int

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
		content, toolCalls, done, err := parser("data: "+joined, state)
		if err != nil {
			// Log parsing error to file
			_, _ = fmt.Fprintf(logFile, "# parsing error: %v\n", err)
			parseErrorCount++

			// Emit warning via notifier if we hit the threshold
			if parseErrorCount == constants.DefaultStreamParseErrorThreshold {
				if notifier != nil {
					notifier.Warnf("Multiple streaming parse errors detected (provider: %s). See log file for details.", provider)
				}
			}
			return false, nil
		}

		if content != "" {
			fmt.Fprint(out, content)
			accumulated.WriteString(content)
		}

		// Accumulate tool calls if any
		if len(toolCalls) > 0 {
			allToolCalls = append(allToolCalls, toolCalls...)
		}

		return done, nil
	}

scanLoop:
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return accumulated.String(), allToolCalls, ctx.Err()
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

			// Handle other SSE lines based on type
			switch {
			case strings.HasPrefix(line, "event:"):
				// Provider-specific event handling
				content, toolCalls, done, err := parser(line, state)
				if err != nil {
					// Log parsing error to file
					_, _ = fmt.Fprintf(logFile, "# parsing error: %v\n", err)
					parseErrorCount++

					// Emit warning via notifier if we hit the threshold
					if parseErrorCount == constants.DefaultStreamParseErrorThreshold {
						if notifier != nil {
							notifier.Warnf("Multiple streaming parse errors detected. Check log file for details.")
						}
					}
				}
				if content != "" {
					fmt.Fprint(out, content)
					accumulated.WriteString(content)
				}
				// Accumulate tool calls if any
				if len(toolCalls) > 0 {
					allToolCalls = append(allToolCalls, toolCalls...)
				}
				if done {
					break scanLoop
				}
			default:
				// Log other lines without parsing
				_, _ = logFile.WriteString(line + "\n")
			}
			continue
		}
	}

	// Flush any remaining SSE data
	_, _ = flushSSE()

	if err := scanner.Err(); err != nil {
		return accumulated.String(), allToolCalls, err
	}

	return accumulated.String(), allToolCalls, nil
}

// handleArgoStream handles Argo's plain text streaming format
// Note: Argo doesn't support tool calls in streaming mode
func handleArgoStream(ctx context.Context, body io.ReadCloser, logFile *os.File, out io.Writer) (string, []ToolCall, error) {
	// Body is closed by HandleResponse, not here

	var accumulated strings.Builder
	buffer := make([]byte, 4096) // 4KB chunks for real-time streaming

	for {
		select {
		case <-ctx.Done():
			return accumulated.String(), nil, ctx.Err()
		default:
			n, err := body.Read(buffer)
			if n > 0 {
				chunk := string(buffer[:n])
				fmt.Fprint(out, chunk)
				accumulated.WriteString(chunk)
				_, _ = logFile.WriteString(chunk)
			}
			if err == io.EOF {
				return accumulated.String(), nil, nil
			}
			if err != nil {
				return accumulated.String(), nil, err
			}
		}
	}
}
