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
	ArgoTestURL = providers.ArgoTestURL
)

// ============================================================================
// Request Building
// ============================================================================

// getProviderWithDefault returns the provider from config or a default value
func getProviderWithDefault(cfg RequestOptions, defaultProvider string) string {
	return providers.ResolveProviderWithFallback(cfg.Provider, defaultProvider)
}

// BuildRequest builds an HTTP request based on configuration and input
func BuildRequest(cfg RequestOptions, input string) (*http.Request, []byte, error) {
	if err := ValidateDeveloperTurn(cfg, input, false); err != nil {
		return nil, nil, err
	}
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)

	// Handle embed mode separately
	if cfg.Embed {
		return buildEmbedRequest(cfg, provider, input)
	}

	// Build typed messages from input
	var messages []TypedMessage

	// Add system message only for providers that carry it inline.
	system := resolvedSystemPrompt(cfg, "")
	if system != "" && !providerUsesOutOfBandSystemPrompt(provider) {
		messages = append(messages, NewTextMessage("system", system))
	}
	if cfg.Developer != "" {
		messages = append(messages, NewTextMessage(string(RoleDeveloper), cfg.Developer))
	}

	// Add user message: the prompt and any -image attachments.
	if input != "" || len(cfg.Images) > 0 {
		messages = append(messages, NewUserMessage(input, cfg.Images))
	}

	// Prepare options
	opts := ChatBuildOptions{
		Stream: cfg.StreamChat,
	}

	// Add tools if enabled
	if cfg.ToolEnabled {
		opts.ToolDefs = AdvertisedTools(cfg)
	}

	// Use the unified BuildChatRequest
	return BuildChatRequest(cfg, messages, opts)
}

