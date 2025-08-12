package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"strings"
	"time"
)

const (
	// minPingInterval is the minimum allowed ping interval to prevent CPU spinning
	minPingInterval = 100 * time.Millisecond
	// maxPingInterval is the maximum allowed ping interval to prevent timeouts
	maxPingInterval = 60 * time.Second

	// Streaming configuration
	defaultTextChunkSize = 20 // Default chunk size for text streaming
	defaultJSONChunkSize = 15 // Default chunk size for JSON streaming
	defaultPingInterval  = 15 * time.Second
)

// Server represents the API proxy server
type Server struct {
	config    *Config
	mapper    *ModelMapper
	converter *Converter
	client    *retry.Client
}

// readResponseBody safely reads a response body with size limit
func (s *Server) readResponseBody(resp *http.Response) ([]byte, error) {
	maxSize := s.config.MaxResponseBodySize
	if maxSize <= 0 {
		maxSize = 50 * 1024 * 1024 // Default 50MB
	}

	limitedReader := io.LimitReader(resp.Body, maxSize+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	if int64(len(body)) > maxSize {
		return nil, fmt.Errorf("response body too large: exceeds %d bytes", maxSize)
	}

	return body, nil
}

// extractRequestLogger is a helper function to extract the request logger from context
// and adapt it to the retry.Logger interface
func extractRequestLogger(ctx context.Context) retry.Logger {
	if logger := GetRequestLogger(ctx); logger != nil {
		return logger
	}
	return nil
}

// NewServer creates a new API proxy server
func NewServer(config *Config) http.Handler {
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClientWithOptions(10*time.Minute, 0, logger.GetLogger(), extractRequestLogger),
	}

	// Wrap with consolidated middleware
	handler := http.Handler(server)
	handler = NewProxyMiddleware(handler, config)

	return handler
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Route requests
	switch r.URL.Path {
	case "/":
		s.handleRoot(w, r)
	case "/v1/messages":
		s.handleMessages(w, r)
	case "/v1/messages/count_tokens":
		s.handleCountTokens(w, r)
	default:
		reqLogger := GetRequestLogger(r.Context())
		reqLogger.Warnf("%s %s | Path not found", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

// handleRoot handles the root endpoint
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	// Get request logger from context
	reqLogger := GetRequestLogger(r.Context())

	// Log at INFO level
	reqLogger.Infof("%s %s | Root endpoint accessed", r.Method, r.URL.Path)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "Anthropic API Proxy for OpenAI, Google, and Argo",
	})
}

