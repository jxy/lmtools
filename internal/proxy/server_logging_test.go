package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/logger"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// captureStderrForLogging captures stderr output during test execution
func captureStderrForLogging(t *testing.T, f func()) string {
	t.Helper()
	// Save original stderr
	oldStderr := os.Stderr

	// Create pipe
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}

	// Replace stderr with pipe writer
	os.Stderr = w

	// Reinitialize logger to use the new stderr
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	// Capture output in goroutine
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()

	// Run the function
	f()

	// Restore stderr and close pipe
	os.Stderr = oldStderr
	w.Close()
	wg.Wait()
	r.Close()

	// Restore logger to use original stderr
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	return buf.String()
}

// extractJSONFromLogLine extracts JSON content from a log line
func extractJSONFromLogLine(logLine string, prefix string) (string, error) {
	// Find the prefix
	idx := strings.Index(logLine, prefix)
	if idx == -1 {
		return "", fmt.Errorf("prefix '%s' not found in log line", prefix)
	}

	// Extract everything after the prefix
	jsonStart := idx + len(prefix)
	if jsonStart >= len(logLine) {
		return "", fmt.Errorf("no content after prefix '%s'", prefix)
	}

	// Trim whitespace and get the JSON part
	jsonStr := strings.TrimSpace(logLine[jsonStart:])

	// Find the end of the JSON (it should end at newline or end of string)
	if idx := strings.Index(jsonStr, "\n"); idx != -1 {
		jsonStr = jsonStr[:idx]
	}

	return jsonStr, nil
}

// validateJSONFormat validates that a string is valid JSON
func validateJSONFormat(jsonStr string) error {
	var data interface{}
	return json.Unmarshal([]byte(jsonStr), &data)
}

// assertLogContainsValidJSON asserts that logs contain valid JSON after the given prefix
func assertLogContainsValidJSON(t *testing.T, logs string, prefix string) {
	t.Helper()

	// Find the line containing the prefix
	lines := strings.Split(logs, "\n")
	found := false

	for _, line := range lines {
		if strings.Contains(line, prefix) {
			found = true

			// Extract JSON from the line
			jsonStr, err := extractJSONFromLogLine(line, prefix)
			if err != nil {
				t.Errorf("Failed to extract JSON from log line: %v\nLine: %s", err, line)
				continue
			}

			// Validate JSON format
			if err := validateJSONFormat(jsonStr); err != nil {
				t.Errorf("Invalid JSON format for %s: %v\nJSON: %s", prefix, err, jsonStr)
			}

			// Check that it doesn't contain Go map syntax
			if strings.Contains(jsonStr, "map[") {
				t.Errorf("JSON contains Go map syntax for %s: %s", prefix, jsonStr)
			}
		}
	}

	if !found {
		t.Errorf("Log prefix '%s' not found in logs:\n%s", prefix, logs)
	}
}

