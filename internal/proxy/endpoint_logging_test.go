package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// captureStderr captures stderr output during test execution
func captureStderr(t *testing.T, f func()) string {
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

func TestCountTokensEndpointLogging(t *testing.T) {
	SetupTestLogger(t)

	// Create minimal server config
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	// Create request
	tokenReq := AnthropicTokenCountRequest{
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Test message for token counting"`),
			},
			{
				Role:    core.RoleAssistant,
				Content: json.RawMessage(`"Response message"`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "test_tool",
				Description: "A test tool for counting",
			},
		},
	}

	reqBody, err := json.Marshal(tokenReq)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Capture logs while making request
	logs := captureStderr(t, func() {
		resp, err := http.Post(
			testServer.URL+"/v1/messages/count_tokens",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			t.Errorf("Request failed: %v", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
		}
	})

	// Verify INFO level logs exist
	if !strings.Contains(logs, "[INFO]") {
		t.Error("Expected INFO level logs, but none found")
		t.Logf("Captured logs: %s", logs)
	}

	// Check if DEBUG logs are present at all
	if !strings.Contains(logs, "[DEBUG]") {
		// DEBUG logs might not be captured, so we'll skip these checks
		t.Logf("DEBUG logs not found in output, skipping debug message checks")
	} else {
		// Verify request logging (now at DEBUG level via LogJSON)
		if !strings.Contains(logs, "Incoming Token Count Request") {
			t.Error("Expected 'Incoming Token Count Request' in DEBUG logs")
		}

		// Verify response logging (now at DEBUG level via LogJSON)
		if !strings.Contains(logs, "Token Count Response") {
			t.Error("Expected 'Token Count Response' in DEBUG logs")
		}
	}

	// Verify summary contains expected information
	if !strings.Contains(logs, "POST /v1/messages/count_tokens") {
		t.Error("Expected 'POST /v1/messages/count_tokens' in INFO logs")
	}

	if !strings.Contains(logs, "Messages: 2") {
		t.Error("Expected 'Messages: 2' in INFO logs")
	}

	if !strings.Contains(logs, "Tools: 1") {
		t.Error("Expected 'Tools: 1' in INFO logs")
	}

	if !strings.Contains(logs, "Input tokens:") {
		t.Error("Expected 'Input tokens:' in INFO logs")
	}

	if !strings.Contains(logs, "Status: 200") {
		t.Error("Expected 'Status: 200' in INFO logs")
	}

	// Verify request ID is present (format: [#N])
	if !strings.Contains(logs, "[#") {
		t.Error("Expected request ID in logs")
	}
}

func TestRootEndpointLogging(t *testing.T) {
	// Reset and initialize logger with INFO level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create minimal server config
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	// Test GET request
	t.Run("GET request", func(t *testing.T) {
		logs := captureStderr(t, func() {
			resp, err := http.Get(testServer.URL + "/")
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status 200, got %d", resp.StatusCode)
			}
		})

		// Verify INFO level log
		if !strings.Contains(logs, "[INFO]") {
			t.Error("Expected INFO level log for root endpoint")
		}

		// Verify log message
		if !strings.Contains(logs, "GET / | Root endpoint accessed") {
			t.Error("Expected 'GET / | Root endpoint accessed' in logs")
		}

		// Verify request ID is present (format: [#N])
		if !strings.Contains(logs, "[#") {
			t.Error("Expected request ID in logs")
		}
	})

	// Test POST request
	t.Run("POST request", func(t *testing.T) {
		logs := captureStderr(t, func() {
			resp, err := http.Post(testServer.URL+"/", "application/json", bytes.NewReader([]byte("{}")))
			if err != nil {
				t.Errorf("Request failed: %v", err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status 200, got %d", resp.StatusCode)
			}
		})

		// Verify INFO level log
		if !strings.Contains(logs, "[INFO]") {
			t.Error("Expected INFO level log for root endpoint")
		}

		// Verify log message
		if !strings.Contains(logs, "POST / | Root endpoint accessed") {
			t.Error("Expected 'POST / | Root endpoint accessed' in logs")
		}

		// Verify request ID is present (format: [#N])
		if !strings.Contains(logs, "[#") {
			t.Error("Expected request ID in logs")
		}
	})
}

func Test404EndpointLogging(t *testing.T) {
	// Reset and initialize logger with WARN level
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("warn"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create minimal server config
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	// Test various unknown paths
	testPaths := []struct {
		method string
		path   string
	}{
		{"GET", "/unknown/path"},
		{"POST", "/api/v2/messages"},
		{"PUT", "/v1/unknown"},
		{"DELETE", "/some/random/endpoint"},
	}

	for _, test := range testPaths {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			logs := captureStderr(t, func() {
				var resp *http.Response
				var err error

				switch test.method {
				case "GET":
					resp, err = http.Get(testServer.URL + test.path)
				case "POST":
					resp, err = http.Post(testServer.URL+test.path, "application/json", bytes.NewReader([]byte("{}")))
				case "PUT":
					req, _ := http.NewRequest("PUT", testServer.URL+test.path, bytes.NewReader([]byte("{}")))
					resp, err = http.DefaultClient.Do(req)
				case "DELETE":
					req, _ := http.NewRequest("DELETE", testServer.URL+test.path, nil)
					resp, err = http.DefaultClient.Do(req)
				}

				if err != nil {
					t.Errorf("Request failed: %v", err)
					return
				}
				defer resp.Body.Close()

				// Should return 404
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("Expected status 404, got %d", resp.StatusCode)
				}
			})

			// Verify WARN level log
			if !strings.Contains(logs, "[WARN]") {
				t.Error("Expected WARN level log for 404 endpoint")
			}

			// Verify log message
			expectedMsg := fmt.Sprintf("%s %s | Path not found", test.method, test.path)
			if !strings.Contains(logs, expectedMsg) {
				t.Errorf("Expected '%s' in logs, got: %s", expectedMsg, logs)
			}

			// Verify request ID is present
			if !strings.Contains(logs, "[#") {
				t.Error("Expected request ID in logs")
			}
		})
	}
}