// handleMessages handles the /v1/messages endpoint
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	// Get request logger from context
	reqLogger := GetRequestLogger(r.Context())

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	// First read the body to detect unknown fields
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		apiErr := NewAPIError(ErrTypePayloadSize, "handleMessages", "Failed to read request body", err)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Parse the JSON
	var anthReq AnthropicRequest
	if err := json.Unmarshal(bodyBytes, &anthReq); err != nil {
		// Check if the error is due to request body size limit
		if err.Error() == "http: request body too large" {
			apiErr := NewAPIError(ErrTypePayloadSize, "handleMessages", "Request body too large", err).
				WithDetails("max_size_mb", s.config.MaxRequestBodySize/1024/1024)
			s.sendAPIError(r.Context(), w, apiErr)
			return
		}
		apiErr := NewAPIError(ErrTypeValidation, "handleMessages", "Invalid request body", err)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Log unknown fields if debug logging is enabled
	logUnknownFields(r.Context(), bodyBytes, anthReq, "Anthropic request")

	// Log incoming request
	reqLogger.LogJSON("Incoming Anthropic Request", anthReq)

	// Validate message array is not empty
	if len(anthReq.Messages) == 0 {
		apiErr := NewAPIError(ErrTypeValidation, "handleMessages", "Messages array cannot be empty", nil).
			WithDetails("field", "messages")
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Store original model for response
	originalModel := anthReq.Model

	// Map the model to appropriate provider
	provider, mappedModel := s.mapper.MapModel(anthReq.Model)
	if provider == "" || mappedModel == "" {
		apiErr := NewAPIError(ErrTypeValidation, "handleMessages", "No API key configured for model", nil).
			WithDetails("model", originalModel)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}
	anthReq.Model = mappedModel

	reqLogger.Debugf("Model mapping: %s -> %s/%s", originalModel, provider, mappedModel)

	// Handle streaming if requested
	if anthReq.Stream {
		s.handleStreamingRequest(w, r, &anthReq, provider, originalModel, mappedModel)
		return
	}

	// Handle non-streaming request
	s.handleNonStreamingRequest(w, r, &anthReq, provider, originalModel, mappedModel)
}

// handleNonStreamingRequest handles non-streaming message requests
func (s *Server) handleNonStreamingRequest(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, provider, originalModel, mappedModel string) {
	reqLogger := GetRequestLogger(r.Context())

	var response interface{}
	var err error

	switch provider {
	case "openai":
		response, err = s.forwardToOpenAI(r.Context(), anthReq)
	case "gemini":
		response, err = s.forwardToGemini(r.Context(), anthReq)
	case "anthropic":
		response, err = s.forwardToAnthropic(r.Context(), anthReq)
	case "argo":
		response, err = s.forwardToArgo(r.Context(), anthReq)
	default:
		apiErr := NewAPIError(ErrTypeValidation, "handleMessages", fmt.Sprintf("Unknown provider: %s", provider), nil)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	if err != nil {
		// Check if error is due to context cancellation
		if r.Context().Err() != nil {
			reqLogger.Debugf("Request cancelled by client")
			// Don't send error response if client disconnected
			return
		}
		apiErr := NewAPIError(ErrTypeServer, "handleNonStreamingRequest", fmt.Sprintf("Provider error: %v", err), err)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Convert response back to Anthropic format
	var anthResp *AnthropicResponse
	switch resp := response.(type) {
	case *OpenAIResponse:
		anthResp = s.converter.ConvertOpenAIToAnthropic(resp, originalModel)
	case *GeminiResponse:
		anthResp = s.converter.ConvertGeminiToAnthropic(resp, originalModel)
	case *ArgoChatResponse:
		anthResp = s.converter.ConvertArgoToAnthropicWithRequest(resp, originalModel, anthReq)
	case *AnthropicResponse:
		// Already in Anthropic format
		anthResp = resp
	default:
		apiErr := NewAPIError(ErrTypeServer, "handleNonStreamingRequest", "Invalid response type from provider", nil)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Log tool calls at INFO level if present
	if anthResp != nil && anthResp.Content != nil {
		for _, block := range anthResp.Content {
			if block.Type == "tool_use" && block.Name != "" {
				if len(block.Input) > 0 {
					// Create a brief summary of parameters with values
					params := make([]string, 0, len(block.Input))
					for key, value := range block.Input {
						// Format value as string, truncate if too long
						valStr := fmt.Sprintf("%v", value)
						if len(valStr) > 100 {
							valStr = valStr[:97] + "..."
						}
						params = append(params, fmt.Sprintf("%s=%s", key, valStr))
					}
					reqLogger.Infof("Tool call: %s (params: %s)", block.Name, strings.Join(params, ", "))
				} else {
					reqLogger.Infof("Tool call: %s (no params)", block.Name)
				}
			}
		}
	}

	// Log final response
	reqLogger.LogJSON("Outgoing Anthropic Response", anthResp)

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(anthResp); err != nil {
		reqLogger.Errorf("Failed to encode response: %v", err)
	}

	// Get response writer to access status code
	if rw, ok := w.(*proxyResponseWriter); ok {
		// Log request completion with model mapping
		reqLogger.LogRequest(r.Method, r.URL.Path, originalModel, anthReq.Model, provider,
			len(anthReq.Messages), len(anthReq.Tools), rw.statusCode, false)

		// Log request completion summary
		if anthResp != nil {
			// Count tool calls in response
			toolCallCount := 0
			if anthResp.Content != nil {
				for _, block := range anthResp.Content {
					if block.Type == "tool_use" {
						toolCallCount++
					}
				}
			}

			// Get token counts
			inputTokens := 0
			outputTokens := 0
			if anthResp.Usage != nil {
				inputTokens = anthResp.Usage.InputTokens
				outputTokens = anthResp.Usage.OutputTokens
			}

			// Log summary
			reqLogger.Infof("Request completed | Messages: %d | Tool calls: %d | Tokens: %d in, %d out | Duration: %v",
				len(anthReq.Messages), toolCallCount, inputTokens, outputTokens, reqLogger.GetDuration())
		}
	}
}

// forwardToOpenAI forwards the request to OpenAI
func (s *Server) forwardToOpenAI(ctx context.Context, anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	reqLogger := GetRequestLogger(ctx)

	// Convert to OpenAI format
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	reqLogger.LogJSON("Outgoing OpenAI Request", openAIReq)

	// Prepare request
	jsonData, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	url := s.config.OpenAIURL

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.mapper.GetAPIKey("openai")))

	// Send request with retry
	resp, err := s.client.Do(ctx, req, "openai")
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := s.readResponseBody(resp)
		s.logErrorResponse(reqLogger, "OpenAI", resp.StatusCode, body)
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return nil, fmt.Errorf("response decode error: %w", err)
	}

	// Log response
	reqLogger.LogJSON("OpenAI Response", openAIResp)

	return &openAIResp, nil
}

// forwardToGemini forwards the request to Google Gemini
func (s *Server) forwardToGemini(ctx context.Context, anthReq *AnthropicRequest) (*GeminiResponse, error) {
	reqLogger := GetRequestLogger(ctx)

	// Convert to Gemini format
	geminiReq, err := s.converter.ConvertAnthropicToGemini(ctx, anthReq)
	if err != nil {
		return nil, fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	reqLogger.LogJSON("Outgoing Gemini Request", geminiReq)

	// Prepare request
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	// Build URL with model
	baseURL := s.config.GeminiURL
	url := fmt.Sprintf("%s/%s:generateContent?key=%s",
		baseURL,
		anthReq.Model,
		s.mapper.GetAPIKey("gemini"))

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request with retry
	resp, err := s.client.Do(ctx, req, "gemini")
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := s.readResponseBody(resp)
		s.logErrorResponse(reqLogger, "Gemini", resp.StatusCode, body)
		return nil, fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var geminiResp GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, fmt.Errorf("response decode error: %w", err)
	}

	// Log response
	reqLogger.LogJSON("Gemini Response", geminiResp)

	return &geminiResp, nil
}

// forwardToArgo forwards the request to Argo
func (s *Server) forwardToArgo(ctx context.Context, anthReq *AnthropicRequest) (*ArgoChatResponse, error) {
	reqLogger := GetRequestLogger(ctx)

	// Convert to Argo format
	argoReq, err := s.converter.ConvertAnthropicToArgo(ctx, anthReq, s.config.ArgoUser)
	if err != nil {
		return nil, fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	reqLogger.LogJSON("Outgoing Argo Request", argoReq)

	// Prepare request
	jsonData, err := json.Marshal(argoReq)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	// Determine endpoint
	endpoint := "chat"
	if anthReq.Stream {
		endpoint = "streamchat"
	}

	url := s.config.GetArgoURL(endpoint)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Accept-Encoding", "identity")

	// Send request with retry
	resp, err := s.client.Do(ctx, req, "argo")
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := s.readResponseBody(resp)
		s.logErrorResponse(reqLogger, "Argo", resp.StatusCode, body)
		return nil, fmt.Errorf("argo API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var argoResp ArgoChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&argoResp); err != nil {
		return nil, fmt.Errorf("response decode error: %w", err)
	}

	// Log response
	reqLogger.LogJSON("Argo Response", argoResp)

	return &argoResp, nil
}

// forwardToAnthropic forwards the request to Anthropic
func (s *Server) forwardToAnthropic(ctx context.Context, anthReq *AnthropicRequest) (*AnthropicResponse, error) {
	reqLogger := GetRequestLogger(ctx)

	// Log outgoing request
	reqLogger.LogJSON("Outgoing Anthropic Request", anthReq)

	// Prepare request
	jsonData, err := json.Marshal(anthReq)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	url := s.config.AnthropicURL

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}

	// Set headers using auth package
	auth.SetProviderHeaders(req, "anthropic", s.mapper.GetAPIKey("anthropic"))
	auth.SetRequestHeaders(req, true, false, "anthropic")

	// Send request with retry
	resp, err := s.client.Do(ctx, req, "anthropic")
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := s.readResponseBody(resp)
		s.logErrorResponse(reqLogger, "Anthropic", resp.StatusCode, body)
		return nil, fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var anthResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
		return nil, fmt.Errorf("response decode error: %w", err)
	}

	// Log response
	reqLogger.LogJSON("Anthropic Response", anthResp)

	return &anthResp, nil
}