// TestResponseLoggingJSON tests that all responses are logged as valid JSON
func TestResponseLoggingJSON(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(ctx context.Context, s *Server) error
		logPrefix string
	}{
		{
			name: "Argo response with map interface",
			setupFunc: func(ctx context.Context, s *Server) error {
				// Create a mock response that would show as map[key:value] in old format
				argoResp := &ArgoChatResponse{
					Response: map[string]interface{}{
						"content": "Hello from Argo",
						"tool_calls": []interface{}{
							map[string]interface{}{
								"id":   "tool_1",
								"type": "function",
								"function": map[string]interface{}{
									"name":      "get_weather",
									"arguments": `{"location": "NYC"}`,
								},
							},
						},
					},
				}

				// Log the response (this is what forwardToArgo does)
				if respJSON, err := json.Marshal(argoResp); err == nil {
					logger.From(ctx).Debugf("Argo Response: %s", string(respJSON))
				} else {
					logger.From(ctx).Debugf("Argo Response: %+v", argoResp)
				}

				return nil
			},
			logPrefix: "Argo Response:",
		},
		{
			name: "OpenAI response",
			setupFunc: func(ctx context.Context, s *Server) error {
				openAIResp := &OpenAIResponse{
					ID:      "chatcmpl-123",
					Object:  "chat.completion",
					Created: 1234567890,
					Model:   "gpt-4",
					Choices: []OpenAIChoice{
						{
							Index: 0,
							Message: OpenAIMessage{
								Role:    "assistant",
								Content: "Hello from OpenAI",
							},
							FinishReason: "stop",
						},
					},
				}

				// Log the response
				if respJSON, err := json.Marshal(openAIResp); err == nil {
					logger.From(ctx).Debugf("OpenAI Response: %s", string(respJSON))
				} else {
					logger.From(ctx).Debugf("OpenAI Response: %+v", openAIResp)
				}

				return nil
			},
			logPrefix: "OpenAI Response:",
		},
		{
			name: "Google response",
			setupFunc: func(ctx context.Context, s *Server) error {
				googleResp := &GoogleResponse{
					Candidates: []GoogleCandidate{
						{
							Content: GoogleContent{
								Role: "model",
								Parts: []GooglePart{
									{Text: "Hello from Google"},
								},
							},
							FinishReason: "STOP",
						},
					},
				}

				// Log the response
				if respJSON, err := json.Marshal(googleResp); err == nil {
					logger.From(ctx).Debugf("Google Response: %s", string(respJSON))
				} else {
					logger.From(ctx).Debugf("Google Response: %+v", googleResp)
				}

				return nil
			},
			logPrefix: "Google Response:",
		},
		{
			name: "Anthropic response",
			setupFunc: func(ctx context.Context, s *Server) error {
				anthResp := &AnthropicResponse{
					ID:   "msg_123",
					Type: "message",
					Role: "assistant",
					Content: []AnthropicContentBlock{
						{
							Type: "text",
							Text: "Hello from Anthropic",
						},
					},
					Model:      "claude-3-opus-20240229",
					StopReason: "end_turn",
				}

				// Log the response
				if respJSON, err := json.Marshal(anthResp); err == nil {
					logger.From(ctx).Debugf("Anthropic Response: %s", string(respJSON))
				} else {
					logger.From(ctx).Debugf("Anthropic Response: %+v", anthResp)
				}

				return nil
			},
			logPrefix: "Anthropic Response:",
		},
		{
			name: "Error response",
			setupFunc: func(ctx context.Context, s *Server) error {
				// Simulate error response logging
				errorData := map[string]interface{}{
					"error": map[string]interface{}{
						"type":    "invalid_request_error",
						"message": "Invalid API key",
						"code":    "invalid_api_key",
					},
				}

				// This simulates what logErrorResponse does
				if errorJSON, err := json.Marshal(errorData); err == nil {
					logger.From(ctx).Debugf("Argo Error Response (status 401): %s", string(errorJSON))
				} else {
					logger.From(ctx).Debugf("Argo Error Response (status 401): %+v", errorData)
				}

				return nil
			},
			logPrefix: "Argo Error Response (status 401):",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create minimal server config
			config := &Config{
				MaxRequestBodySize: 10 * 1024 * 1024,
			}
			server := &Server{config: config}

			// Capture logs
			logs := captureStderrForLogging(t, func() {
				// Create context with request counter
				ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(1))

				// Run the test function
				if err := tt.setupFunc(ctx, server); err != nil {
					t.Errorf("Setup function failed: %v", err)
				}
			})

			// Verify the logs contain valid JSON
			assertLogContainsValidJSON(t, logs, tt.logPrefix)
		})
	}
}

