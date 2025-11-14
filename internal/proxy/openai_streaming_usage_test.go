package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSimulateOpenAIStreamIncludeUsage_TextOnly verifies usage:null appears when requested
// and a final usage object chunk is emitted after the final finish_reason, before [DONE].
func TestSimulateOpenAIStreamIncludeUsage_TextOnly(t *testing.T) {
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Mock Argo: simple text response
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ArgoChatResponse{Response: "Hello world"}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer mockArgo.Close()

	// Server config
	config := &Config{
		ArgoUser:    "testuser",
		ArgoEnv:     mockArgo.URL,
		ArgoBaseURL: mockArgo.URL,
	}
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Recorder and writer
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	// Anthropic-style request with include_usage flag via metadata
	anthReq := &AnthropicRequest{
		Model:  "gpt-4",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Say hi"`),
		}},
		MaxTokens: 64,
		Metadata:  map[string]interface{}{constants.IncludeUsageKey: true},
	}

	// Create writer with include_usage option based on metadata
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx, WithIncludeUsage(includeUsageFromMetadata(anthReq)))
	if err != nil {
		t.Fatalf("Failed to create OpenAI stream writer: %v", err)
	}

	// Execute simulated streaming
	if err := server.simulateOpenAIStreamFromArgo(ctx, anthReq, writer); err != nil {
		t.Fatalf("simulateOpenAIStreamFromArgo failed: %v", err)
	}

	// Parse SSE
	response := recorder.Body.String()
	lines := strings.Split(response, "\n")

	usageNullIdx := -1
	finishIdx := -1
	usageFinalIdx := -1
	doneIdx := -1

	for i, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				doneIdx = i
				continue
			}
			if data == "" {
				continue
			}
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(data), &obj); err != nil {
				continue
			}
			// Track usage:null chunks (object has usage key with null)
			if _, hasUsage := obj["usage"]; hasUsage && obj["usage"] == nil {
				// Ensure this is early in the stream (before final finish_reason)
				usageNullIdx = i
			}
			// Track final usage object
			if usage, hasUsage := obj["usage"]; hasUsage && usage != nil {
				if _, ok := usage.(map[string]interface{}); ok {
					usageFinalIdx = i
				}
			}
			// Track final finish_reason on choices[0]
			if choices, ok := obj["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
						finishIdx = i
					}
				}
			}
		}
	}

	if usageNullIdx == -1 {
		t.Error("Did not see usage:null chunk in stream")
	}
	if finishIdx == -1 {
		t.Error("Did not see final finish_reason chunk in stream")
	}
	if usageFinalIdx == -1 {
		t.Error("Did not see final usage object chunk in stream")
	}
	if usageFinalIdx <= finishIdx || doneIdx <= usageFinalIdx {
		t.Errorf("Expected final usage after finish_reason and before [DONE]; got indices usageFinal=%d finish=%d done=%d", usageFinalIdx, finishIdx, doneIdx)
	}
}

// TestSimulateOpenAIStreamIncludeUsage_ToolCalls verifies usage streaming with tool calls present.
func TestSimulateOpenAIStreamIncludeUsage_ToolCalls(t *testing.T) {
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Mock Argo: tool_calls structure
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ArgoChatResponse{Response: map[string]interface{}{
			"content": "Analyzing",
			"tool_calls": []map[string]interface{}{
				{
					"id":   "tool_abc",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "test_tool",
						"arguments": `{"arg1":"Hello","arg2":"World"}`,
					},
				},
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer mockArgo.Close()

	// Server config
	config := &Config{
		ArgoUser:    "testuser",
		ArgoEnv:     mockArgo.URL,
		ArgoBaseURL: mockArgo.URL,
	}
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Recorder and writer
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	// Anthropic-style request with include_usage flag via metadata
	anthReq := &AnthropicRequest{
		Model:  "gpt-4",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Use tool"`),
		}},
		MaxTokens: 64,
		Metadata:  map[string]interface{}{constants.IncludeUsageKey: true},
		Tools: []AnthropicTool{{
			Name:        "test_tool",
			Description: "test",
			InputSchema: map[string]interface{}{"type": "object"},
		}},
	}

	// Create writer with include_usage option based on metadata
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx, WithIncludeUsage(includeUsageFromMetadata(anthReq)))
	if err != nil {
		t.Fatalf("Failed to create OpenAI stream writer: %v", err)
	}

	// Execute simulated streaming
	if err := server.simulateOpenAIStreamFromArgo(ctx, anthReq, writer); err != nil {
		t.Fatalf("simulateOpenAIStreamFromArgo failed: %v", err)
	}

	// Parse SSE
	response := recorder.Body.String()
	lines := strings.Split(response, "\n")

	usageNullIdx := -1
	finishIdx := -1
	usageFinalIdx := -1
	doneIdx := -1

	for i, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				doneIdx = i
				continue
			}
			if data == "" {
				continue
			}
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(data), &obj); err != nil {
				continue
			}
			// Track usage:null chunks
			if _, hasUsage := obj["usage"]; hasUsage && obj["usage"] == nil {
				usageNullIdx = i
			}
			// Track final usage object
			if usage, hasUsage := obj["usage"]; hasUsage && usage != nil {
				if _, ok := usage.(map[string]interface{}); ok {
					usageFinalIdx = i
				}
			}
			// Track final finish_reason on choices[0]
			if choices, ok := obj["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
						finishIdx = i
					}
				}
			}
		}
	}

	if usageNullIdx == -1 {
		t.Error("Did not see usage:null chunk in stream")
	}
	if finishIdx == -1 {
		t.Error("Did not see final finish_reason chunk in stream")
	}
	if usageFinalIdx == -1 {
		t.Error("Did not see final usage object chunk in stream")
	}
	if usageFinalIdx <= finishIdx || doneIdx <= usageFinalIdx {
		t.Errorf("Expected final usage after finish_reason and before [DONE]; got indices usageFinal=%d finish=%d done=%d", usageFinalIdx, finishIdx, doneIdx)
	}
}
