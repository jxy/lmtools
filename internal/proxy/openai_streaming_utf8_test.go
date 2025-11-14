package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSimulateOpenAIStreamFromArgoUTF8 tests that simulateOpenAIStreamFromArgo respects UTF-8 boundaries
func TestSimulateOpenAIStreamFromArgoUTF8(t *testing.T) {
	// Initialize logger
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	tests := []struct {
		name     string
		response string
		desc     string
	}{
		{
			name:     "emoji_content",
			response: "Hello 😀 World 🌍! Here are some emojis: 🚀🎉🎯",
			desc:     "response with 4-byte UTF-8 emojis",
		},
		{
			name:     "chinese_content",
			response: "你好世界！这是一个测试。中文字符应该正确处理。",
			desc:     "response with Chinese characters (3-byte UTF-8)",
		},
		{
			name:     "japanese_content",
			response: "こんにちは世界！これはテストです。日本語の文字も正しく処理されるべきです。",
			desc:     "response with Japanese characters (3-byte UTF-8)",
		},
		{
			name:     "arabic_content",
			response: "مرحبا بالعالم! هذا اختبار. يجب معالجة الأحرف العربية بشكل صحيح.",
			desc:     "response with Arabic text (2-byte UTF-8)",
		},
		{
			name:     "mixed_content",
			response: "Mixed content: English, 中文, 日本語, Русский, العربية, 🚀 emojis 🎯",
			desc:     "response with mixed scripts and emojis",
		},
		{
			name:     "mathematical_symbols",
			response: "Mathematical: ∑ ∏ ∫ ∂ ∇ ≈ ≠ ≤ ≥ ∈ ∉ ⊂ ⊃ ∪ ∩",
			desc:     "response with mathematical symbols",
		},
		{
			name:     "long_emoji_sequence",
			response: "Emojis: 😀😃😄😁😆😅😂🤣😊😇🙂🙃😉😌😍🥰😘😗😙😚😋😛😜🤪😝",
			desc:     "response with long sequence of emojis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock Argo server
			mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Return response with UTF-8 content
				resp := ArgoChatResponse{
					Response: tt.response,
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Fatalf("Failed to encode response: %v", err)
				}
			}))
			defer mockArgo.Close()

			// Create config
			config := &Config{
				ArgoUser:    "testuser",
				ArgoEnv:     mockArgo.URL,
				ArgoBaseURL: mockArgo.URL,
			}

			// Create server
			mapper := NewModelMapper(config)
			server := &Server{
				config:    config,
				mapper:    mapper,
				converter: NewConverter(mapper),
				client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
			}

			// Create response recorder
			recorder := httptest.NewRecorder()

			// Create OpenAI stream writer
			ctx := context.Background()
			writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
			if err != nil {
				t.Fatalf("Failed to create OpenAI stream writer: %v", err)
			}

			// Create request
			anthReq := &AnthropicRequest{
				Model:  "gpt-4",
				Stream: true,
				Messages: []AnthropicMessage{{
					Role:    "user",
					Content: json.RawMessage(`"Test message"`),
				}},
				MaxTokens: 100,
			}

			// Execute simulated streaming
			if err := server.simulateOpenAIStreamFromArgo(ctx, anthReq, writer); err != nil {
				t.Fatalf("simulateOpenAIStreamFromArgo failed: %v", err)
			}

			// Parse the SSE response
			response := recorder.Body.String()
			lines := strings.Split(response, "\n")

			var chunks []string
			sawInitialAssistant := false
			sawFinalStop := false
			for _, line := range lines {
				if strings.HasPrefix(line, "data: ") {
					data := strings.TrimPrefix(line, "data: ")
					if data == "" || data == "[DONE]" {
						continue
					}

					// Parse the JSON
					var chunk map[string]interface{}
					if err := json.Unmarshal([]byte(data), &chunk); err != nil {
						continue // Skip non-JSON lines
					}

					// Extract content from choices
					if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
						if choice, ok := choices[0].(map[string]interface{}); ok {
							if delta, ok := choice["delta"].(map[string]interface{}); ok {
								// Accept content either as a non-empty string OR as null on first delta
								if contentVal, hasContent := delta["content"]; hasContent {
									if contentStr, ok := contentVal.(string); ok && contentStr != "" {
										chunks = append(chunks, contentStr)
										if !utf8.ValidString(contentStr) {
											t.Errorf("Chunk contains invalid UTF-8: %q (bytes: %v)", contentStr, []byte(contentStr))
										}
									} else if contentVal == nil {
										if role, ok := delta["role"].(string); ok && role == "assistant" {
											sawInitialAssistant = true
										}
									}
								}
								// For intermediate chunks, finish_reason should be null
								if _, ok := choice["finish_reason"]; ok {
									if choice["finish_reason"] != nil {
										// Only final chunk contains a non-null finish_reason
										if fr, _ := choice["finish_reason"].(string); fr != "stop" {
											t.Errorf("Expected intermediate finish_reason null; got: %v", choice["finish_reason"])
										}
									}
								}
							}
							// Catch finish_reason on the same chunk
							if fr, ok := choice["finish_reason"].(string); ok && fr == "stop" {
								sawFinalStop = true
							}
						}
					}
				}
			}

			// Reconstruct the text from chunks
			reconstructed := strings.Join(chunks, "")

			// Verify we got the original content back
			if reconstructed != tt.response {
				t.Errorf("Reconstructed text doesn't match original\nGot:  %q\nWant: %q", reconstructed, tt.response)
			}
			if !sawInitialAssistant {
				t.Error("Did not see initial assistant delta with content:null")
			}
			if !sawFinalStop {
				t.Error("Did not see final finish_reason=stop")
			}

			// Verify each chunk is valid UTF-8
			for i, chunk := range chunks {
				if !utf8.ValidString(chunk) {
					t.Errorf("Chunk %d contains invalid UTF-8: %q", i, chunk)
				}
			}

			t.Logf("Successfully streamed %s: %d chunks, all valid UTF-8", tt.desc, len(chunks))
		})
	}
}

