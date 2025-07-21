package apiproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Server represents the API proxy server
type Server struct {
	config    *Config
	mapper    *ModelMapper
	converter *Converter
	client    *RetryableHTTPClient
}

// NewServer creates a new API proxy server
func NewServer(config *Config) http.Handler {
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    NewRetryableHTTPClient(10 * time.Minute),
	}

	// Wrap with middleware (order matters - outermost runs first)
	handler := http.Handler(server)
	handler = NewRequestLogger(handler)
	handler = NewErrorMiddleware(handler)
	handler = NewSecurityMiddleware(handler, config)
	handler = NewStreamingMiddleware(handler)
	handler = NewRequestIDMiddleware(handler)

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
		http.NotFound(w, r)
	}
}

// handleRoot handles the root endpoint
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "Anthropic API Proxy for OpenAI, Google, and Argo",
	})
}

// handleMessages handles the /v1/messages endpoint
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var anthReq AnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&anthReq); err != nil {
		// Check if the error is due to request body size limit
		if err.Error() == "http: request body too large" {
			apiErr := NewAPIError(ErrTypePayloadSize, "handleMessages", "Request body too large", err).
				WithDetails("max_size_mb", 10)
			s.sendAPIError(w, apiErr)
			return
		}
		apiErr := NewAPIError(ErrTypeValidation, "handleMessages", "Invalid request body", err)
		s.sendAPIError(w, apiErr)
		return
	}

	// Log incoming request
	LogJSON("Incoming Anthropic Request", anthReq)

	// Validate message array is not empty
	if len(anthReq.Messages) == 0 {
		apiErr := NewAPIError(ErrTypeValidation, "handleMessages", "Messages array cannot be empty", nil).
			WithDetails("field", "messages")
		s.sendAPIError(w, apiErr)
		return
	}

	// Store original model for response
	originalModel := anthReq.Model

	// Map the model to appropriate provider
	provider, mappedModel := s.mapper.MapModel(anthReq.Model)
	if provider == "" || mappedModel == "" {
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("No API key configured for model: %s. Please provide the appropriate API key file (--openai-api-key-file, --gemini-api-key-file, or --argo-user)", originalModel))
		return
	}
	anthReq.Model = mappedModel

	LogDebug(fmt.Sprintf("Model mapping: %s -> %s/%s", originalModel, provider, mappedModel))

	// Handle streaming if requested
	if anthReq.Stream {
		LogDebug(fmt.Sprintf("Streaming requested for model %s via provider %s", originalModel, provider))
		s.handleStreamingRequest(w, &anthReq, provider, originalModel)
		return
	}

	// Handle non-streaming request
	s.handleNonStreamingRequest(w, &anthReq, provider, originalModel)
}

// handleNonStreamingRequest handles non-streaming message requests
func (s *Server) handleNonStreamingRequest(w http.ResponseWriter, anthReq *AnthropicRequest, provider, originalModel string) {
	var response interface{}
	var err error

	switch provider {
	case "openai":
		response, err = s.forwardToOpenAI(anthReq)
	case "gemini":
		response, err = s.forwardToGemini(anthReq)
	case "argo":
		response, err = s.forwardToArgo(anthReq)
	default:
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Unknown provider: %s", provider))
		return
	}

	if err != nil {
		s.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Provider error: %v", err))
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
		anthResp = s.converter.ConvertArgoToAnthropic(resp, originalModel)
	default:
		s.sendError(w, http.StatusInternalServerError, "Invalid response type from provider")
		return
	}

	// Log final response
	LogJSON("Outgoing Anthropic Response", anthResp)

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(anthResp); err != nil {
		LogError("Failed to encode response", err)
	}
}

// forwardToOpenAI forwards the request to OpenAI
func (s *Server) forwardToOpenAI(anthReq *AnthropicRequest) (*OpenAIResponse, error) {
	// Convert to OpenAI format
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(anthReq)
	if err != nil {
		return nil, fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	LogJSON("Outgoing OpenAI Request", openAIReq)

	// Prepare request
	jsonData, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequest("POST", GetProviderURL("openai"), bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.mapper.GetAPIKey("openai")))

	// Send request with retry
	ctx := context.Background()
	resp, err := s.client.Do(ctx, req, "openai")
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return nil, fmt.Errorf("response decode error: %w", err)
	}

	// Log response
	LogJSON("OpenAI Response", openAIResp)

	return &openAIResp, nil
}

