package proxy

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOmittedFieldsLogging(t *testing.T) {
	tests := []struct {
		name           string
		setupRequest   func() *AnthropicRequest
		targetProvider string
		expectedLogs   []string
	}{
		{
			name: "OpenAI omits top_k and metadata",
			setupRequest: func() *AnthropicRequest {
				topK := 10
				return &AnthropicRequest{
					Model:     "claude-3-opus-20240229",
					MaxTokens: 100,
					TopK:      &topK,
					Metadata:  map[string]interface{}{"user_id": "12345", "session": "abc"},
					Messages: []AnthropicMessage{
						{
							Role:    core.RoleUser,
							Content: json.RawMessage(`"Hello"`),
						},
					},
				}
			},
			targetProvider: constants.ProviderOpenAI,
			expectedLogs: []string{
				"Omitting top_k=10 from Anthropic request (not supported by OpenAI)",
				"Omitting metadata from Anthropic request (not supported by OpenAI)",
			},
		},
		{
			name: "Google omits metadata and tool_choice",
			setupRequest: func() *AnthropicRequest {
				return &AnthropicRequest{
					Model:     "claude-3-opus-20240229",
					MaxTokens: 100,
					Metadata:  map[string]interface{}{"request_id": "xyz"},
					ToolChoice: &AnthropicToolChoice{
						Type: "tool",
						Name: "get_weather",
					},
					Messages: []AnthropicMessage{
						{
							Role:    core.RoleUser,
							Content: json.RawMessage(`"What's the weather?"`),
						},
					},
				}
			},
			targetProvider: constants.ProviderGoogle,
			expectedLogs: []string{
				"Omitting metadata from Anthropic request (not supported by Google)",
				"Omitting tool_choice from Anthropic request (Google uses different tool configuration): type=tool, name=get_weather",
			},
		},
		{
			name: "Argo omits top_k and metadata",
			setupRequest: func() *AnthropicRequest {
				topK := 5
				return &AnthropicRequest{
					Model:     "claude-3-opus-20240229",
					MaxTokens: 100,
					TopK:      &topK,
					Metadata:  map[string]interface{}{"trace_id": "trace123"},
					Messages: []AnthropicMessage{
						{
							Role:    core.RoleUser,
							Content: json.RawMessage(`"Hello Argo"`),
						},
					},
				}
			},
			targetProvider: constants.ProviderArgo,
			expectedLogs: []string{
				"Omitting top_k=5 from Anthropic request (not supported by Argo)",
				"Omitting metadata from Anthropic request (not supported by Argo)",
			},
		},
		{
			name: "No omitted fields",
			setupRequest: func() *AnthropicRequest {
				return &AnthropicRequest{
					Model:     "claude-3-opus-20240229",
					MaxTokens: 100,
					Messages: []AnthropicMessage{
						{
							Role:    core.RoleUser,
							Content: json.RawMessage(`"Simple message"`),
						},
					},
				}
			},
			targetProvider: constants.ProviderOpenAI,
			expectedLogs:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary directory for test logs
			tempDir := t.TempDir()

			// Reset logger for testing
			logger.ResetForTesting()

			// Initialize logger with debug level to capture debug logs
			err := logger.InitializeWithOptions(
				logger.WithLogDir(tempDir),
				logger.WithLevel("debug"),
				logger.WithFormat("text"),
				logger.WithStderr(true),
				logger.WithFile(true),
			)
			if err != nil {
				t.Fatalf("Failed to initialize logger: %v", err)
			}
			defer logger.Close()

			// Create converter with minimal config
			config := &Config{
				Provider: constants.ProviderOpenAI,
			}
			mapper := NewModelMapper(config)
			converter := NewConverter(mapper)

			// Setup request
			req := tt.setupRequest()

			// Convert based on target provider
			switch tt.targetProvider {
			case constants.ProviderOpenAI:
				_, err := converter.ConvertAnthropicToOpenAI(context.Background(), req)
				if err != nil {
					t.Fatalf("Failed to convert to OpenAI: %v", err)
				}
			case constants.ProviderGoogle:
				_, err := converter.ConvertAnthropicToGoogle(context.Background(), req)
				if err != nil {
					t.Fatalf("Failed to convert to Google: %v", err)
				}
			case constants.ProviderArgo:
				_, err := converter.ConvertAnthropicToArgo(context.Background(), req, "testuser")
				if err != nil {
					t.Fatalf("Failed to convert to Argo: %v", err)
				}
			}

			// Read the log file to check for expected logs
			logFiles, err := filepath.Glob(filepath.Join(tempDir, "*.log"))
			if err != nil {
				t.Fatalf("Failed to glob log files: %v", err)
			}

			// If no logs are expected and no log file was created (lazy initialization), that's fine
			if len(tt.expectedLogs) == 0 && len(logFiles) == 0 {
				// No logs expected and no log file created - this is correct behavior
				return
			}

			// If we expect logs, there should be a log file
			if len(tt.expectedLogs) > 0 && len(logFiles) == 0 {
				t.Fatalf("Expected logs but no log files found in %s", tempDir)
			}

			// If a log file exists, read and check it
			if len(logFiles) > 0 {
				// Read the log file
				logContent, err := os.ReadFile(logFiles[0])
				if err != nil {
					t.Fatalf("Failed to read log file: %v", err)
				}

				logOutput := string(logContent)

				// Check logs
				for _, expectedLog := range tt.expectedLogs {
					if !strings.Contains(logOutput, expectedLog) {
						t.Errorf("Expected log not found: %s\nActual logs:\n%s", expectedLog, logOutput)
					}
				}

				// Also check that we don't have unexpected logs
				if len(tt.expectedLogs) == 0 && strings.Contains(logOutput, "Omitting") {
					t.Errorf("Unexpected omission logs found when none expected:\n%s", logOutput)
				}
			}
		})
	}
}