// TestSimulateOpenAIStreamFromArgoWithTools tests UTF-8 handling with tool responses
func TestSimulateOpenAIStreamFromArgoWithTools(t *testing.T) {
	// Initialize logger
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create a mock Argo server that returns both text and tool use
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return response with UTF-8 content
		// For tool responses, Argo returns a complex structure
		resp := ArgoChatResponse{
			Response: map[string]interface{}{
				"content": "Analysis complete: 分析完成 🎯",
				"tool_calls": []map[string]interface{}{
					{
						"id":   "tool_123",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "search",
							"arguments": `{"query":"Tokyo weather 東京の天気 🌸","location":"東京"}`,
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer mockArgo.Close()

	// Create config
	config := &Config{
		ArgoUser:    "testuser",
		ArgoEnv:     mockArgo.URL,
		ArgoBaseURL: mockArgo.URL,
	}

	// Create server
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Create response recorder
	recorder := httptest.NewRecorder()

	// Create OpenAI stream writer
	ctx := context.Background()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
	if err != nil {
		t.Fatalf("Failed to create OpenAI stream writer: %v", err)
	}

	// Create request with tools
	anthReq := &AnthropicRequest{
		Model:  "gpt-4",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Search for Tokyo weather"`),
		}},
		Tools: []AnthropicTool{
			{
				Name:        "search",
				Description: "Search for information",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		},
		MaxTokens: 100,
	}

	// Execute simulated streaming
	if err := server.simulateOpenAIStreamFromArgo(ctx, anthReq, writer); err != nil {
		t.Fatalf("simulateOpenAIStreamFromArgo failed: %v", err)
	}

	// Parse the SSE response
	response := recorder.Body.String()
	lines := strings.Split(response, "\n")

	var textChunks []string
	var toolCallFound bool
	var sawInitialEmpty bool
	var argConcat string
	var sawFinishToolCalls bool
	var sawInitialAssistant bool
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "" || data == "[DONE]" {
				continue
			}

			// Parse the JSON
			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			// Extract content from choices
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						// First delta: role assistant with content:null
						if role, ok := delta["role"].(string); ok && role == "assistant" {
							if _, hasContent := delta["content"]; hasContent {
								if delta["content"] == nil {
									sawInitialAssistant = true
								}
							}
						}
						// Check for text content
						if content, ok := delta["content"].(string); ok && content != "" {
							textChunks = append(textChunks, content)

							// Verify valid UTF-8
							if !utf8.ValidString(content) {
								t.Errorf("Text chunk contains invalid UTF-8: %q", content)
							}
						}

						// Check for tool calls
						if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
							toolCallFound = true
							// Verify tool call arguments contain valid UTF-8
							for _, tc := range toolCalls {
								if tcMap, ok := tc.(map[string]interface{}); ok {
									if fn, ok := tcMap["function"].(map[string]interface{}); ok {
										if args, ok := fn["arguments"].(string); ok {
											if args == "" { // initial empty arguments chunk
												sawInitialEmpty = true
												continue
											}
											if !utf8.ValidString(args) {
												t.Errorf("Tool arguments contain invalid UTF-8: %q", args)
											}
											argConcat += args
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Reconstruct the text
	reconstructedText := strings.Join(textChunks, "")

	// Verify text content
	expectedText := "Analysis complete: 分析完成 🎯"
	if reconstructedText != expectedText {
		t.Errorf("Reconstructed text doesn't match\nGot:  %q\nWant: %q", reconstructedText, expectedText)
	}

	// Verify tool call was found in the stream
	if !toolCallFound {
		t.Error("Tool call was not found in the stream")
	}
	if !sawInitialAssistant {
		t.Error("Did not see initial assistant delta with content:null")
	}
	if !sawInitialEmpty {
		t.Error("Did not see initial empty arguments chunk for tool call")
	}
	// Ensure finish_reason: tool_calls appeared and intermediate chunks have finish_reason null
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "" || data == "[DONE]" {
				continue
			}
			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if fr, ok := choice["finish_reason"].(string); ok && fr == "tool_calls" {
						sawFinishToolCalls = true
						break
					}
					// Intermediate chunks should present finish_reason key and be null
					if _, present := choice["finish_reason"]; present {
						if choice["finish_reason"] != nil {
							if fr, _ := choice["finish_reason"].(string); fr != "tool_calls" {
								t.Errorf("Expected intermediate finish_reason null; got: %v", choice["finish_reason"])
							}
						}
					}
				}
			}
		}
	}
	if !sawFinishToolCalls {
		t.Error("Did not see finish_reason=tool_calls during stream")
	}

	t.Logf("Successfully streamed mixed content with tools: %d text chunks, tool calls found", len(textChunks))
}

// TestOpenAIStreamingChunkBoundaries tests that chunk boundaries don't split UTF-8 characters
func TestOpenAIStreamingChunkBoundaries(t *testing.T) {
	// Initialize logger
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Create a string that will likely be split at a multi-byte character boundary
	// The chunk size is 50 bytes, so we position multi-byte characters around that boundary
	// "Test content with " = 18 bytes
	// "some padding text " = 18 bytes  (total: 36 bytes)
	// "here 你好世界" - "here " = 5 bytes (total: 41 bytes), then Chinese chars
	response := "Test content with some padding text here 你好世界 and more content 🚀 after"

	// Create a mock Argo server
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ArgoChatResponse{
			Response: response,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer mockArgo.Close()

	// Create config
	config := &Config{
		ArgoUser:    "testuser",
		ArgoEnv:     mockArgo.URL,
		ArgoBaseURL: mockArgo.URL,
	}

	// Create server
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Create response recorder
	recorder := httptest.NewRecorder()

	// Create OpenAI stream writer
	ctx := context.Background()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
	if err != nil {
		t.Fatalf("Failed to create OpenAI stream writer: %v", err)
	}

	// Create request
	anthReq := &AnthropicRequest{
		Model:  "gpt-4",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Test message"`),
		}},
		MaxTokens: 100,
	}

	// Execute simulated streaming
	if err := server.simulateOpenAIStreamFromArgo(ctx, anthReq, writer); err != nil {
		t.Fatalf("simulateOpenAIStreamFromArgo failed: %v", err)
	}

	// Parse the SSE response
	responseStr := recorder.Body.String()
	lines := strings.Split(responseStr, "\n")

	var chunks []string
	invalidUTF8Found := false
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "" || data == "[DONE]" {
				continue
			}

			// Check if the raw data contains replacement character
			if strings.Contains(data, "\ufffd") {
				t.Errorf("Found UTF-8 replacement character in chunk: %q", data)
				invalidUTF8Found = true
			}

			// Parse the JSON
			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			// Extract content
			if choices, ok := chunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if content, ok := delta["content"].(string); ok && content != "" {
							chunks = append(chunks, content)

							// Verify valid UTF-8
							if !utf8.ValidString(content) {
								t.Errorf("Chunk contains invalid UTF-8: %q (bytes: %v)", content, []byte(content))
								invalidUTF8Found = true
							}

							// Check for replacement character
							if strings.Contains(content, "\ufffd") {
								t.Errorf("Chunk contains UTF-8 replacement character: %q", content)
								invalidUTF8Found = true
							}
						}
					}
				}
			}
		}
	}

	// Reconstruct and verify
	reconstructed := strings.Join(chunks, "")
	if reconstructed != response {
		t.Errorf("Reconstructed text doesn't match\nGot:  %q\nWant: %q", reconstructed, response)
	}

	if invalidUTF8Found {
		t.Error("Invalid UTF-8 or replacement characters were found in the stream")
	}

	t.Logf("Successfully handled chunk boundaries: %d chunks created, no UTF-8 splitting detected", len(chunks))
}
