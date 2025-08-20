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

func init() {
	// Initialize logger with request counter enabled for all proxy tests
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)
}

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
							Role:    RoleUser,
							Content: json.RawMessage(`"What's the weather?"`),
						},
					},
				}
			},
			targetProvider: "google",
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
			err := logger.InitializeWithOptions(
				logger.WithLogDir(tempDir),
				logger.WithLevel("debug"),
				logger.WithFormat("text"),
				logger.WithOutputMode(logger.OutputBoth),
			)
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
			case "google":
				_, err := converter.ConvertAnthropicToGoogle(context.Background(), req)
				if err != nil {
					t.Fatalf("Failed to convert to Google: %v", err)
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
		logger.WithOutputMode(logger.OutputStderrOnly),
		logger.WithComponent("test"),
	)

	// Create converter and test request with minimal config
	config := &Config{
		Provider: "openai",
	}
	mapper := NewModelMapper(config)
	converter := NewConverter(mapper)

	// Create a context with request logger to ensure logs go through properly
	reqLogger := NewRequestScopedLogger()
	ctx := WithRequestLogger(context.Background(), reqLogger)

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
		logger.WithOutputMode(logger.OutputStderrOnly),
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