// handleCountTokens handles the /v1/messages/count_tokens endpoint
func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	// Get request logger from context
	reqLogger := GetRequestLogger(r.Context())

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var tokenReq AnthropicTokenCountRequest
	if err := json.NewDecoder(r.Body).Decode(&tokenReq); err != nil {
		// Check if the error is due to request body size limit
		if err.Error() == "http: request body too large" {
			apiErr := NewAPIError(ErrTypePayloadSize, "handleCountTokens", "Request body too large", err).
				WithDetails("max_size_mb", s.config.MaxRequestBodySize/1024/1024)
			s.sendAPIError(r.Context(), w, apiErr)
			return
		}
		apiErr := NewAPIError(ErrTypeValidation, "handleCountTokens", fmt.Sprintf("Invalid request body: %v", err), err)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Log incoming request at DEBUG level
	reqLogger.LogJSON("Incoming Token Count Request", tokenReq)

	// Validate message array is not empty
	if len(tokenReq.Messages) == 0 {
		apiErr := NewAPIError(ErrTypeValidation, "handleCountTokens", "Messages array cannot be empty", nil)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Simple token estimation based on content length
	totalChars := 0

	// Count system message
	if tokenReq.System != nil {
		var systemContent string
		_ = json.Unmarshal(tokenReq.System, &systemContent)
		totalChars += len(systemContent)
	}

	// Count messages
	for _, msg := range tokenReq.Messages {
		var content interface{}
		_ = json.Unmarshal(msg.Content, &content)
		totalChars += estimateContentLength(content)
	}

	// Count tools
	for _, tool := range tokenReq.Tools {
		totalChars += len(tool.Name) + len(tool.Description)
		toolJSON, _ := json.Marshal(tool.InputSchema)
		totalChars += len(toolJSON)
	}

	// Estimate tokens using the unified function
	tokens := EstimateTokenCountFromChars(totalChars)

	// Send response
	resp := AnthropicTokenCountResponse{
		InputTokens: tokens,
	}

	// Log response at DEBUG level
	reqLogger.LogJSON("Token Count Response", resp)

	// Log request summary with token count at INFO level
	reqLogger.Infof("POST %s | Messages: %d | Tools: %d | Input tokens: %d | Status: 200",
		r.URL.Path, len(tokenReq.Messages), len(tokenReq.Tools), tokens)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleStreamingRequest handles streaming message requests
func (s *Server) handleStreamingRequest(w http.ResponseWriter, r *http.Request, anthReq *AnthropicRequest, provider, originalModel, mappedModel string) {
	reqLogger := GetRequestLogger(r.Context())

	// Log the streaming request start
	reqLogger.Infof("Starting streaming request: %s->%s via %s", originalModel, mappedModel, provider)

	// Log the incoming request JSON for streaming (this was missing)
	reqLogger.LogJSON("Incoming Anthropic Streaming Request", anthReq)

	// Create Anthropic stream handler
	handler, err := NewAnthropicStreamHandler(w, originalModel, reqLogger)
	if err != nil {
		reqLogger.Errorf("Failed to create stream handler: %v", err)
		apiErr := NewAPIError(ErrTypeServer, "handleStreamingRequest", fmt.Sprintf("Failed to create stream handler: %v", err), err)
		s.sendAPIError(r.Context(), w, apiErr)
		return
	}

	// Send initial events
	if err := handler.SendMessageStart(); err != nil {
		reqLogger.Errorf("Failed to send message_start: %v", err)
		return
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		reqLogger.Errorf("Failed to send content_block_start: %v", err)
		return
	}
	if err := handler.SendPing(); err != nil {
		reqLogger.Errorf("Failed to send ping: %v", err)
		return
	}

	// Handle streaming based on provider
	switch provider {
	case "openai":
		if err := s.streamFromOpenAI(r.Context(), anthReq, handler); err != nil {
			reqLogger.Errorf("OpenAI streaming error: %v", err)
			if completeErr := handler.Complete("error"); completeErr != nil {
				reqLogger.Errorf("Failed to send error completion: %v", completeErr)
			}
		}
	case "gemini":
		if err := s.streamFromGemini(r.Context(), anthReq, handler); err != nil {
			reqLogger.Errorf("Gemini streaming error: %v", err)
			if completeErr := handler.Complete("error"); completeErr != nil {
				reqLogger.Errorf("Failed to send error completion: %v", completeErr)
			}
		}
	case "anthropic":
		if err := s.streamFromAnthropic(r.Context(), anthReq, handler); err != nil {
			reqLogger.Errorf("Anthropic streaming error: %v", err)
			if completeErr := handler.Complete("error"); completeErr != nil {
				reqLogger.Errorf("Failed to send error completion: %v", completeErr)
			}
		}
	case "argo":
		// Check if request has tools - Argo streamchat doesn't handle them correctly
		if len(anthReq.Tools) > 0 {
			reqLogger.Debugf("Request has %d tools defined, using simulated streaming for Argo", len(anthReq.Tools))
			if err := s.simulateStreamingFromArgo(r.Context(), anthReq, handler); err != nil {
				reqLogger.Errorf("Argo simulated streaming error: %v", err)
				if completeErr := handler.Complete("error"); completeErr != nil {
					reqLogger.Errorf("Failed to send error completion: %v", completeErr)
				}
			}
		} else {
			reqLogger.Debugf("Request has no tools, using native Argo streaming")
			if err := s.streamFromArgo(r.Context(), anthReq, handler); err != nil {
				reqLogger.Errorf("Argo native streaming failed: %v", err)
				// Try to fallback to simulated streaming
				reqLogger.Debugf("=== Fallback: Attempting simulated streaming after native streaming failure ===")
				reqLogger.Debugf("Native streaming error was: %v", err)

				if err := s.simulateStreamingFromArgo(r.Context(), anthReq, handler); err != nil {
					reqLogger.Errorf("Fallback to simulated streaming also failed: %v", err)
					if completeErr := handler.Complete("error"); completeErr != nil {
						reqLogger.Errorf("Failed to send error completion: %v", completeErr)
					}
				} else {
					reqLogger.Debugf("Fallback to simulated streaming succeeded")
				}
			}
		}
	default:
		if completeErr := handler.Complete("error"); completeErr != nil {
			reqLogger.Errorf("Failed to send error completion: %v", completeErr)
		}
	}

	// Log request completion with model mapping
	if rw, ok := w.(*proxyResponseWriter); ok {
		reqLogger.LogRequest(r.Method, r.URL.Path, originalModel, anthReq.Model, provider,
			len(anthReq.Messages), len(anthReq.Tools), rw.statusCode, true)

		// Log request completion summary for streaming
		if handler != nil && handler.state != nil {
			toolCallCount := len(handler.state.ToolCalls)
			inputTokens := handler.state.InputTokens
			outputTokens := handler.state.OutputTokens

			// Log summary
			reqLogger.Infof("Request completed | Messages: %d | Tool calls: %d | Tokens: %d in, %d out | Duration: %v",
				len(anthReq.Messages), toolCallCount, inputTokens, outputTokens, reqLogger.GetDuration())
		}
	}
}

// streamFromOpenAI streams from OpenAI
func (s *Server) streamFromOpenAI(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	reqLogger := GetRequestLogger(ctx)

	// Convert to OpenAI format
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return fmt.Errorf("conversion error: %w", err)
	}

	// Force streaming
	openAIReq.Stream = true

	// Log outgoing request
	reqLogger.LogJSON("Outgoing OpenAI Streaming Request", openAIReq)

	// Prepare request
	jsonData, err := json.Marshal(openAIReq)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	url := s.config.OpenAIURL

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.mapper.GetAPIKey("openai")))

	// Send request with retry
	resp, err := s.client.Do(ctx, req, "openai")
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}

	// Ensure body is closed even if parsing fails
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := s.readResponseBody(resp)
		s.logErrorResponse(reqLogger, "OpenAI Streaming", resp.StatusCode, body)
		return fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse streaming response
	parser := NewOpenAIStreamParser(handler)
	return parser.Parse(resp.Body)
}

