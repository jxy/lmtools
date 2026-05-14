package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMapperAnthropicProvider tests model mapping for Anthropic provider
func TestMapperAnthropicProvider(t *testing.T) {
	tests := []struct {
		name         string
		config       *Config
		inputModel   string
		wantProvider string
		wantModel    string
	}{
		{
			name: "anthropic provider with claude model passes through without model map",
			config: &Config{
				Provider:        constants.ProviderAnthropic,
				AnthropicAPIKey: "test-key",
			},
			inputModel:   "claude-3-opus-20240229",
			wantProvider: constants.ProviderAnthropic,
			wantModel:    "claude-3-opus-20240229",
		},
		{
			name: "anthropic provider with haiku model passes through without model map",
			config: &Config{
				Provider:        constants.ProviderAnthropic,
				AnthropicAPIKey: "test-key",
			},
			inputModel:   "claude-3-haiku-20240307",
			wantProvider: constants.ProviderAnthropic,
			wantModel:    "claude-3-haiku-20240307",
		},
		{
			name: "anthropic provider uses explicit model map",
			config: &Config{
				Provider:        constants.ProviderAnthropic,
				AnthropicAPIKey: "test-key",
				ModelMapRules:   []ModelMapRule{mustModelMapRule(t, "^claude-3-opus.*=claude-3-opus-20240229")},
			},
			inputModel:   "claude-3-opus",
			wantProvider: constants.ProviderAnthropic,
			wantModel:    "claude-3-opus-20240229",
		},
		{
			name: "anthropic provider with non-claude model",
			config: &Config{
				Provider:        constants.ProviderAnthropic,
				AnthropicAPIKey: "test-key",
			},
			inputModel:   "gpt-4",
			wantProvider: constants.ProviderAnthropic,
			wantModel:    "gpt-4",
		},
		{
			name: "anthropic provider maps haiku only with explicit model map",
			config: &Config{
				Provider:        constants.ProviderAnthropic,
				AnthropicAPIKey: "test-key",
				ModelMapRules:   []ModelMapRule{mustModelMapRule(t, "^claude-3-haiku.*=claude-3-haiku-20240307")},
			},
			inputModel:   "claude-3-haiku-20240307",
			wantProvider: constants.ProviderAnthropic,
			wantModel:    "claude-3-haiku-20240307",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := NewModelMapper(tt.config)
			gotModel := mapper.MapModel(tt.inputModel)
			gotProvider := tt.config.Provider

			if gotProvider != tt.wantProvider {
				t.Errorf("MapModel() provider = %v, want %v", gotProvider, tt.wantProvider)
			}
			if gotModel != tt.wantModel {
				t.Errorf("MapModel() model = %v, want %v", gotModel, tt.wantModel)
			}
		})
	}
}

// TestConfigValidationAnthropic tests config validation for Anthropic provider
func TestConfigValidationAnthropic(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid anthropic config",
			config: &Config{
				Provider:        constants.ProviderAnthropic,
				AnthropicAPIKey: "test-key",
			},
			wantErr: false,
		},
		{
			name: "anthropic without key but with provider URL",
			config: &Config{
				Provider:    constants.ProviderAnthropic,
				ProviderURL: "https://custom-anthropic.com",
			},
			wantErr: false,
		},
		{
			name: "anthropic without key or provider URL",
			config: &Config{
				Provider: constants.ProviderAnthropic,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestForwardToAnthropic tests the forwardToAnthropic function
func TestForwardToAnthropic(t *testing.T) {
	// Initialize logger for testing
	SetupTestLogger(t)

	// Create a mock Anthropic server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.Header.Get("x-api-key") != "test-anthropic-key" {
			t.Errorf("Expected x-api-key header, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("Expected anthropic-version header, got %s", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type header, got %s", r.Header.Get("Content-Type"))
		}

		// Parse request
		var req AnthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
		}

		// Send response
		resp := AnthropicResponse{
			ID:    "msg_test123",
			Type:  "message",
			Role:  "assistant",
			Model: req.Model,
			Content: []AnthropicContentBlock{
				{
					Type: "text",
					Text: "Hello from Anthropic!",
				},
			},
			StopReason: "end_turn",
			Usage: &AnthropicUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	}))
	defer mockServer.Close()

	// Create server with mock config
	config := &Config{
		Provider:        constants.ProviderAnthropic,
		AnthropicAPIKey: "test-anthropic-key",
		ProviderURL:     mockServer.URL,
	}

	s := &Server{
		config:    config,
		endpoints: &Endpoints{Anthropic: mockServer.URL},
		mapper:    NewModelMapper(config),
		converter: NewConverter(NewModelMapper(config)),
		client:    retry.NewClient(5*time.Second, logger.GetLogger()),
	}

	// Test request
	anthReq := &AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	ctx := context.Background()
	resp, err := s.forwardToAnthropic(ctx, anthReq)
	if err != nil {
		t.Fatalf("forwardToAnthropic() error = %v", err)
	}

	// Verify response
	if resp.ID != "msg_test123" {
		t.Errorf("Expected ID msg_test123, got %s", resp.ID)
	}
	if len(resp.Content) != 1 {
		t.Errorf("Expected 1 content block, got %d", len(resp.Content))
	}
	if resp.Content[0].Text != "Hello from Anthropic!" {
		t.Errorf("Expected text 'Hello from Anthropic!', got %s", resp.Content[0].Text)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("Expected tokens 10/5, got %d/%d", resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}
}

