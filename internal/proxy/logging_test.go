//go:build integration
// +build integration

package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDebugLoggingToStderr verifies that DEBUG logs go to stderr when no log directory is configured
func TestDebugLoggingToStderr(t *testing.T) {
	// Reset logger to allow reinitialization with DEBUG level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create a test server configured to use Argo as preferred provider
	// Don't provide OpenAI key so it doesn't get selected over Argo
	argoMock := httptest.NewServer(NewMockArgo(t))
	defer argoMock.Close()

	config := &Config{
		ArgoUser:           "testuser",
		ArgoEnv:            "test", 
		Provider:  "argo",
		SmallModel:         "claude-3-haiku-20240307",
		Model:              "claude-3-opus-20240229",
		MaxRequestBodySize: 10 * 1024 * 1024,
		ArgoBaseURL:        argoMock.URL,
		// DON'T set OpenAI/Gemini keys so Argo is selected
	}
	config.InitializeURLs()
	server := NewServer(config)
	proxyServer := httptest.NewServer(server)
	defer proxyServer.Close()

	// Use a mutex-protected buffer to capture stderr
	var mu sync.Mutex
	var stderrBuf bytes.Buffer
	
	// Create a custom writer that captures stderr
	type safeWriter struct {
		mu  *sync.Mutex
		buf *bytes.Buffer
	}
	
	sw := &safeWriter{mu: &mu, buf: &stderrBuf}
	
	// Create a pipe and writer that copies to our buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	
	// Reinitialize logger to use the new stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to reinitialize logger: %v", err)
	}
	
	// Start goroutine to copy from pipe to buffer
	done := make(chan bool)
	go func() {
		io.Copy(sw.buf, r)
		done <- true
	}()

	// Create a request that uses Argo with tools to trigger the specific debug message
	req := AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Test with tools"`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"param": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
		},
	}

	// Send the request
	reqBody, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	
	// Read the response body to ensure request completes
	io.ReadAll(resp.Body)
	resp.Body.Close()
	
	// Wait a bit to ensure all logging completes
	time.Sleep(50 * time.Millisecond)

	// Restore stderr
	w.Close()
	os.Stderr = oldStderr
	<-done
	
	// Restore logger to use original stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to restore logger: %v", err)
	}
	
	// Get the captured output
	mu.Lock()
	stderrOutput := stderrBuf.String()
	mu.Unlock()

	// Verify we see the Argo tool detection message
	expectedMessage := "Request has 1 tools defined, using simulated streaming for Argo"
	if strings.Contains(stderrOutput, expectedMessage) {
		t.Logf("SUCCESS: Found expected Argo tool detection message")
	} else if strings.Contains(stderrOutput, "[DEBUG]") {
		t.Logf("Found DEBUG messages but not the Argo tool detection message")
		t.Logf("Captured output:\n%s", stderrOutput)
		// Check what provider was selected
		if strings.Contains(stderrOutput, "via openai") {
			t.Errorf("Model was incorrectly routed to OpenAI instead of Argo")
		} else if strings.Contains(stderrOutput, "via gemini") {
			t.Errorf("Model was incorrectly routed to Gemini instead of Argo")
		} else {
			t.Errorf("Expected Argo tool detection message not found")
		}
	} else {
		t.Errorf("No DEBUG messages found in stderr output.\nOutput: %s", stderrOutput)
	}
}

// TestInfoLoggingToStderr verifies that INFO logs still go to stderr
func TestInfoLoggingToStderr(t *testing.T) {
	// Reset logger to allow reinitialization with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	
	// Reinitialize logger to use the new stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to reinitialize logger: %v", err)
	}

	// Log an info message directly
	logger.Infof("Test info message")

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = oldStderr
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	stderrOutput := buf.String()
	
	// Restore logger to use original stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to restore logger: %v", err)
	}

	// Verify info message is in stderr (now includes timestamp)
	if !strings.Contains(stderrOutput, "[INFO]") || !strings.Contains(stderrOutput, "Test info message") {
		t.Errorf("Expected info message not found in stderr output: %s", stderrOutput)
	}
}