// TestOmittedFieldsLoggingWithoutFile tests that debug logs are written to stderr when no log file is configured
func TestOmittedFieldsLoggingWithoutFile(t *testing.T) {
	// Capture stderr BEFORE reinitializing logger to avoid race conditions
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Reinitialize logger with stderr output after setting up the pipe
	// This ensures the logger will write to our pipe
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	// Create converter and test request with minimal config
	config := &Config{
		Provider: constants.ProviderOpenAI,
	}
	mapper := NewModelMapper(config)
	converter := NewConverter(mapper)

	// Create a context to pass to the converter
	ctx := context.Background()

	topK := 10
	req := &AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 100,
		TopK:      &topK,
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	// Convert to trigger logging
	_, err := converter.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("Failed to convert: %v", err)
	}

	// Close the writer to signal EOF
	w.Close()

	// Read all captured output
	captured, _ := io.ReadAll(r)

	// Restore stderr
	os.Stderr = oldStderr

	// Restore the logger to its original state for other tests
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	// Check that debug log was written to stderr
	capturedStr := string(captured)
	if !strings.Contains(capturedStr, "Omitting top_k=10") {
		t.Errorf("Expected debug log not found in stderr output: %s", capturedStr)
		// Also check if the log contains the request ID prefix
		if strings.Contains(capturedStr, "[#") {
			t.Logf("Found request ID prefix in output")
		}
		// Log what we actually captured
		t.Logf("Captured output length: %d", len(capturedStr))
		if len(capturedStr) > 0 {
			t.Logf("First 200 chars: %s", capturedStr[:min(200, len(capturedStr))])
		}
	}
}