// forwardToGemini forwards the request to Google Gemini
func (s *Server) forwardToGemini(anthReq *AnthropicRequest) (*GeminiResponse, error) {
	// Convert to Gemini format
	geminiReq, err := s.converter.ConvertAnthropicToGemini(anthReq)
	if err != nil {
		return nil, fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	LogJSON("Outgoing Gemini Request", geminiReq)

	// Prepare request
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	// Build URL with model
	url := fmt.Sprintf("%s/%s:generateContent?key=%s",
		GetProviderURL("gemini"),
		anthReq.Model,
		s.mapper.GetAPIKey("gemini"))

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request with retry
	ctx := context.Background()
	resp, err := s.client.Do(ctx, req, "gemini")
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var geminiResp GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, fmt.Errorf("response decode error: %w", err)
	}

	// Log response
	LogJSON("Gemini Response", geminiResp)

	return &geminiResp, nil
}

// forwardToArgo forwards the request to Argo
func (s *Server) forwardToArgo(anthReq *AnthropicRequest) (*ArgoChatResponse, error) {
	// Convert to Argo format
	argoReq, err := s.converter.ConvertAnthropicToArgo(anthReq, s.config.ArgoUser)
	if err != nil {
		return nil, fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	LogJSON("Outgoing Argo Request", argoReq)

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

	url := GetArgoURL(s.config.ArgoEnv, endpoint)

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Accept-Encoding", "identity")

	// Send request with retry
	ctx := context.Background()
	resp, err := s.client.Do(ctx, req, "argo")
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("argo API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var argoResp ArgoChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&argoResp); err != nil {
		return nil, fmt.Errorf("response decode error: %w", err)
	}

	// Log response
	LogJSON("Argo Response", argoResp)

	return &argoResp, nil
}

// handleCountTokens handles the /v1/messages/count_tokens endpoint
func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var tokenReq AnthropicTokenCountRequest
	if err := json.NewDecoder(r.Body).Decode(&tokenReq); err != nil {
		// Check if the error is due to request body size limit
		if err.Error() == "http: request body too large" {
			s.sendError(w, http.StatusRequestEntityTooLarge, "Request body too large. Maximum allowed size is 10MB")
			return
		}
		s.sendError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	// Validate message array is not empty
	if len(tokenReq.Messages) == 0 {
		s.sendError(w, http.StatusBadRequest, "Messages array cannot be empty")
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

	// Estimate tokens (~4 chars per token)
	tokens := totalChars / 4

	// Send response
	resp := AnthropicTokenCountResponse{
		InputTokens: tokens,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleStreamingRequest handles streaming message requests
func (s *Server) handleStreamingRequest(w http.ResponseWriter, anthReq *AnthropicRequest, provider, originalModel string) {
	LogDebug(fmt.Sprintf("handleStreamingRequest called for provider %s", provider))

	// Log the streaming request start
	LogInfo(fmt.Sprintf("Starting streaming request: %s via %s", originalModel, provider))

	// Create Anthropic stream handler
	handler, err := NewAnthropicStreamHandler(w, originalModel)
	if err != nil {
		LogError(fmt.Sprintf("Failed to create stream handler: %v", err), err)
		s.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create stream handler: %v", err))
		return
	}

	LogDebug("Stream handler created successfully")

	// Send initial events
	if err := handler.SendMessageStart(); err != nil {
		log.Printf("Failed to send message_start: %v", err)
		return
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		log.Printf("Failed to send content_block_start: %v", err)
		return
	}
	if err := handler.SendPing(); err != nil {
		log.Printf("Failed to send ping: %v", err)
		return
	}

	// Handle streaming based on provider
	switch provider {
	case "openai":
		if err := s.streamFromOpenAI(anthReq, handler); err != nil {
			LogError(fmt.Sprintf("OpenAI streaming error: %v", err), err)
			_ = handler.Complete("error")
		}
	case "gemini":
		if err := s.streamFromGemini(anthReq, handler); err != nil {
			LogError(fmt.Sprintf("Gemini streaming error: %v", err), err)
			_ = handler.Complete("error")
		}
	case "argo":
		// Check if request has tools - Argo streamchat doesn't handle them correctly
		if len(anthReq.Tools) > 0 {
			LogDebug(fmt.Sprintf("Request has %d tools defined, using simulated streaming for Argo", len(anthReq.Tools)))
			if err := s.simulateStreamingFromArgo(anthReq, handler); err != nil {
				LogError(fmt.Sprintf("Argo simulated streaming error: %v", err), err)
				_ = handler.Complete("error")
			}
		} else {
			LogDebug("Request has no tools, using native Argo streaming")
			if err := s.streamFromArgo(anthReq, handler); err != nil {
				LogError(fmt.Sprintf("Argo native streaming failed: %v", err), err)
				// Try to fallback to simulated streaming
				LogDebug("=== Fallback: Attempting simulated streaming after native streaming failure ===")
				LogDebug(fmt.Sprintf("Native streaming error was: %v", err))

				if err := s.simulateStreamingFromArgo(anthReq, handler); err != nil {
					LogError("Fallback to simulated streaming also failed", err)
					if completeErr := handler.Complete("error"); completeErr != nil {
						LogError("Failed to send error completion", completeErr)
					}
				} else {
					LogDebug("Fallback to simulated streaming succeeded")
				}
			}
		}
	default:
		_ = handler.Complete("error")
	}
}

// streamFromOpenAI streams from OpenAI
func (s *Server) streamFromOpenAI(anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	// Convert to OpenAI format
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(anthReq)
	if err != nil {
		return fmt.Errorf("conversion error: %w", err)
	}

	// Force streaming
	openAIReq.Stream = true

	// Log outgoing request
	LogJSON("Outgoing OpenAI Streaming Request", openAIReq)

	// Prepare request
	jsonData, err := json.Marshal(openAIReq)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequest("POST", GetProviderURL("openai"), bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.mapper.GetAPIKey("openai")))

	// Send request with retry
	ctx := context.Background()
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
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse streaming response
	parser := NewOpenAIStreamParser(handler)
	return parser.Parse(resp.Body)
}

// streamFromGemini streams from Google Gemini
func (s *Server) streamFromGemini(anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	// Convert to Gemini format
	geminiReq, err := s.converter.ConvertAnthropicToGemini(anthReq)
	if err != nil {
		return fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	LogJSON("Outgoing Gemini Streaming Request", geminiReq)

	// Build URL with streaming endpoint
	url := fmt.Sprintf("%s/%s:streamGenerateContent?key=%s&alt=sse",
		GetProviderURL("gemini"),
		anthReq.Model,
		s.mapper.GetAPIKey("gemini"))

	// Prepare request
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request with retry
	ctx := context.Background()
	resp, err := s.client.Do(ctx, req, "gemini")
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse streaming response
	parser := NewGeminiStreamParser(handler)
	return parser.Parse(resp.Body)
}

// streamFromArgo streams from Argo
func (s *Server) streamFromArgo(anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	// Convert to Argo format
	argoReq, err := s.converter.ConvertAnthropicToArgo(anthReq, s.config.ArgoUser)
	if err != nil {
		return fmt.Errorf("conversion error: %w", err)
	}

	// Log outgoing request
	LogJSON("Outgoing Argo Streaming Request", argoReq)

	// Prepare request
	jsonData, err := json.Marshal(argoReq)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	url := GetArgoURL(s.config.ArgoEnv, "streamchat")

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("request creation error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Accept-Encoding", "identity")

	// Send request with retry
	ctx := context.Background()
	resp, err := s.client.Do(ctx, req, "argo")
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("argo API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse streaming response
	parser := NewArgoStreamParser(handler)
	return parser.Parse(resp.Body)
}

// simulateStreamingFromArgo simulates streaming by calling non-streaming endpoint
func (s *Server) simulateStreamingFromArgo(anthReq *AnthropicRequest, handler *AnthropicStreamHandler) error {
	LogDebug("=== Starting Simulated Streaming for Argo ===")
	LogDebug(fmt.Sprintf("Request ID: %s, Model: %s", handler.state.MessageID, anthReq.Model))

	// Mark this as simulated streaming to avoid tracking tool calls
	LogDebug("Setting handler.simulatedStreaming = true to prevent duplicate tool tracking")
	handler.simulatedStreaming = true

	// Force non-streaming for the actual request
	LogDebug("Forcing anthReq.Stream = false for non-streaming endpoint call")
	anthReq.Stream = false

	// Get non-streaming response
	LogDebug("Calling forwardToArgo to get non-streaming response...")
	argoResp, err := s.forwardToArgo(anthReq)
	if err != nil {
		LogError("Failed to get non-streaming response from Argo", err)
		return fmt.Errorf("failed to get non-streaming response: %w", err)
	}

	// Start ping ticker for long-running operations
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	// Create a channel to signal when conversion is done
	conversionDone := make(chan struct{})
	var anthResp *AnthropicResponse

	// Run conversion in a goroutine
	go func() {
		defer close(conversionDone)
		LogDebug("Converting Argo response to Anthropic format...")
		anthResp = s.converter.ConvertArgoToAnthropic(argoResp, handler.originalModel)
	}()

	// Send pings while waiting for conversion
	for {
		select {
		case <-conversionDone:
			// Conversion completed, break out of loop
			goto conversionComplete
		case <-pingTicker.C:
			// Send ping every 15 seconds
			if err := handler.SendPing(); err != nil {
				LogDebug(fmt.Sprintf("Failed to send ping: %v", err))
			}
		}
	}

conversionComplete:

	// Validate response
	if anthResp == nil {
		LogError("Converted response is nil", nil)
		return fmt.Errorf("converted response is nil")
	}

	LogDebug(fmt.Sprintf("Processing %d content blocks for simulated streaming", len(anthResp.Content)))

	// Process each content block
	blockIndex := 0 // Index for tool blocks (text is always index 0)
	textBlockProcessed := false

	for _, block := range anthResp.Content {
		switch block.Type {
		case "text":
			// Send text in chunks
			content := block.Text

			if content == "" {
				continue
			}

			textBlockProcessed = true

			// Split content into chunks to simulate streaming
			chunkSize := 20 // Characters per chunk

			for j := 0; j < len(content); j += chunkSize {
				end := j + chunkSize
				if end > len(content) {
					end = len(content)
				}

				chunk := content[j:end]

				if err := handler.SendTextDelta(chunk); err != nil {
					LogError("Failed to send text chunk", err)
					return fmt.Errorf("failed to send text chunk: %w", err)
				}

				// Small delay to simulate streaming
				time.Sleep(10 * time.Millisecond)
			}

		case "tool_use":
			LogDebug(fmt.Sprintf("Tool: %s", block.Name))

			// Validate tool block
			if block.ID == "" {
				LogDebug("WARNING: Tool block has empty ID")
			}
			if block.Name == "" {
				LogDebug("WARNING: Tool block has empty name")
			}
			if block.Input == nil {
				block.Input = make(map[string]interface{})
			}

			// Close text block if needed
			if textBlockProcessed && !handler.state.TextBlockClosed {

				if err := handler.SendContentBlockStop(0); err != nil {
					LogError("Failed to close text block", err)
					return fmt.Errorf("failed to close text block: %w", err)
				}
				handler.state.TextBlockClosed = true
			}

			// Send tool use block
			blockIndex++

			if err := handler.SendToolUseStart(blockIndex, block.ID, block.Name); err != nil {
				LogError(fmt.Sprintf("Failed to send tool_use start for block index %d", blockIndex), err)
				return fmt.Errorf("failed to send tool_use start (index=%d): %w", blockIndex, err)
			}

			// Send tool input as JSON chunks to match Anthropic's streaming format
			inputJSON, err := json.Marshal(block.Input)
			if err != nil {
				LogError(fmt.Sprintf("Failed to marshal tool input for %s", block.Name), err)
				return fmt.Errorf("failed to marshal tool input for %s: %w", block.Name, err)
			}

			// Send empty initial delta to match Anthropic format
			if err := handler.SendToolInputDelta(blockIndex, ""); err != nil {
				LogError("Failed to send initial empty delta", err)
				return fmt.Errorf("failed to send initial empty delta: %w", err)
			}

			// Stream the JSON in chunks to simulate real streaming
			jsonStr := string(inputJSON)

			// Simulate streaming JSON in realistic chunks
			chunkSize := 15 // Approximate chunk size
			for j := 0; j < len(jsonStr); j += chunkSize {
				end := j + chunkSize
				if end > len(jsonStr) {
					end = len(jsonStr)
				}

				chunk := jsonStr[j:end]

				if err := handler.SendToolInputDelta(blockIndex, chunk); err != nil {
					LogError("Failed to send tool input chunk", err)
					return fmt.Errorf("failed to send tool input chunk: %w", err)
				}

				// Small delay between chunks
				time.Sleep(5 * time.Millisecond)
			}

			// Close tool block
			if err := handler.SendContentBlockStop(blockIndex); err != nil {
				LogError(fmt.Sprintf("Failed to close tool block index %d", blockIndex), err)
				return fmt.Errorf("failed to close tool block (index=%d): %w", blockIndex, err)
			}

			// Update tool tracking
			handler.state.LastToolIndex = blockIndex
			handler.state.ToolIndex = &blockIndex

		default:
			LogDebug(fmt.Sprintf("WARNING: Unknown block type: %s", block.Type))
		}
	}

	// Update token counts
	handler.state.InputTokens = anthResp.Usage.InputTokens
	handler.state.OutputTokens = anthResp.Usage.OutputTokens

	// Complete the stream with appropriate stop reason
	err = handler.Complete(anthResp.StopReason)
	if err != nil {
		LogError("Failed to complete simulated stream", err)
		return err
	}

	return nil
}

// sendError sends an error response
func (s *Server) sendError(w http.ResponseWriter, status int, message string) {
	// Log the error details
	LogError(fmt.Sprintf("HTTP %d error", status), fmt.Errorf("%s", message))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "error",
			"message": message,
		},
	})
}

// sendAPIError sends an APIError response
func (s *Server) sendAPIError(w http.ResponseWriter, err *APIError) {
	// Log the error with full context
	LogError(fmt.Sprintf("%s error in %s", err.Type, err.Operation), err)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.HTTPStatusCode())

	errorResp := map[string]interface{}{
		"error": map[string]interface{}{
			"type":    string(err.Type),
			"message": err.Message,
		},
	}

	// Add details if present
	if len(err.Details) > 0 {
		errorResp["error"].(map[string]interface{})["details"] = err.Details
	}

	_ = json.NewEncoder(w).Encode(errorResp)
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