// TestDebugLoggingDisabledAtInfoLevel verifies DEBUG logs don't appear when log level is INFO
func TestDebugLoggingDisabledAtInfoLevel(t *testing.T) {
	// Reset logger to allow reinitialization with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	
	// Reinitialize logger to use the new stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to reinitialize logger: %v", err)
	}

	// Log a debug message
	logger.Debugf("%s", "This debug message should not appear")

	// Log an info message to ensure logging is working
	logger.Infof("%s", "This info message should appear")

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = oldStderr
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	stderrOutput := buf.String()
	
	// Restore logger to use original stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to restore logger: %v", err)
	}

	// Verify debug message is NOT in stderr
	if strings.Contains(stderrOutput, "This debug message should not appear") {
		t.Errorf("Debug message appeared in stderr when log level is INFO")
	}

	// Verify info message IS in stderr
	if !strings.Contains(stderrOutput, "This info message should appear") {
		t.Errorf("Info message not found in stderr output")
	}
}

// TestArgoToolDetectionLogging specifically tests the tool detection logging
func TestArgoToolDetectionLogging(t *testing.T) {
	// Reset logger to allow reinitialization with DEBUG level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create a mock Argo server that accepts tool requests
	argoMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the request body to avoid EOF errors
		body, _ := io.ReadAll(r.Body)
		t.Logf("Mock Argo received request: %s", string(body))
		
		// Return a simple response
		resp := ArgoChatResponse{
			Response: "Test response from Argo",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer argoMock.Close()

	// Create config with Argo as provider - NO other API keys
	config := &Config{
		ArgoUser:          "testuser",
		ArgoEnv:           "test",
		Provider: "argo",
		ArgoBaseURL:       argoMock.URL,
		Model:             "claude-3-opus-20240229",
		SmallModel:        "claude-3-haiku-20240307",
		MaxRequestBodySize: 10 * 1024 * 1024,
		// NO OpenAI or Gemini keys - this ensures Argo is used
	}
	config.InitializeURLs()

	// Create server
	server := NewServer(config)
	proxyServer := httptest.NewServer(server)
	defer proxyServer.Close()

	// Use mutex-protected buffer for stderr capture
	var mu sync.Mutex
	var stderrBuf bytes.Buffer
	
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	
	// Reinitialize logger to use the new stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to reinitialize logger: %v", err)
	}
	
	done := make(chan bool)
	go func() {
		io.Copy(&stderrBuf, r)
		done <- true
	}()

	// Make a streaming request with tools (should trigger simulated streaming)
	req := AnthropicRequest{
		Model:     "claude-3-opus-20240229",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Test"`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "test_tool",
				Description: "Test tool",
				InputSchema: map[string]interface{}{
					"type": "object",
				},
			},
		},
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}
	
	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	
	// Read response to ensure completion
	io.ReadAll(resp.Body)
	resp.Body.Close()
	
	// Wait for logging to complete
	time.Sleep(50 * time.Millisecond)

	// Restore stderr
	w.Close()
	os.Stderr = oldStderr
	<-done
	
	// Restore logger to use original stderr
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputStderrOnly),
	); err != nil {
		t.Fatalf("Failed to restore logger: %v", err)
	}
	
	mu.Lock()
	stderrOutput := stderrBuf.String()
	mu.Unlock()

	// Check for the specific tool detection message
	if !strings.Contains(stderrOutput, "Request has 1 tools defined, using simulated streaming for Argo") {
		t.Errorf("Tool detection debug message not found in stderr.\nCaptured output:\n%s", stderrOutput)
		
		// Debug: Check what provider was selected
		if strings.Contains(stderrOutput, "Model mapping:") {
			t.Logf("Model mapping found in output - check if correct provider was selected")
		}
		if strings.Contains(stderrOutput, "via openai") {
			t.Errorf("ERROR: Model was routed to OpenAI instead of Argo!")
		}
		if strings.Contains(stderrOutput, "via gemini") {
			t.Errorf("ERROR: Model was routed to Gemini instead of Argo!")
		}
	}
}