// TestStreamFromAnthropic tests the streamFromAnthropic function
func TestStreamFromAnthropic(t *testing.T) {
	// Initialize logger for testing
	SetupTestLogger(t)

	// Create a mock Anthropic streaming server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.Header.Get("x-api-key") != "test-anthropic-key" {
			t.Errorf("Expected x-api-key header, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Expected Accept: text/event-stream, got %s", r.Header.Get("Accept"))
		}

		// Parse request
		var req AnthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
		}

		// Verify streaming is enabled
		if !req.Stream {
			t.Errorf("Expected stream=true in request")
		}

		// Send SSE response
		setSSEHeaders(w)

		// Send events
		events := []struct {
			event string
			data  string
		}{
			{"message_start", `{"message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-3-opus-20240229","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}},"type":"message_start"}`},
			{"content_block_start", `{"index":0,"content_block":{"type":"text","text":""},"type":"content_block_start"}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"Hello "},"type":"content_block_delta"}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"from "},"type":"content_block_delta"}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"streaming!"},"type":"content_block_delta"}`},
			{"content_block_stop", `{"index":0,"type":"content_block_stop"}`},
			{"message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3},"type":"message_delta"}`},
			{"message_stop", `{"type":"message_stop"}`},
		}

		for _, e := range events {
			fmt.Fprintf(w, "event: %s\n", e.event)
			fmt.Fprintf(w, "data: %s\n\n", e.data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer mockServer.Close()

	// Create server with mock config
	config := &Config{
		Provider:        constants.ProviderAnthropic,
		AnthropicAPIKey: "test-anthropic-key",
		ProviderURL:     mockServer.URL,
	}

	s := &Server{
		config:    config,
		endpoints: &Endpoints{Anthropic: mockServer.URL},
		mapper:    NewModelMapper(config),
		converter: NewConverter(NewModelMapper(config)),
		client:    retry.NewClient(5*time.Second, logger.GetLogger()),
	}

	// Create a test response writer
	w := httptest.NewRecorder()

	// Create handler
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(w, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Initialize handler with required events
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message_start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content_block_start: %v", err)
	}

	// Test request
	anthReq := &AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	// Stream response
	err = s.streamFromAnthropic(ctx, anthReq, handler)
	if err != nil {
		t.Fatalf("streamFromAnthropic() error = %v", err)
	}

	// Verify accumulated text
	if handler.state.AccumulatedText != "Hello from streaming!" {
		t.Errorf("Expected text 'Hello from streaming!', got '%s'", handler.state.AccumulatedText)
	}
	if handler.state.OutputTokens != 3 {
		t.Errorf("Expected 3 output tokens, got %d", handler.state.OutputTokens)
	}
}

// TestAnthropicIntegration tests the full integration with Anthropic provider
func TestAnthropicIntegration(t *testing.T) {
	// Create a mock Anthropic server
	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route based on streaming
		var req AnthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
			return
		}

		if req.Stream {
			// Handle streaming
			setSSEHeaders(w)
			fmt.Fprintf(w, "event: message_start\n")
			fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3-opus-20240229\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_start\n")
			fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\n")
			fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Streamed response\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\n")
			fmt.Fprintf(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_stop\n")
			fmt.Fprintf(w, "data: {\"type\":\"message_stop\"}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		} else {
			// Handle non-streaming
			resp := AnthropicResponse{
				ID:   "msg_test",
				Type: "message",
				Role: "assistant",
				Content: []AnthropicContentBlock{
					{Type: "text", Text: "Non-streamed response"},
				},
				Usage: &AnthropicUsage{InputTokens: 5, OutputTokens: 3},
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Errorf("Failed to encode response: %v", err)
			}
		}
	}))
	defer mockAnthropic.Close()

	// Create apiproxy server
	config := &Config{
		Provider:            constants.ProviderAnthropic,
		AnthropicAPIKey:     "test-key",
		ProviderURL:         mockAnthropic.URL,
		MaxRequestBodySize:  10 * 1024 * 1024, // 10MB
		MaxResponseBodySize: 10 * 1024 * 1024, // 10MB
	}
	// Create server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	// Test non-streaming request
	t.Run("non-streaming", func(t *testing.T) {
		reqBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		var resp AnthropicResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Errorf("Failed to decode response: %v", err)
		}
		if len(resp.Content) != 1 || resp.Content[0].Text != "Non-streamed response" {
			t.Errorf("Unexpected response: %+v", resp)
		}
	})

	// Test streaming request
	t.Run("streaming", func(t *testing.T) {
		reqBody := `{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"Hello"}],"max_tokens":100,"stream":true}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		// Verify SSE format
		body := w.Body.String()
		if !strings.Contains(body, "event: message_start") {
			t.Errorf("Missing message_start event in response")
		}
		if !strings.Contains(body, "Streamed response") {
			t.Errorf("Missing streamed text in response")
		}
	})
}
