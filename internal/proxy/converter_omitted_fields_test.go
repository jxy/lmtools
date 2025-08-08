package proxy

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/logger"
	"os"
	"path/filepath"
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
							Role:    RoleUser,
							Content: json.RawMessage(`"Hello"`),
						},
					},
				}
			},
			targetProvider: "openai",
			expectedLogs: []string{
				"Omitting top_k=10 from Anthropic request (not supported by OpenAI)",
				"Omitting metadata from Anthropic request (not supported by OpenAI)",
			},
		},
		{
			name: "Gemini omits metadata and tool_choice",
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
							Role:    RoleUser,
							Content: json.RawMessage(`"What's the weather?"`),
						},
					},
				}
			},
			targetProvider: "gemini",
			expectedLogs: []string{
				"Omitting metadata from Anthropic request (not supported by Gemini)",
				"Omitting tool_choice from Anthropic request (Gemini uses different tool configuration): type=tool, name=get_weather",
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
							Role:    RoleUser,
							Content: json.RawMessage(`"Hello Argo"`),
						},
					},
				}
			},
			targetProvider: "argo",
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
							Role:    RoleUser,
							Content: json.RawMessage(`"Simple message"`),
						},
					},
				}
			},
			targetProvider: "openai",
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
			err := logger.Initialize(tempDir, "debug", "text", true)
			if err != nil {
				t.Fatalf("Failed to initialize logger: %v", err)
			}
			defer logger.Close()

			// Create converter with minimal config
			config := &Config{
				Provider: "openai",
			}
			mapper := NewModelMapper(config)
			converter := NewConverter(mapper)

			// Setup request
			req := tt.setupRequest()

			// Convert based on target provider
			switch tt.targetProvider {
			case "openai":
				_, err := converter.ConvertAnthropicToOpenAI(context.Background(), req)
				if err != nil {
					t.Fatalf("Failed to convert to OpenAI: %v", err)
				}
			case "gemini":
				_, err := converter.ConvertAnthropicToGemini(context.Background(), req)
				if err != nil {
					t.Fatalf("Failed to convert to Gemini: %v", err)
				}
			case "argo":
				_, err := converter.ConvertAnthropicToArgo(context.Background(), req, "testuser")
				if err != nil {
					t.Fatalf("Failed to convert to Argo: %v", err)
				}
			}

			// Read the log file to check for expected logs
			logFiles, err := filepath.Glob(filepath.Join(tempDir, "*.log"))
			if err != nil || len(logFiles) == 0 {
				t.Fatalf("No log files found in %s", tempDir)
			}

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
		})
	}
}

// TestOmittedFieldsLoggingWithoutFile tests that debug logs are written to stderr when no log file is configured
func TestOmittedFieldsLoggingWithoutFile(t *testing.T) {
	// Reset logger for testing
	logger.ResetForTesting()

	// Initialize logger without log directory (debug goes to stderr)
	err := logger.Initialize("", "debug", "text", true)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// Create converter and test request with minimal config
	config := &Config{
		Provider: "openai",
	}
	mapper := NewModelMapper(config)
	converter := NewConverter(mapper)

	topK := 10
	req := &AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 100,
		TopK:      &topK,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	// Convert to trigger logging
	_, err = converter.ConvertAnthropicToOpenAI(context.Background(), req)
	if err != nil {
		t.Fatalf("Failed to convert: %v", err)
	}

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = oldStderr
	captured, _ := io.ReadAll(r)

	// Check that debug log was written to stderr
	if !strings.Contains(string(captured), "Omitting top_k=10") {
		t.Errorf("Expected debug log not found in stderr output: %s", string(captured))
	}
}