// streamFromGemini streams from Google Gemini
func (s *Server) streamFromGemini(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	reqLogger := GetRequestLogger(ctx)

	// Convert to Gemini format
	geminiReq, err := s.converter.ConvertAnthropicToGemini(ctx, anthReq)
	if err != nil {
		return fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	reqLogger.LogJSON("Outgoing Gemini Streaming Request", geminiReq)

	// Build URL with streaming endpoint
	baseURL := s.config.GeminiURL
	url := fmt.Sprintf("%s/%s:streamGenerateContent?key=%s&alt=sse",
		baseURL,
		anthReq.Model,
		s.mapper.GetAPIKey("gemini"))

	// Prepare request
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request with retry
	resp, err := s.client.Do(ctx, req, "gemini")
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := s.readResponseBody(resp)
		s.logErrorResponse(reqLogger, "Gemini Streaming", resp.StatusCode, body)
		return fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse streaming response
	parser := NewGeminiStreamParser(handler)
	return parser.Parse(resp.Body)
}

// streamFromArgo streams from Argo
func (s *Server) streamFromArgo(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	reqLogger := GetRequestLogger(ctx)

	// Estimate and set input tokens for Argo streaming
	// Argo doesn't provide token counts, so we must estimate them
	inputTokens := EstimateRequestTokens(anthReq)
	handler.mu.Lock()
	handler.state.InputTokens = inputTokens
	handler.mu.Unlock()

	// Convert to Argo format
	argoReq, err := s.converter.ConvertAnthropicToArgo(ctx, anthReq, s.config.ArgoUser)
	if err != nil {
		return fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	reqLogger.LogJSON("Outgoing Argo Streaming Request", argoReq)

	// Prepare request
	jsonData, err := json.Marshal(argoReq)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	url := s.config.GetArgoURL("streamchat")

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Accept-Encoding", "identity")

	// Send request with retry while sending pings
	return s.streamFromArgoWithPings(ctx, req, handler, defaultPingInterval)
}

// streamFromArgoWithPings sends the request to Argo streamchat and handles pings while waiting
func (s *Server) streamFromArgoWithPings(ctx context.Context, req *http.Request, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	reqLogger := GetRequestLogger(ctx)

	// Create a cancellable context for the API call
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Channel for API response
	type argoResult struct {
		resp *http.Response
		err  error
	}
	resultChan := make(chan argoResult, 1)

	// Make the API call in a goroutine
	go func() {
		resp, err := s.client.Do(callCtx, req, "argo")
		// Check if parent context is already cancelled before trying to send
		if ctx.Err() != nil {
			reqLogger.Debugf("Parent context already cancelled, not sending result")
			if resp != nil {
				resp.Body.Close()
			}
			return
		}
		select {
		case resultChan <- argoResult{resp: resp, err: err}:
			// Successfully sent result
		case <-ctx.Done():
			// Context was cancelled, don't block on send
			reqLogger.Debugf("Context cancelled before sending result to channel")
			if resp != nil {
				resp.Body.Close()
			}
		}
	}()

	// Send pings while waiting for initial response
	waitStartTime := time.Now()
	reqLogger.Debugf("Waiting for Argo streaming response (ping interval: %v)", pingInterval)

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case result := <-resultChan:
			reqLogger.Debugf("Received response from Argo streamchat after %v", time.Since(waitStartTime))
			if result.err != nil {
				return fmt.Errorf("request error: %w", result.err)
			}

			defer result.resp.Body.Close()

			// Check status
			if result.resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(result.resp.Body)
				s.logErrorResponse(reqLogger, "Argo Streaming", result.resp.StatusCode, body)
				return fmt.Errorf("argo API error (status %d): %s", result.resp.StatusCode, string(body))
			}

			// Parse streaming response with continued ping support
			parser := NewArgoStreamParser(handler)
			return parser.ParseWithPingInterval(result.resp.Body, pingInterval)

		case <-pingTicker.C:
			reqLogger.Debugf("Sending ping while waiting for Argo streaming response")
			if err := handler.SendPing(); err != nil {
				reqLogger.Errorf("Failed to send ping, client likely disconnected: %v", err)
				// Cancel the API call since client is gone
				cancel()
				return fmt.Errorf("client disconnected: %w", err)
			}

		case <-ctx.Done():
			reqLogger.Debugf("Context cancelled while waiting for Argo streaming response")
			return ctx.Err()
		}
	}
}

