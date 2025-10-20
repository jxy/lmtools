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

// ============================================================================
// Request Building
// ============================================================================

// getProviderWithDefault returns the provider from config or a default value
func getProviderWithDefault(cfg RequestConfig, defaultProvider string) string {
	provider := cfg.GetProvider()
	if provider == "" {
		return defaultProvider
	}
	return provider
}

// BuildRequest builds an HTTP request based on configuration and input
func BuildRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)

	// Handle embed mode separately
	if cfg.IsEmbed() {
		return buildEmbedRequest(cfg, provider, input)
	}

	// Build typed messages from input
	var messages []TypedMessage

	// Add system message if configured (except for Google and Anthropic which handle it separately)
	effectiveSystem := cfg.GetEffectiveSystem()
	if effectiveSystem != "" && provider != constants.ProviderGoogle && provider != constants.ProviderAnthropic {
		messages = append(messages, NewTextMessage("system", effectiveSystem))
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
func buildEmbedRequest(cfg RequestConfig, provider, input string) (*http.Request, []byte, error) {
	switch provider {
	case constants.ProviderArgo:
		return buildArgoEmbedRequest(cfg, input)
	case constants.ProviderOpenAI:
		return buildOpenAIEmbedRequest(cfg, input)
	default:
		return nil, nil, fmt.Errorf("%s provider does not support embedding mode", provider)
	}
}

// buildArgoEmbedRequest handles Argo embedding requests
func buildArgoEmbedRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
	urlBase := getBaseURL(cfg.GetEnv())
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

	endpoint := fmt.Sprintf("%s/embed/", urlBase)
	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	auth.SetRequestHeaders(httpReq, true, cfg.IsStreamChat(), constants.ProviderArgo)
	return httpReq, body, nil
}