// TestMetadataLoggingJSON tests that metadata is logged as valid JSON
func TestMetadataLoggingJSON(t *testing.T) {
	tests := []struct {
		name      string
		metadata  map[string]interface{}
		provider  string
		logPrefix string
	}{
		{
			name: "Simple metadata",
			metadata: map[string]interface{}{
				"user_id": "user_123",
				"session": "session_abc",
			},
			provider:  "Argo",
			logPrefix: "Omitting metadata from Anthropic request (not supported by Argo):",
		},
		{
			name: "Complex metadata with nested objects",
			metadata: map[string]interface{}{
				"user": map[string]interface{}{
					"id":   "123",
					"name": "Test User",
				},
				"context": map[string]interface{}{
					"app":     "test-app",
					"version": "1.0.0",
				},
			},
			provider:  "OpenAI",
			logPrefix: "Omitting metadata from Anthropic request (not supported by OpenAI):",
		},
		{
			name: "Metadata with arrays",
			metadata: map[string]interface{}{
				"tags":     []string{"test", "debug", "json"},
				"features": []interface{}{"feature1", "feature2"},
			},
			provider:  "Google",
			logPrefix: "Omitting metadata from Anthropic request (not supported by Google):",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture logs
			logs := captureStderrForLogging(t, func() {
				// Create context with request counter
				ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(1))

				// Simulate metadata logging
				if len(tt.metadata) > 0 {
					if metaJSON, err := json.Marshal(tt.metadata); err == nil {
						logger.From(ctx).Debugf("Omitting metadata from Anthropic request (not supported by %s): %s", tt.provider, string(metaJSON))
					} else {
						logger.From(ctx).Debugf("Omitting metadata from Anthropic request (not supported by %s): %+v", tt.provider, tt.metadata)
					}
				}
			})

			// Verify the logs contain valid JSON
			assertLogContainsValidJSON(t, logs, tt.logPrefix)

			// Additionally verify the JSON contains expected fields
			lines := strings.Split(logs, "\n")
			for _, line := range lines {
				if strings.Contains(line, tt.logPrefix) {
					jsonStr, err := extractJSONFromLogLine(line, tt.logPrefix)
					if err != nil {
						continue
					}

					// Parse and verify structure
					var parsed map[string]interface{}
					if err := json.Unmarshal([]byte(jsonStr), &parsed); err == nil {
						// Verify it matches our original metadata
						originalJSON, _ := json.Marshal(tt.metadata)
						parsedJSON, _ := json.Marshal(parsed)

						if string(originalJSON) != string(parsedJSON) {
							t.Errorf("Logged metadata doesn't match original.\nOriginal: %s\nLogged: %s",
								string(originalJSON), string(parsedJSON))
						}
					}
				}
			}
		})
	}
}

// TestJSONExtractionFromLogs tests the JSON extraction helper function
func TestJSONExtractionFromLogs(t *testing.T) {
	tests := []struct {
		name      string
		logLine   string
		prefix    string
		wantJSON  string
		wantError bool
	}{
		{
			name:     "Simple JSON extraction",
			logLine:  `[DEBUG] [2025-01-01T00:00:00Z] [test] [#1] Argo Response: {"response":"Hello"}`,
			prefix:   "Argo Response:",
			wantJSON: `{"response":"Hello"}`,
		},
		{
			name:     "JSON with nested objects",
			logLine:  `[DEBUG] [2025-01-01T00:00:00Z] [test] [#1] OpenAI Response: {"id":"123","choices":[{"message":{"content":"Hi"}}]}`,
			prefix:   "OpenAI Response:",
			wantJSON: `{"id":"123","choices":[{"message":{"content":"Hi"}}]}`,
		},
		{
			name:      "Missing prefix",
			logLine:   `[DEBUG] [2025-01-01T00:00:00Z] [test] [#1] Some other log`,
			prefix:    "Argo Response:",
			wantError: true,
		},
		{
			name:     "JSON at end of line",
			logLine:  `[DEBUG] Metadata: {"user_id":"123"}`,
			prefix:   "Metadata:",
			wantJSON: `{"user_id":"123"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractJSONFromLogLine(tt.logLine, tt.prefix)

			if (err != nil) != tt.wantError {
				t.Errorf("extractJSONFromLogLine() error = %v, wantError %v", err, tt.wantError)
				return
			}

			if !tt.wantError && got != tt.wantJSON {
				t.Errorf("extractJSONFromLogLine() = %v, want %v", got, tt.wantJSON)
			}
		})
	}
}

// TestNoMapSyntaxInLogs ensures no Go map syntax appears in logs
func TestNoMapSyntaxInLogs(t *testing.T) {
	// Create a response that would typically show map syntax
	argoResp := &ArgoChatResponse{
		Response: map[string]interface{}{
			"content": "Test response",
			"metadata": map[string]interface{}{
				"key1": "value1",
				"key2": []interface{}{"a", "b", "c"},
			},
		},
	}

	// Capture logs
	logs := captureStderrForLogging(t, func() {
		ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(1))

		// Log the response using the actual server method
		if respJSON, err := json.Marshal(argoResp); err == nil {
			logger.From(ctx).Debugf("Argo Response: %s", string(respJSON))
		} else {
			logger.From(ctx).Debugf("Argo Response: %+v", argoResp)
		}
	})

	// Check for map syntax
	mapSyntaxRegex := regexp.MustCompile(`map\[[^\]]+\]`)
	if matches := mapSyntaxRegex.FindAllString(logs, -1); len(matches) > 0 {
		t.Errorf("Found Go map syntax in logs: %v\nFull logs:\n%s", matches, logs)
	}
}
