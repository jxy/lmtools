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

// TestOpenAIStreamingJSONUnicodeEscape ensures simulateOpenAIStreamFromArgo uses JSON-aware chunking
// and does not split within \uXXXX escape sequences for tool arguments.
func TestOpenAIStreamingJSONUnicodeEscape(t *testing.T) {
	SetupTestLogger(t)

	// Prepare Argo response with a tool_use-like payload
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ArgoChatResponse{
			Response: map[string]interface{}{
				"content": "Now let's run the tests again to see if they pass:\n\n",
				"tool_calls": []map[string]interface{}{
					{
						"id":   "toolu_vrtx_01TEST",
						"type": "function",
						"function": map[string]interface{}{
							"name": "Bash",
							// OpenAI-style: arguments are provided as a JSON string
							"arguments": `{"command":"cd /path/to/project \u0026 make test with tab \t and quote \" and backslash \\ end","description":"Run unit tests after fixing function names","timeout":60000}`,
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

	// Server config
	config := &Config{
		Provider:    constants.ProviderArgo,
		ArgoUser:    "testuser",
		ArgoEnv:     mockArgo.URL,
		ProviderURL: mockArgo.URL,
	}
	server := NewTestServerDirectWithClient(t, config, retry.NewClient(10*time.Minute, logger.GetLogger()))

	// Prepare SSE recorder and writer
	recorder := httptest.NewRecorder()
	ctx := context.Background()
	writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
	if err != nil {
		t.Fatalf("Failed to create OpenAI stream writer: %v", err)
	}

	// Anthropic-style request routed to Argo
	anthReq := &AnthropicRequest{
		Model:  "gpt-4",
		Stream: true,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Run tests"`),
		}},
		MaxTokens: 100,
	}

	// Execute simulated streaming
	if err := server.simulateOpenAIStreamFromArgo(ctx, anthReq, writer); err != nil {
		t.Fatalf("simulateOpenAIStreamFromArgo failed: %v", err)
	}

	// Parse SSE output and accumulate tool argument deltas
	response := recorder.Body.String()
	lines := strings.Split(response, "\n")

	var argConcat string
	var sawInitialEmpty bool
	var sawInitialAssistant bool
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" || data == "[DONE]" {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			continue
		}
		// OpenAI SSE: first delta has role assistant + content:null, then tool argument fragments
		choices, _ := obj["choices"].([]interface{})
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]interface{})
		delta, _ := choice["delta"].(map[string]interface{})
		if role, ok := delta["role"].(string); ok && role == "assistant" {
			if _, hasContent := delta["content"]; hasContent {
				if delta["content"] == nil {
					sawInitialAssistant = true
				}
			}
		}
		if tcList, ok := delta["tool_calls"].([]interface{}); ok {
			for _, tc := range tcList {
				if tcMap, ok := tc.(map[string]interface{}); ok {
					if fn, ok := tcMap["function"].(map[string]interface{}); ok {
						if args, ok := fn["arguments"].(string); ok {
							if args == "" {
								sawInitialEmpty = true
							} else {
								argConcat += args
							}
						}
					}
				}
			}
		}
	}

	// Expect initial empty, then full JSON object concatenated
	if !sawInitialAssistant {
		t.Fatal("Did not see initial assistant delta with content:null")
	}
	if !sawInitialEmpty {
		t.Fatal("Did not see initial empty arguments chunk")
	}
	// The server streams the JSON for function arguments; depending on converter, this may be
	// either a wrapper {"raw_arguments":"..."} or the raw JSON object itself. Handle both.
	var top map[string]interface{}
	if err := json.Unmarshal([]byte(argConcat), &top); err != nil {
		t.Fatalf("Failed to parse concatenated arguments JSON: %v\nJSON: %s", err, argConcat)
	}
	var inner map[string]interface{}
	if raw, ok := top["raw_arguments"].(string); ok {
		if err := json.Unmarshal([]byte(raw), &inner); err != nil {
			t.Fatalf("Failed to parse inner raw_arguments JSON: %v\nJSON: %s", err, raw)
		}
	} else {
		inner = top
	}
	if _, ok := inner["timeout"].(float64); !ok {
		t.Fatalf("Arguments missing expected 'timeout' field: %v", inner)
	}
}