// buildArgoChatRequest builds an Argo chat request for both single and session modes
// buildArgoChatRequestTyped builds an Argo chat request from typed messages
// This is the unified function that both buildArgoChatRequest and buildArgoChatRequestWithTools use
func buildArgoChatRequestTyped(cfg RequestConfig, messages []TypedMessage, stream bool) (*http.Request, []byte, error) {
	urlBase := getBaseURL(cfg.GetEnv())
	model := cfg.GetModel()
	if model == "" {
		model = GetDefaultChatModel(constants.ProviderArgo)
	}

	// Check if first message is already a system message
	hasSystemMessage := len(messages) > 0 && messages[0].Role == string(RoleSystem)

	// Add system message if needed
	finalMessages := messages
	effectiveSystem := cfg.GetEffectiveSystem()
	if !hasSystemMessage && effectiveSystem != "" {
		// Prepend system message
		systemMsg := NewTextMessage("system", effectiveSystem)
		finalMessages = append([]TypedMessage{systemMsg}, messages...)
	}

	// Determine model provider for Argo routing
	provider := DetermineArgoModelProvider(model)

	// Convert TypedMessage to appropriate format based on model
	var argoMessages []interface{}
	switch provider {
	case constants.ProviderAnthropic:
		// Use typed conversion with helper function
		typedMessages := ToAnthropicTyped(finalMessages)
		argoMessages = MarshalAnthropicMessagesForRequest(typedMessages)
	case constants.ProviderOpenAI:
		// Use typed conversion with helper function
		typedMessages := ToOpenAITyped(finalMessages)
		argoMessages = MarshalOpenAIMessagesForRequest(typedMessages)
	case constants.ProviderGoogle:
		// Use typed conversion with helper function
		typedMessages := ToGoogleForArgoTyped(finalMessages)
		argoMessages = MarshalGoogleMessagesForRequest(typedMessages)
	default:
		// Default to OpenAI format with helper function
		typedMessages := ToOpenAITyped(finalMessages)
		argoMessages = MarshalOpenAIMessagesForRequest(typedMessages)
	}

	// Create request with appropriate format
	req := map[string]interface{}{
		"user":     cfg.GetUser(),
		"model":    model,
		"messages": argoMessages,
	}

	// Add tool if configured
	if cfg.IsToolEnabled() {
		toolDefs := GetBuiltinUniversalCommandTool()
		// Convert tools to appropriate format based on model
		converted := ConvertToolsForProvider(model, toolDefs, nil)
		req["tools"] = converted.Tools
		req["tool_choice"] = converted.ToolChoice
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	// For Argo, check if we should use streaming
	// When tools are configured, Argo doesn't support streaming
	actualStream := stream
	if stream && cfg.IsToolEnabled() {
		actualStream = false
		// Argo doesn't support streaming with tools
	}

	endpoint := fmt.Sprintf("%s/chat/", urlBase)
	if actualStream {
		// Only use streaming endpoint if no tools are configured
		endpoint = fmt.Sprintf("%s/streamchat/", urlBase)
	}

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	auth.SetRequestHeaders(httpReq, true, actualStream, constants.ProviderArgo)
	return httpReq, body, nil
}

// buildOpenAIEmbedRequest handles OpenAI embedding requests
func buildOpenAIEmbedRequest(cfg RequestConfig, input string) (*http.Request, []byte, error) {
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

	// Determine endpoint
	url := cfg.GetProviderURL()
	if url == "" {
		url = "https://api.openai.com/v1"
	}
	url = strings.TrimRight(url, "/") + "/embeddings"

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
func HandleResponse(ctx context.Context, cfg RequestConfig, resp *http.Response, logger Logger, notifier Notifier) (Response, error) {
	defer resp.Body.Close()

	// Get provider, default to argo
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)

	// Validate HTTP status first
	if resp.StatusCode != http.StatusOK {
		// Read limited body for error message
		limitedBody := io.LimitReader(resp.Body, 1024) // 1KB limit
		errorData, err := io.ReadAll(limitedBody)
		if err != nil {
			errorData = []byte("failed to read error response")
		}
		return Response{}, errors.NewHTTPError(resp.StatusCode, string(errorData))
	}

	var response Response
	var err error

	// Handle streaming responses with provider-specific parsing
	// Note: buildArgoChatRequestWithTools already handles disabling streaming for Argo with tools
	// by using the non-streaming endpoint, so we don't need to check that here
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
func handleStreamingResponse(ctx context.Context, cfg RequestConfig, resp *http.Response, provider string, logger Logger, notifier Notifier) (Response, error) {
	f, path, err := logger.CreateLogFile(logger.GetLogDir(), "stream_chat_output")
	if err != nil {
		return Response{}, fmt.Errorf("failed to create log file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			notifier.Warnf("Failed to close log file %s: %v", path, closeErr)
		}
	}()

	// Stream and parse based on provider
	switch provider {
	case constants.ProviderArgo:
		// Argo doesn't support tools with streaming
		text, toolCalls, err := handleArgoStream(ctx, resp.Body, f, os.Stdout)
		return Response{Text: text, ToolCalls: toolCalls}, err
	case constants.ProviderOpenAI:
		text, toolCalls, err := handleOpenAIStreamWithTools(ctx, resp.Body, f, os.Stdout, notifier)
		return Response{Text: text, ToolCalls: toolCalls}, err
	case constants.ProviderAnthropic:
		text, toolCalls, err := handleAnthropicStreamWithTools(ctx, resp.Body, f, os.Stdout, notifier)
		return Response{Text: text, ToolCalls: toolCalls}, err
	case constants.ProviderGoogle:
		text, toolCalls, err := handleGoogleStreamWithTools(ctx, resp.Body, f, os.Stdout, notifier)
		return Response{Text: text, ToolCalls: toolCalls}, err
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
			return Response{Text: buf.String()}, fmt.Errorf("streaming interrupted: %w", ctx.Err())
		case err := <-done:
			if err != nil {
				return Response{Text: buf.String()}, fmt.Errorf("error streaming response: %w", err)
			}
			return Response{Text: buf.String()}, nil
		}
	}
}

// handleNonStreamingResponse handles non-streaming responses
func handleNonStreamingResponse(cfg RequestConfig, resp *http.Response, provider string, logger Logger, notifier Notifier) (Response, error) {
	// Read response body
	data, err := io.ReadAll(resp.Body)
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
	return parseResponse(provider, data, cfg.IsEmbed())
}

// parseResponse routes to provider-specific parser
func parseResponse(provider string, data []byte, isEmbed bool) (Response, error) {
	switch provider {
	case constants.ProviderArgo:
		text, toolCalls, err := parseArgoResponseWithTools(data, isEmbed)
		return Response{Text: text, ToolCalls: toolCalls}, err
	case constants.ProviderOpenAI:
		text, toolCalls, err := parseOpenAIResponseWithTools(data, isEmbed)
		return Response{Text: text, ToolCalls: toolCalls}, err
	case constants.ProviderGoogle:
		text, toolCalls, err := parseGoogleResponseWithTools(data, isEmbed)
		return Response{Text: text, ToolCalls: toolCalls}, err
	case constants.ProviderAnthropic:
		text, toolCalls, err := parseAnthropicResponseWithTools(data, isEmbed)
		return Response{Text: text, ToolCalls: toolCalls}, err
	default:
		return Response{}, fmt.Errorf("unsupported provider: %s", provider)
	}
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
	if len(tools) == 0 {
		return ConvertedTools{}
	}

	provider := DetermineArgoModelProvider(model)

	switch provider {
	case constants.ProviderOpenAI:
		// Use typed OpenAI format
		openAITools := ConvertToolsToOpenAITyped(tools)
		// Convert tool choice
		if toolChoice != nil {
			if toolChoice.Type == "tool" && toolChoice.Name != "" {
				// Use typed structure for specific function choice
				return ConvertedTools{
					Tools: openAITools,
					ToolChoice: OpenAIToolChoice{
						Type: "function",
						Function: &OpenAIToolChoiceFunction{
							Name: toolChoice.Name,
						},
					},
				}
			}
			// For auto/none/required, return plain string (OpenAI API requirement)
			return ConvertedTools{
				Tools:      openAITools,
				ToolChoice: toolChoice.Type, // String type like "auto", "none", etc.
			}
		}
		// Default to string "auto" (OpenAI API expects plain string)
		return ConvertedTools{Tools: openAITools, ToolChoice: "auto"}

	case constants.ProviderGoogle:
		// Use typed Google format
		googleTools := ConvertToolsToGoogleTyped(tools)
		return ConvertedTools{Tools: googleTools, ToolChoice: nil} // Google doesn't use tool_choice

	case constants.ProviderAnthropic:
		// Use typed Anthropic format
		anthropicTools := ConvertToolsToAnthropicTyped(tools)
		// Convert tool choice
		if toolChoice != nil {
			return ConvertedTools{
				Tools: anthropicTools,
				ToolChoice: AnthropicToolChoice{
					Type: toolChoice.Type,
					Name: toolChoice.Name,
				},
			}
		}
		return ConvertedTools{
			Tools: anthropicTools,
			ToolChoice: AnthropicToolChoice{
				Type: "auto",
			},
		}

	default:
		// Default to typed OpenAI format
		openAITools := ConvertToolsToOpenAITyped(tools)
		if toolChoice != nil {
			if toolChoice.Type == "tool" && toolChoice.Name != "" {
				return ConvertedTools{
					Tools: openAITools,
					ToolChoice: OpenAIToolChoice{
						Type: "function",
						Function: &OpenAIToolChoiceFunction{
							Name: toolChoice.Name,
						},
					},
				}
			}
			return ConvertedTools{
				Tools:      openAITools,
				ToolChoice: toolChoice.Type,
			}
		}
		return ConvertedTools{Tools: openAITools, ToolChoice: "auto"}
	}
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
func BuildChatRequest(cfg RequestConfig, typedMessages []TypedMessage, opts ChatBuildOptions) (*http.Request, []byte, error) {
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
	system := opts.SystemOverride
	if system == "" {
		system = cfg.GetEffectiveSystem()
	}

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
func BuildToolResultRequest(cfg RequestConfig, model string, system string, toolDefs []ToolDefinition, typedMessages []TypedMessage) (*http.Request, []byte, error) {
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
func BuildRequestWithToolInteractions(ctx context.Context, cfg RequestConfig, sess Session, getMessagesWithTools func(context.Context, string) ([]TypedMessage, error)) (RequestBuild, error) {
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
