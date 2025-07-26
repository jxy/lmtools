//go:build integration
// +build integration

package apiproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)


func TestIntegrationBasicChat(t *testing.T) {
	proxyServer, openAIMock, geminiMock, argoMock := SetupTestServer(t)
	defer proxyServer.Close()
	defer openAIMock.Close()
	defer geminiMock.Close()
	defer argoMock.Close()

	tests := []struct {
		name      string
		request   AnthropicRequest
		checkResp func(t *testing.T, resp *AnthropicResponse)
	}{
		{
			name: "haiku to OpenAI",
			request: AnthropicRequest{
				Model:     "claude-3-haiku-20240307",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			checkResp: func(t *testing.T, resp *AnthropicResponse) {
				if len(resp.Content) == 0 {
					t.Fatal("Expected content in response")
				}
				if resp.Content[0].Type != "text" {
					t.Errorf("Expected text content, got %s", resp.Content[0].Type)
				}
				if !strings.Contains(resp.Content[0].Text, "OpenAI") {
					t.Errorf("Expected OpenAI response, got %s", resp.Content[0].Text)
				}
			},
		},
		{
			name: "direct Gemini model",
			request: AnthropicRequest{
				Model:     "gemini-2.0-flash",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    RoleUser,
						Content: json.RawMessage(`"Hello"`),
					},
				},
			},
			checkResp: func(t *testing.T, resp *AnthropicResponse) {
				if len(resp.Content) == 0 {
					t.Fatal("Expected content in response")
				}
				if !strings.Contains(resp.Content[0].Text, "Gemini") {
					t.Errorf("Expected Gemini response, got %s", resp.Content[0].Text)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make request
			reqBody, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("Failed to marshal request: %v", err)
			}
			t.Logf("Request body: %s", string(reqBody))
			
			resp, err := http.Post(
				proxyServer.URL+"/v1/messages",
				"application/json",
				bytes.NewReader(reqBody),
			)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
			}

			// Parse response
			var anthResp AnthropicResponse
			if err := json.NewDecoder(resp.Body).Decode(&anthResp); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Check response
			tt.checkResp(t, &anthResp)
		})
	}
}

func TestIntegrationStreaming(t *testing.T) {
	proxyServer, openAIMock, geminiMock, argoMock := SetupTestServer(t)
	defer proxyServer.Close()
	defer openAIMock.Close()
	defer geminiMock.Close()
	defer argoMock.Close()

	// Make streaming request
	req := AnthropicRequest{
		Model:     "claude-3-haiku",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Check Content-Type
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", ct)
	}

	// Read streaming response
	reader := bufio.NewReader(resp.Body)
	events := []string{}

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Error reading stream: %v", err)
		}

		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event:") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}

		// Check for completion
		if line == "data: [DONE]" {
			break
		}
	}

	// Verify we got expected events
	expectedEvents := []string{"message_start", "content_block_start", "ping", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	eventMap := make(map[string]bool)
	for _, e := range events {
		eventMap[e] = true
	}

	for _, expected := range expectedEvents {
		if !eventMap[expected] {
			t.Errorf("Missing expected event: %s", expected)
		}
	}
}

func TestIntegrationRetry(t *testing.T) {
	// This test requires a more complex setup to test retry logic
	// For now, we'll skip it and implement it separately
	t.Skip("Retry test needs special mock setup - implement separately")
}

func TestIntegrationSimulatedStreamingWithTools(t *testing.T) {
	proxyServer, _, _, _ := SetupTestServer(t)

	// Make streaming request with tools
	req := AnthropicRequest{
		Model:     "gpt4",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    RoleUser,
				Content: json.RawMessage(`"List the contents of the current directory"`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "LS",
				Description: "List directory contents",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type":        "string",
							"description": "Directory path",
						},
					},
					"required": []string{"path"},
				},
			},
		},
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Read streaming response and count content_block_stop events
	reader := bufio.NewReader(resp.Body)
	contentBlockStopCount := 0
	blockIndices := make(map[int]int) // Track how many times each index is closed

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Error reading stream: %v", err)
		}

		line = strings.TrimSpace(line)
		
		// Check for content_block_stop events
		if line == "event: content_block_stop" {
			// Next line should be data
			dataLine, err := reader.ReadString('\n')
			if err == nil {
				dataLine = strings.TrimSpace(dataLine)
				if strings.HasPrefix(dataLine, "data: ") {
					var data map[string]interface{}
					jsonStr := strings.TrimPrefix(dataLine, "data: ")
					if err := json.Unmarshal([]byte(jsonStr), &data); err == nil {
						if index, ok := data["index"].(float64); ok {
							blockIndices[int(index)]++
							contentBlockStopCount++
						}
					}
				}
			}
		}

		// Check for completion
		if line == "data: [DONE]" {
			break
		}
	}

	// Verify no double closing
	for index, count := range blockIndices {
		if count > 1 {
			t.Errorf("Block %d was closed %d times (expected 1)", index, count)
		}
	}

	// Should have at least 1 content_block_stop event
	// Note: Argo tool format parsing is not implemented, so tool tags are included in text
	if contentBlockStopCount < 1 {
		t.Errorf("Expected at least 1 content_block_stop event, got %d", contentBlockStopCount)
	}

	t.Logf("Blocks closed: %v", blockIndices)
}