// buildEmbedRequest handles embedding requests for providers that support it
func buildEmbedRequest(cfg RequestOptions, provider, input string) (*http.Request, []byte, error) {
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
func buildArgoEmbedRequest(cfg RequestOptions, input string) (*http.Request, []byte, error) {
	model := cfg.Model
	if model == "" {
		model = DefaultEmbedModel
	}

	req := map[string]interface{}{
		"user":   cfg.User,
		"model":  model,
		"prompt": []string{input},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}

	endpoint, err := providers.ResolveEmbedURL(constants.ProviderArgo, cfg.ProviderURL, cfg.Env)
	if err != nil {
		return nil, nil, err
	}
	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	auth.SetRequestHeaders(httpReq, true, cfg.StreamChat)
	return httpReq, body, nil
}

// buildOpenAIEmbedRequest handles OpenAI embedding requests
func buildOpenAIEmbedRequest(cfg RequestOptions, input string) (*http.Request, []byte, error) {
	model := cfg.Model
	if model == "" {
		model = providers.DefaultChatModel(constants.ProviderOpenAI)
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

	url, err := providers.ResolveEmbedURL(constants.ProviderOpenAI, cfg.ProviderURL, "")
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
	Text             string
	ToolCalls        []ToolCall
	Blocks           []Block
	ThoughtSignature string
	Streamed         bool
	// IncompleteReason names why the provider stopped before completing the
	// response, such as max_output_tokens or content_filter. Partial content
	// remains available for presentation and persistence.
	IncompleteReason string
	// CompletionError explains an invalid stream whose partial content is retained.
	CompletionError error
	// Usage carries the provider's token counts, when reported. The CLI
	// displays them and its optional context-sizing probe uses the input
	// count. Session stores keep the exchange separately from these counts.
	Usage *Usage
	// Model is the model the provider says answered, which an alias such as
	// claudeopus55 resolves to; empty when it said nothing. Display-only.
	Model string
}

// ResponseParseOptions controls provider-specific compatibility parsing and
// optional response presentation.
type ResponseParseOptions struct {
	ArgoLegacy bool
	ToolDefs   []ToolDefinition
	Output     ResponseOutput
}

// HandleResponse processes an HTTP response based on configuration.
// For streaming responses, it has dual behavior:
// 1. Emits semantic output events in real time (answer text defaults to os.Stdout)
// 2. Returns the full accumulated content as a string for session storage
// The response body is closed by this function - callers should not close it.
// Returns: (Response, error)
func HandleResponse(ctx context.Context, cfg RequestOptions, resp *http.Response, logger Logger, notifier Notifier) (Response, error) {
	return HandleResponseWithOptions(ctx, cfg, resp, logger, notifier, ResponseParseOptions{})
}

// HandleResponseWithOptions processes an HTTP response with compatibility parse options.
func HandleResponseWithOptions(ctx context.Context, cfg RequestOptions, resp *http.Response, logger Logger, notifier Notifier, opts ResponseParseOptions) (Response, error) {
	defer resp.Body.Close()
	LogWireReads(resp, logger)

	// Get provider, default to argo
	provider := effectiveResponseProvider(cfg)

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

	// Handle streaming responses with provider-specific parsing. Legacy Argo may
	// downgrade tool-enabled requests to non-streaming during request planning.
	streamed := shouldHandleStreamingResponse(cfg, provider, resp)
	output := opts.Output
	if streamed {
		output = responseOutputOrDefault(output, os.Stdout)
		response, err = handleStreamingResponse(ctx, cfg, resp, provider, logger, notifier, output)
	} else {
		response, err = handleNonStreamingResponse(cfg, resp, provider, logger, notifier, opts)
	}

	if err != nil {
		switch {
		case response.IncompleteReason == "invalid_stream":
			// Save and present partial content through the same path as token
			// exhaustion. ValidateResponse reports the decoding error before
			// any tools execute; a completed marker cannot clear it.
			response.CompletionError = err
		case ctx.Err() != nil && strings.TrimSpace(response.Text) != "":
			// The operator stopped an answer that had begun. What they read
			// is kept, so the next turn can refer to it; see cancelledResponse.
			response = cancelledResponse(response, err)
		default:
			return Response{}, err
		}
	}

	response.Streamed = streamed
	response = prepareResponseCompletion(cfg, response)
	AnnotateMCPToolCalls(response.ToolCalls, opts.ToolDefs)
	if output != nil {
		output.HandleResponseComplete(response)
	}
	notifyTokenUsage(notifier, response.Usage, len(response.ToolCalls) > 0)
	return response, nil
}

// cancelledResponse keeps the answer text a cancelled stream had delivered,
// and nothing else: reasoning without its signature cannot be replayed, a
// tool call cut off mid-arguments cannot run, and the token counts describe a
// response that never finished. The text becomes one block so the session's
// blocks and its text agree.
func cancelledResponse(response Response, err error) Response {
	return Response{
		Text:             response.Text,
		Blocks:           []Block{TextBlock{Text: response.Text}},
		IncompleteReason: "cancelled",
		CompletionError:  err,
	}
}

// EffectiveChatModel resolves the model a chat request is actually built with:
// the configured model, or the provider default when -model was not given.
// Response parsing sees only cfg, so it must resolve the model exactly the way
// BuildChatRequest does; otherwise an Argo request built for the default gpt5
// model would be parsed as if no model had been chosen.
func EffectiveChatModel(cfg RequestOptions) string {
	if cfg.Model != "" {
		return cfg.Model
	}
	return providers.DefaultChatModel(getProviderWithDefault(cfg, constants.ProviderArgo))
}

// usesOpenAIResponsesWire reports whether the request identified by cfg and model
// is rendered and parsed as OpenAI Responses. Request building and response parsing
// must agree, so both consult this one predicate: for Argo only gpt* models use the
// Responses endpoint, while other Argo models still route by model prefix.
func usesOpenAIResponsesWire(cfg RequestOptions, model string) bool {
	if cfg.Embed {
		return false
	}
	return providers.UsesResponsesAPIWire(getProviderWithDefault(cfg, constants.ProviderArgo), model, cfg.OpenAIResponses, cfg.ArgoLegacy)
}

func effectiveResponseProvider(cfg RequestOptions) string {
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)
	if provider != constants.ProviderArgo || cfg.Embed {
		return provider
	}
	if cfg.ArgoLegacy {
		return constants.ProviderArgo
	}

	return providers.DetermineArgoModelProvider(EffectiveChatModel(cfg))
}

func shouldHandleStreamingResponse(cfg RequestOptions, provider string, resp *http.Response) bool {
	if !cfg.StreamChat {
		return false
	}
	if provider != constants.ProviderArgo || !cfg.ArgoLegacy {
		return true
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(contentType, "text/plain") || strings.Contains(contentType, "text/event-stream")
}

// handleStreamingResponse handles streaming responses and returns accumulated content
func handleStreamingResponse(ctx context.Context, cfg RequestOptions, resp *http.Response, provider string, logger Logger, notifier Notifier, output ResponseOutput) (Response, error) {
	if usesOpenAIResponsesWire(cfg, EffectiveChatModel(cfg)) {
		f, path, err := logger.CreateLogFile(logger.GetLogDir(), "stream_chat_output")
		if err != nil {
			return Response{}, fmt.Errorf("failed to create log file: %w", err)
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil {
				notifier.Warnf("Failed to close log file %s: %v", path, closeErr)
			}
		}()
		state := NewOpenAIResponsesStreamState()
		text, toolCalls, err := RunStream(ctx, resp.Body, f, output, notifier, state, constants.ProviderOpenAI)
		blocks := state.Blocks()
		if len(blocks) == 0 {
			blocks = responseBlocksFromParts(text, toolCalls, "")
		}
		return Response{Text: text, ToolCalls: toolCalls, Blocks: blocks, Usage: state.Usage(), IncompleteReason: state.IncompleteReason(), Model: state.model}, err
	}

	spec, err := providerSpecForName(provider)
	if err != nil {
		spec = unknownProviderSpec(provider)
	}
	return spec.handleStreamResponse(ctx, resp.Body, logger, notifier, output)
}

// LogWireReads makes each read of resp's body log what it read, as it is
// read, when logger is at DEBUG. The log then holds the body as it came: a
// stream with every event, comment, and line ending, in the order and the
// pieces it arrived in, and a body whose read failed or ran over its limit
// as far as it was read, though limitio.ReadLimited then returns none of
// it. The stream_chat_output file holds what the stream parser made of a
// stream.
func LogWireReads(resp *http.Response, logger Logger) {
	if logger != nil && logger.IsDebugEnabled() {
		resp.Body = wireLoggedBody{ReadCloser: resp.Body, logger: logger}
	}
}

// wireLoggedBody is a body LogWireReads logs the reads of.
type wireLoggedBody struct {
	io.ReadCloser
	logger Logger
}

func (b wireLoggedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.logger.Debugf("WIRE BACKEND RESPONSE BODY:\n%s", p[:n])
	}
	return n, err
}

// handleNonStreamingResponse handles non-streaming responses
func handleNonStreamingResponse(cfg RequestOptions, resp *http.Response, provider string, logger Logger, notifier Notifier, opts ResponseParseOptions) (Response, error) {
	// Read response body with size limit from constants
	data, err := limitio.ReadLimitedWithKind(resp.Body, constants.DefaultMaxResponseBodySize, "API response body")
	if err != nil {
		return Response{}, fmt.Errorf("failed to read response body: %w", err)
	}

	// Log response
	logPrefix := "chat_output"
	if cfg.Embed {
		logPrefix = "embed_output"
	}
	if err := logger.LogJSON(logger.GetLogDir(), logPrefix, data); err != nil {
		notifier.Warnf("Failed to log response: %v", err)
	}

	// Parse response based on provider. Token counts are lifted from the same
	// bytes here, the one point every non-streaming provider body passes
	// through, rather than in each provider parser.
	attachUsage := func(response Response, err error) (Response, error) {
		if err != nil {
			return response, err
		}
		response.Usage = UsageFromPayload(data)
		response.Model = ModelFromPayload(data)
		return response, nil
	}
	spec, err := providerSpecForName(provider)
	if err != nil {
		return Response{}, err
	}
	if provider == constants.ProviderArgo && opts.ArgoLegacy {
		text, toolCalls, err := parseArgoResponseWithToolsOptions(data, cfg.Embed, ArgoResponseParseOptions{
			ExtractEmbeddedTools: true,
			ToolDefs:             opts.ToolDefs,
		})
		return attachUsage(Response{Text: text, ToolCalls: toolCalls}, err)
	}
	if usesOpenAIResponsesWire(cfg, EffectiveChatModel(cfg)) {
		return attachUsage(parseOpenAIResponses(data, cfg.Embed))
	}
	return attachUsage(spec.parseResponseData(data, cfg.Embed))
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
	ModelOverride     string           // Override the model from config
	SystemOverride    string           // Override the system prompt from config
	SystemOverrideSet bool             // Whether SystemOverride was intentionally set, including empty
	ToolDefs          []ToolDefinition // Tool definitions to include
	Stream            bool             // Whether to stream the response
}

// BuildChatRequest is the unified entry point for building chat requests
// It handles all providers and configurations through a single interface
func BuildChatRequest(cfg RequestOptions, typedMessages []TypedMessage, opts ChatBuildOptions) (*http.Request, []byte, error) {
	// Determine the model
	model := opts.ModelOverride
	if model == "" {
		model = EffectiveChatModel(cfg)
	}

	// Determine system prompt
	system, systemExplicit := resolvedBuildSystemPrompt(cfg, opts.SystemOverride, opts.SystemOverrideSet)

	// Use the existing unified builder
	return buildChatRequestFromTyped(cfg, typedMessages, model, system, systemExplicit, opts.ToolDefs, nil, opts.Stream)
}

// BuildToolResultRequest builds a request containing tool execution results
func BuildToolResultRequest(cfg RequestOptions, model string, system string, toolDefs []ToolDefinition, typedMessages []TypedMessage) (*http.Request, []byte, error) {
	// Preserve follow-up request behavior for callers that rely on tool mode in the
	// config rather than passing tool definitions explicitly.
	if len(toolDefs) == 0 && cfg.ToolEnabled {
		toolDefs = GetBuiltinTools(cfg)
	}

	// Use the new unified BuildChatRequest
	opts := ChatBuildOptions{
		ModelOverride:     model,
		SystemOverride:    system,
		SystemOverrideSet: system != "",
		ToolDefs:          toolDefs,
		Stream:            cfg.StreamChat,
	}
	return BuildChatRequest(cfg, typedMessages, opts)
}

// ============================================================================
// Streaming Response Handlers
// ============================================================================

// RunStream processes streaming responses
// using a provider-specific stream state that may return tool calls.
func RunStream(ctx context.Context, body io.ReadCloser, logFile *os.File, output ResponseOutput, notifier Notifier, state StreamState, provider string) (string, []ToolCall, error) {
	// Body is closed by HandleResponse, not here
	output = responseOutputOrDefault(output, os.Stdout)

	var accumulated strings.Builder
	var allToolCalls []ToolCall
	scanner := bufio.NewScanner(body)
	// Increase buffer size to handle large SSE lines (default is ~64KB)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024) // 2MB max

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
		content, toolCalls, done, err := state.ParseLine("data: " + joined)
		if err != nil {
			// Log parsing error to file
			_, _ = fmt.Fprintf(logFile, "# parsing error: %v\n", err)
			if isFatalStreamError(err) {
				return false, err
			}
			parseErrorCount++

			// Emit warning via notifier if we hit the threshold
			if parseErrorCount == constants.DefaultStreamParseErrorThreshold {
				if notifier != nil {
					notifier.Warnf("Multiple streaming parse errors detected (provider: %s). See log file for details.", provider)
				}
			}
			return false, nil
		}

		emittedText := emitPendingResponseStreamEvents(state, output)

		if content != "" {
			if !emittedText {
				output.HandleStreamEvent(ResponseStreamEvent{Type: ResponseStreamTextDelta, Text: content})
			}
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
				done, err := flushSSE()
				if err != nil {
					return accumulated.String(), allToolCalls, err
				}
				if done {
					break scanLoop
				}
				continue
			}

			if strings.HasPrefix(line, ":") {
				// SSE comment line - log but ignore
				_, _ = logFile.WriteString(line + "\n")
				continue
			}

			if data, ok := streamFieldValue(line, "data"); ok {
				sseBuf = append(sseBuf, data)
				continue
			}

			// Handle other SSE lines based on type
			switch {
			case strings.HasPrefix(line, "event:"):
				// Provider-specific event handling
				content, toolCalls, done, err := state.ParseLine(line)
				if err != nil {
					// Log parsing error to file
					_, _ = fmt.Fprintf(logFile, "# parsing error: %v\n", err)
					if isFatalStreamError(err) {
						return accumulated.String(), allToolCalls, err
					}
					parseErrorCount++

					// Emit warning via notifier if we hit the threshold
					if parseErrorCount == constants.DefaultStreamParseErrorThreshold {
						if notifier != nil {
							notifier.Warnf("Multiple streaming parse errors detected. Check log file for details.")
						}
					}
				}
				emittedText := emitPendingResponseStreamEvents(state, output)
				if content != "" {
					if !emittedText {
						output.HandleStreamEvent(ResponseStreamEvent{Type: ResponseStreamTextDelta, Text: content})
					}
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
	if _, err := flushSSE(); err != nil {
		return accumulated.String(), allToolCalls, err
	}

	if err := scanner.Err(); err != nil {
		return accumulated.String(), allToolCalls, streamReadError(ctx, logFile, err)
	}

	return accumulated.String(), allToolCalls, nil
}

// streamReadError names a failure to read a response stream, such as a
// connection that closed partway, and notes it in the stream's log file,
// which otherwise ends without a word about why. A cancelled read is
// returned as it is.
func streamReadError(ctx context.Context, logFile io.Writer, err error) error {
	if ctx.Err() != nil {
		return err
	}
	_, _ = fmt.Fprintf(logFile, "# read error: %v\n", err)
	return fmt.Errorf("read response stream: %w", err)
}

// handleArgoStream handles Argo's plain text streaming format
// Note: Argo doesn't support tool calls in streaming mode
func handleArgoStream(ctx context.Context, body io.ReadCloser, logFile *os.File, output ResponseOutput) (Response, error) {
	// Body is closed by HandleResponse, not here
	output = responseOutputOrDefault(output, os.Stdout)

	var accumulated strings.Builder
	buffer := make([]byte, 4096) // 4KB chunks for real-time streaming

	for {
		select {
		case <-ctx.Done():
			return Response{Text: accumulated.String()}, ctx.Err()
		default:
			n, err := body.Read(buffer)
			if n > 0 {
				chunk := string(buffer[:n])
				output.HandleStreamEvent(ResponseStreamEvent{Type: ResponseStreamTextDelta, Text: chunk})
				accumulated.WriteString(chunk)
				_, _ = logFile.WriteString(chunk)
			}
			if err == io.EOF {
				return Response{Text: accumulated.String()}, nil
			}
			if err != nil {
				return Response{Text: accumulated.String()}, streamReadError(ctx, logFile, err)
			}
		}
	}
}