// streamFromAnthropic streams from Anthropic
func (s *Server) streamFromAnthropic(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	reqLogger := GetRequestLogger(ctx)

	// Force streaming
	anthReq.Stream = true

	// Log outgoing request
	reqLogger.LogJSON("Outgoing Anthropic Streaming Request", anthReq)

	// Prepare request
	jsonData, err := json.Marshal(anthReq)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	url := s.config.AnthropicURL

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request creation error: %w", err)
	}

	// Set headers using auth package
	auth.SetProviderHeaders(req, "anthropic", s.mapper.GetAPIKey("anthropic"))
	auth.SetRequestHeaders(req, true, true, "anthropic")

	// Send request with retry
	resp, err := s.client.Do(ctx, req, "anthropic")
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := s.readResponseBody(resp)
		s.logErrorResponse(reqLogger, "Anthropic Streaming", resp.StatusCode, body)
		return fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse Anthropic's native SSE format
	return s.parseAnthropicStream(resp.Body, handler)
}

// parseAnthropicStream parses Anthropic's native SSE format
func (s *Server) parseAnthropicStream(body io.Reader, handler *AnthropicStreamHandler) error {
	scanner := bufio.NewScanner(body)

	var currentEvent string
	var stopReason string

	for scanner.Scan() {
		line := scanner.Text()

		// Parse SSE format
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			// Parse JSON based on current event type
			switch currentEvent {
			case "message_start":
				// Extract usage from message_start
				var msgStart struct {
					Message struct {
						Usage struct {
							InputTokens int `json:"input_tokens"`
						} `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal([]byte(data), &msgStart); err == nil {
					handler.state.InputTokens = msgStart.Message.Usage.InputTokens
				}

			case "content_block_start":
				// Already handled by handler initialization

			case "content_block_delta":
				// Parse delta for text content
				var delta struct {
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(data), &delta); err == nil {
					if delta.Delta.Type == "text_delta" && delta.Delta.Text != "" {
						if err := handler.SendTextDelta(delta.Delta.Text); err != nil {
							return err
						}
					}
				}

			case "content_block_stop":
				// Content block completed

			case "message_delta":
				// Parse delta for stop reason and usage
				var msgDelta struct {
					Delta struct {
						StopReason string `json:"stop_reason,omitempty"`
					} `json:"delta"`
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(data), &msgDelta); err == nil {
					if msgDelta.Delta.StopReason != "" {
						stopReason = msgDelta.Delta.StopReason
					}
					if msgDelta.Usage.OutputTokens > 0 {
						handler.state.OutputTokens = msgDelta.Usage.OutputTokens
					}
				}

			case "message_stop":
				// Complete the stream with the stop reason
				return handler.Complete(stopReason)

			case "error":
				// Handle error event
				var errorData struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(data), &errorData); err == nil {
					return fmt.Errorf("anthropic API error: %s - %s", errorData.Error.Type, errorData.Error.Message)
				}
			}

			// Reset current event after processing data
			currentEvent = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stream: %w", err)
	}

	// If we didn't receive a proper message_stop, complete anyway
	if stopReason == "" {
		stopReason = "end_turn"
	}
	return handler.Complete(stopReason)
}

// simulateStreamingFromArgo simulates streaming by calling non-streaming endpoint with default ping interval
func (s *Server) simulateStreamingFromArgo(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	return s.simulateStreamingFromArgoWithInterval(ctx, anthReq, handler, defaultPingInterval)
}

// validatePingInterval ensures the ping interval is within acceptable bounds
func (s *Server) validatePingInterval(pingInterval time.Duration) time.Duration {
	// Validate ping interval to prevent panic and CPU spinning
	if pingInterval <= 0 {
		logger.Debugf("WARNING: Invalid pingInterval %v, using default %v", pingInterval, defaultPingInterval)
		return defaultPingInterval
	}
	if pingInterval < minPingInterval {
		logger.Debugf("WARNING: pingInterval %v too small, using minimum %v", pingInterval, minPingInterval)
		return minPingInterval
	}
	if pingInterval > maxPingInterval {
		logger.Debugf("WARNING: pingInterval %v too large, using maximum %v", pingInterval, maxPingInterval)
		return maxPingInterval
	}
	return pingInterval
}

// waitForArgoResponseWithPings fetches the Argo response while sending periodic pings
func (s *Server) waitForArgoResponseWithPings(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) (*ArgoChatResponse, error) {
	reqLogger := GetRequestLogger(ctx)

	// Create a cancellable context for the API call
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Channel for API response
	type argoResult struct {
		resp *ArgoChatResponse
		err  error
	}
	resultChan := make(chan argoResult, 1)

	// Make the API call in a goroutine
	go func() {
		// Create non-streaming copy
		nonStreamingReq := *anthReq
		nonStreamingReq.Stream = false

		resp, err := s.forwardToArgo(callCtx, &nonStreamingReq)
		// Check if parent context is already cancelled before trying to send
		if ctx.Err() != nil {
			reqLogger.Debugf("Parent context already cancelled, not sending result")
			return
		}
		select {
		case resultChan <- argoResult{resp: resp, err: err}:
			// Successfully sent result
		case <-ctx.Done():
			// Context was cancelled, don't block on send
			reqLogger.Debugf("Context cancelled before sending result to channel")
		}
	}()

	// Send pings while waiting for API response
	waitStartTime := time.Now()
	reqLogger.Debugf("Waiting for Argo API response (ping interval: %v)", pingInterval)

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case result := <-resultChan:
			reqLogger.Debugf("Received response from Argo API after %v", time.Since(waitStartTime))
			if result.err != nil {
				return nil, result.err
			}
			return result.resp, nil

		case <-pingTicker.C:
			reqLogger.Debugf("Sending ping while waiting for Argo API response")
			if err := handler.SendPing(); err != nil {
				reqLogger.Errorf("Failed to send ping, client likely disconnected: %v", err)
				// Cancel the API call since client is gone
				cancel()
				return nil, fmt.Errorf("client disconnected: %w", err)
			}

		case <-ctx.Done():
			reqLogger.Debugf("Context cancelled while waiting for Argo API response")
			return nil, ctx.Err()
		}
	}
}

// streamArgoResponseContent streams the content blocks from an Argo response
func (s *Server) streamArgoResponseContent(ctx context.Context, anthResp *AnthropicResponse, handler *AnthropicStreamHandler) error {
	reqLogger := GetRequestLogger(ctx)

	reqLogger.Debugf("Processing %d content blocks for simulated streaming", len(anthResp.Content))

	// Process each content block
	blockIndex := 0 // Index for tool blocks (text is always index 0)
	textBlockProcessed := false

	for _, block := range anthResp.Content {
		switch block.Type {
		case "text":
			if err := s.streamTextBlock(block.Text, handler); err != nil {
				return err
			}
			textBlockProcessed = true

		case "tool_use":
			// Close text block if needed
			if textBlockProcessed && !handler.state.TextBlockClosed {
				if err := handler.SendContentBlockStop(0); err != nil {
					reqLogger.Errorf("Failed to close text block: %v", err)
					return fmt.Errorf("failed to close text block: %w", err)
				}
				handler.state.TextBlockClosed = true
			}

			// Stream tool block
			blockIndex++
			if err := s.streamToolBlock(ctx, block, blockIndex, handler); err != nil {
				return err
			}

		default:
			reqLogger.Debugf("WARNING: Unknown block type: %s", block.Type)
		}
	}

	// Update token counts
	if anthResp.Usage != nil {
		handler.state.InputTokens = anthResp.Usage.InputTokens
		handler.state.OutputTokens = anthResp.Usage.OutputTokens
	}

	// Complete the stream
	return handler.Complete(anthResp.StopReason)
}

// streamTextBlock streams a text content block in chunks
func (s *Server) streamTextBlock(content string, handler *AnthropicStreamHandler) error {
	if content == "" {
		return nil
	}

	// Split content into chunks to simulate streaming
	chunks := splitTextForStreaming(content, defaultTextChunkSize)

	for _, chunk := range chunks {
		if err := handler.SendTextDelta(chunk); err != nil {
			logger.Errorf("Failed to send text chunk: %v", err)
			return fmt.Errorf("failed to send text chunk: %w", err)
		}
		// No artificial delay - let network be the natural throttle
	}

	return nil
}

// streamToolBlock streams a tool use block
func (s *Server) streamToolBlock(ctx context.Context, block AnthropicContentBlock, blockIndex int, handler *AnthropicStreamHandler) error {
	reqLogger := GetRequestLogger(ctx)

	// Log tool call at INFO level with parameters
	if len(block.Input) > 0 {
		// Create a brief summary of parameters with values
		params := make([]string, 0, len(block.Input))
		for key, value := range block.Input {
			// Format value as string, truncate if too long
			valStr := fmt.Sprintf("%v", value)
			if len(valStr) > 100 {
				valStr = valStr[:97] + "..."
			}
			params = append(params, fmt.Sprintf("%s=%s", key, valStr))
		}
		reqLogger.Infof("Tool call: %s (params: %s)", block.Name, strings.Join(params, ", "))
	} else {
		reqLogger.Infof("Tool call: %s (no params)", block.Name)
	}

	// Validate tool block
	if block.ID == "" {
		reqLogger.Debugf("WARNING: Tool block has empty ID")
	}
	if block.Name == "" {
		reqLogger.Debugf("WARNING: Tool block has empty name")
	}
	if block.Input == nil {
		block.Input = make(map[string]interface{})
	}

	// Send tool use start
	if err := handler.SendToolUseStart(blockIndex, block.ID, block.Name); err != nil {
		reqLogger.Errorf("Failed to send tool_use start for block index %d: %v", blockIndex, err)
		return fmt.Errorf("failed to send tool_use start (index=%d): %w", blockIndex, err)
	}

	// Send tool input as JSON chunks
	inputJSON, err := json.Marshal(block.Input)
	if err != nil {
		reqLogger.Errorf("Failed to marshal tool input for %s: %v", block.Name, err)
		return fmt.Errorf("failed to marshal tool input for %s: %w", block.Name, err)
	}

	// Send empty initial delta to match Anthropic format
	if err := handler.SendToolInputDelta(blockIndex, ""); err != nil {
		reqLogger.Errorf("Failed to send initial empty delta: %v", err)
		return fmt.Errorf("failed to send initial empty delta: %w", err)
	}

	// Stream the JSON in chunks
	jsonStr := string(inputJSON)
	chunks := splitTextForStreaming(jsonStr, defaultJSONChunkSize)
	for _, chunk := range chunks {
		if err := handler.SendToolInputDelta(blockIndex, chunk); err != nil {
			reqLogger.Errorf("Failed to send tool input chunk: %v", err)
			return fmt.Errorf("failed to send tool input chunk: %w", err)
		}
		// No artificial delay - let network be the natural throttle
	}

	// Close tool block
	if err := handler.SendContentBlockStop(blockIndex); err != nil {
		reqLogger.Errorf("Failed to close tool block index %d: %v", blockIndex, err)
		return fmt.Errorf("failed to close tool block (index=%d): %w", blockIndex, err)
	}

	// Update tool tracking
	handler.state.LastToolIndex = blockIndex
	handler.state.ToolIndex = &blockIndex

	return nil
}

// simulateStreamingFromArgoWithInterval simulates streaming with configurable ping interval
func (s *Server) simulateStreamingFromArgoWithInterval(ctx context.Context, anthReq *AnthropicRequest, handler *AnthropicStreamHandler, pingInterval time.Duration) error {
	reqLogger := GetRequestLogger(ctx)

	// Validate ping interval
	pingInterval = s.validatePingInterval(pingInterval)

	reqLogger.Debugf("Using simulated streaming (%d tools)", len(anthReq.Tools))
	handler.simulatedStreaming = true

	// Get response from Argo while sending pings
	argoResp, err := s.waitForArgoResponseWithPings(ctx, anthReq, handler, pingInterval)
	if err != nil {
		reqLogger.Errorf("Failed to get non-streaming response from Argo: %v", err)
		return fmt.Errorf("failed to get non-streaming response: %w", err)
	}

	// Convert response
	anthResp := s.converter.ConvertArgoToAnthropicWithRequest(argoResp, handler.originalModel, anthReq)

	// Validate response
	if anthResp == nil {
		reqLogger.Errorf("Converted response is nil")
		return fmt.Errorf("converted response is nil")
	}

	// Stream the response content
	return s.streamArgoResponseContent(ctx, anthResp, handler)
}

func (s *Server) logErrorResponse(reqLogger *RequestScopedLogger, provider string, statusCode int, body []byte) {
	// Try to parse as JSON for better logging
	var errorData interface{}
	if err := json.Unmarshal(body, &errorData); err == nil {
		reqLogger.LogJSON(fmt.Sprintf("%s Error Response (status %d)", provider, statusCode), errorData)
	} else {
		reqLogger.Debugf("%s Error Response (status %d, raw): %s", provider, statusCode, string(body))
	}
}

// estimateContentLength estimates the character length of content
func estimateContentLength(content interface{}) int {
	switch v := content.(type) {
	case string:
		return len(v)
	case []interface{}:
		length := 0
		for _, item := range v {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if text, ok := itemMap["text"].(string); ok {
					length += len(text)
				}
			}
		}
		return length
	default:
		// Marshal and count
		data, _ := json.Marshal(content)
		return len(data)
	}
}

// splitTextForStreaming splits text into chunks for simulated streaming.
// It uses Go's built-in UTF-8 iteration to ensure we never break multibyte characters.
func splitTextForStreaming(text string, targetChunkSize int) []string {
	if len(text) == 0 {
		return nil
	}

	// Parameter validation
	if targetChunkSize <= 0 {
		targetChunkSize = defaultTextChunkSize
	}

	var chunks []string
	var currentChunk []rune

	for _, r := range text {
		currentChunk = append(currentChunk, r)

		// Check if we've reached the target size in bytes
		if len(string(currentChunk)) >= targetChunkSize {
			chunks = append(chunks, string(currentChunk))
			currentChunk = nil
		}
	}

	// Don't forget the last chunk
	if len(currentChunk) > 0 {
		chunks = append(chunks, string(currentChunk))
	}

	return chunks
}