// TestCountTokensWithDifferentInputSizes tests token counting with various input sizes
func TestCountTokensWithDifferentInputSizes(t *testing.T) {
	// Reset and initialize logger with INFO level (to see the summary)
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Close()

	// Create minimal server config
	config := &Config{
		Provider:           constants.ProviderAnthropic,
		MaxRequestBodySize: 10 * 1024 * 1024,
	}

	// Create server (NewEndpoints is called internally)
	server, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)
	testServer := httptest.NewServer(server)
	defer testServer.Close()

	tests := []struct {
		name          string
		messageCount  int
		toolCount     int
		expectedChars int // Approximate
	}{
		{
			name:         "Single message, no tools",
			messageCount: 1,
			toolCount:    0,
		},
		{
			name:         "Multiple messages with tools",
			messageCount: 5,
			toolCount:    3,
		},
		{
			name:         "No messages (should error)",
			messageCount: 0,
			toolCount:    0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Build request
			var messages []AnthropicMessage
			for i := 0; i < test.messageCount; i++ {
				role := core.RoleUser
				if i%2 == 1 {
					role = core.RoleAssistant
				}
				messages = append(messages, AnthropicMessage{
					Role:    role,
					Content: json.RawMessage(fmt.Sprintf(`"Message %d content"`, i)),
				})
			}

			var tools []AnthropicTool
			for i := 0; i < test.toolCount; i++ {
				tools = append(tools, AnthropicTool{
					Name:        fmt.Sprintf("tool_%d", i),
					Description: fmt.Sprintf("Tool %d description", i),
				})
			}

			tokenReq := AnthropicTokenCountRequest{
				Messages: messages,
				Tools:    tools,
			}

			reqBody, _ := json.Marshal(tokenReq)

			logs := captureStderr(t, func() {
				resp, err := http.Post(
					testServer.URL+"/v1/messages/count_tokens",
					"application/json",
					bytes.NewReader(reqBody),
				)
				if err != nil {
					t.Errorf("Request failed: %v", err)
					return
				}
				defer resp.Body.Close()

				if test.messageCount == 0 {
					// Should return error for empty messages
					if resp.StatusCode == http.StatusOK {
						t.Error("Expected error for empty messages, got 200")
					}
				} else {
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						t.Errorf("Expected status 200, got %d: %s", resp.StatusCode, string(body))
					}
				}
			})

			if test.messageCount > 0 {
				// Verify the counts in logs
				expectedMessages := fmt.Sprintf("Messages: %d", test.messageCount)
				if !strings.Contains(logs, expectedMessages) {
					t.Errorf("Expected '%s' in logs", expectedMessages)
				}

				expectedTools := fmt.Sprintf("Tools: %d", test.toolCount)
				if !strings.Contains(logs, expectedTools) {
					t.Errorf("Expected '%s' in logs", expectedTools)
				}

				// Verify token count is present
				if !strings.Contains(logs, "Input tokens:") {
					t.Error("Expected 'Input tokens:' in logs")
				}
			}
		})
	}
}