// TestOmittedFieldsLoggingJSON tests that omitted fields are logged in valid JSON format
func TestOmittedFieldsLoggingJSON(t *testing.T) {
	tests := []struct {
		name           string
		setupRequest   func() *AnthropicRequest
		targetProvider string
		expectedJSON   map[string]interface{} // Expected JSON structure for metadata
	}{
		{
			name: "Complex metadata with nested objects",
			setupRequest: func() *AnthropicRequest {
				return &AnthropicRequest{
					Model:     "claude-3-opus-20240229",
					MaxTokens: 100,
					Metadata: map[string]interface{}{
						"user": map[string]interface{}{
							"id":   "123",
							"name": "Test User",
						},
						"session": "abc-123",
						"tags":    []string{"test", "debug"},
					},
					Messages: []AnthropicMessage{
						{
							Role:    core.RoleUser,
							Content: json.RawMessage(`"Test message"`),
						},
					},
				}
			},
			targetProvider: constants.ProviderArgo,
			expectedJSON: map[string]interface{}{
				"user": map[string]interface{}{
					"id":   "123",
					"name": "Test User",
				},
				"session": "abc-123",
				"tags":    []interface{}{"test", "debug"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stderr
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			// Reset and reinitialize logger
			logger.ResetForTesting()
			_ = logger.InitializeWithOptions(
				logger.WithLevel("debug"),
				logger.WithFormat("text"),
				logger.WithStderr(true),
				logger.WithFile(false),
				logger.WithComponent("test"),
			)

			// Create converter
			config := &Config{Provider: tt.targetProvider}
			mapper := NewModelMapper(config)
			converter := NewConverter(mapper)

			// Create context with request counter
			ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(1))

			// Setup and convert request
			req := tt.setupRequest()
			switch tt.targetProvider {
			case constants.ProviderOpenAI:
				_, _ = converter.ConvertAnthropicToOpenAI(ctx, req)
			case constants.ProviderGoogle:
				_, _ = converter.ConvertAnthropicToGoogle(ctx, req)
			case constants.ProviderArgo:
				_, _ = converter.ConvertAnthropicToArgo(ctx, req, "testuser")
			}

			// Close writer and read output
			w.Close()
			captured, _ := io.ReadAll(r)
			os.Stderr = oldStderr

			// Restore logger
			logger.ResetForTesting()
			_ = logger.InitializeWithOptions(
				logger.WithLevel("debug"),
				logger.WithFormat("text"),
				logger.WithStderr(true),
				logger.WithFile(false),
				logger.WithComponent("test"),
			)

			logs := string(captured)

			// Find metadata log line
			lines := strings.Split(logs, "\n")
			var metadataLine string
			for _, line := range lines {
				if strings.Contains(line, "Omitting metadata from Anthropic request") {
					metadataLine = line
					break
				}
			}

			if metadataLine == "" {
				t.Fatal("Metadata log line not found")
			}

			// Extract JSON from log line
			// Simple title case conversion for provider name
			providerTitle := tt.targetProvider
			if len(providerTitle) > 0 {
				providerTitle = strings.ToUpper(providerTitle[:1]) + providerTitle[1:]
			}
			prefix := "Omitting metadata from Anthropic request (not supported by " + providerTitle + "): "
			idx := strings.Index(metadataLine, prefix)
			if idx == -1 {
				t.Fatalf("Prefix not found in line: %s", metadataLine)
			}

			jsonStart := idx + len(prefix)
			jsonStr := strings.TrimSpace(metadataLine[jsonStart:])

			// Validate it's valid JSON
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
				t.Errorf("Invalid JSON in metadata log: %v\nJSON: %s", err, jsonStr)
			}

			// Verify no Go map syntax
			if strings.Contains(jsonStr, "map[") {
				t.Errorf("JSON contains Go map syntax: %s", jsonStr)
			}

			// Verify structure matches expected
			expectedJSON, _ := json.Marshal(tt.expectedJSON)
			actualJSON, _ := json.Marshal(parsed)
			if string(expectedJSON) != string(actualJSON) {
				t.Errorf("JSON structure mismatch.\nExpected: %s\nActual: %s", expectedJSON, actualJSON)
			}
		})
	}
}

// TestNoMapSyntaxInOmittedFieldLogs ensures no Go map syntax appears in omitted field logs
func TestNoMapSyntaxInOmittedFieldLogs(t *testing.T) {
	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Reset and reinitialize logger
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	// Create converter
	config := &Config{Provider: constants.ProviderOpenAI}
	mapper := NewModelMapper(config)
	converter := NewConverter(mapper)

	// Create context with request counter
	ctx := context.WithValue(context.Background(), logger.RequestCounterKey{}, int64(1))

	// Create request with complex metadata
	topK := 5
	req := &AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 100,
		TopK:      &topK,
		Metadata: map[string]interface{}{
			"user_id": "user_123",
			"context": map[string]interface{}{
				"app_version": "1.0.0",
				"features":    []string{"chat", "tools"},
			},
		},
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Test"`),
			},
		},
	}

	// Convert to trigger logging
	_, _ = converter.ConvertAnthropicToOpenAI(ctx, req)

	// Close writer and read output
	w.Close()
	captured, _ := io.ReadAll(r)
	os.Stderr = oldStderr

	// Restore logger
	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	logs := string(captured)

	// Check for map syntax
	mapSyntaxRegex := regexp.MustCompile(`map\[[^\]]+\]`)
	if matches := mapSyntaxRegex.FindAllString(logs, -1); len(matches) > 0 {
		t.Errorf("Found Go map syntax in logs: %v\nFull logs:\n%s", matches, logs)
	}

	// Verify JSON format for metadata
	if strings.Contains(logs, "Omitting metadata") {
		lines := strings.Split(logs, "\n")
		for _, line := range lines {
			if strings.Contains(line, "Omitting metadata") {
				// Find the JSON part after the last colon and space
				idx := strings.LastIndex(line, ": ")
				if idx != -1 {
					jsonStr := strings.TrimSpace(line[idx+2:])
					var parsed interface{}
					if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
						t.Errorf("Invalid JSON in metadata log: %v\nJSON: %s", err, jsonStr)
					}
				}
			}
		}
	}
}
