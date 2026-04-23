package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectUnknownFields(t *testing.T) {
	tests := []struct {
		name           string
		jsonData       string
		expectedFields []string
	}{
		{
			name: "No unknown fields",
			jsonData: `{
				"model": "claude-3-opus-20240229",
				"max_tokens": 100,
				"messages": [{"role": "user", "content": "Hello"}]
			}`,
			expectedFields: []string{},
		},
		{
			name: "Single unknown field",
			jsonData: `{
				"model": "claude-3-opus-20240229",
				"max_tokens": 100,
				"messages": [{"role": "user", "content": "Hello"}],
				"unknown_field": "value"
			}`,
			expectedFields: []string{"unknown_field"},
		},
		{
			name: "Multiple unknown fields",
			jsonData: `{
				"model": "claude-3-opus-20240229",
				"max_tokens": 100,
				"messages": [{"role": "user", "content": "Hello"}],
				"custom_param": 123,
				"extra_option": true,
				"new_feature": {"nested": "value"}
			}`,
			expectedFields: []string{"custom_param", "extra_option", "new_feature"},
		},
		{
			name: "Unknown fields with known fields",
			jsonData: `{
				"model": "claude-3-opus-20240229",
				"max_tokens": 100,
				"temperature": 0.7,
				"top_p": 0.9,
				"top_k": 10,
				"metadata": {"user": "test"},
				"messages": [{"role": "user", "content": "Hello"}],
				"stream": true,
				"stop_sequences": ["END"],
				"system": "You are helpful",
				"tools": [],
				"tool_choice": {"type": "auto"},
				"response_format": {"type": "json"},
				"frequency_penalty": 0.5,
				"presence_penalty": 0.3,
				"user": "user123"
			}`,
			expectedFields: []string{"response_format", "frequency_penalty", "presence_penalty", "user"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req AnthropicRequest
			fields, err := detectUnknownFields([]byte(tt.jsonData), req)
			if err != nil {
				t.Fatalf("Failed to detect unknown fields: %v", err)
			}

			// Check if we got the expected number of unknown fields
			if len(fields) != len(tt.expectedFields) {
				t.Errorf("Expected %d unknown fields, got %d: %v",
					len(tt.expectedFields), len(fields), fields)
				return
			}

			// Check if all expected fields are present
			for _, expected := range tt.expectedFields {
				found := false
				for _, field := range fields {
					if field == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected unknown field '%s' not found in %v", expected, fields)
				}
			}
		})
	}
}

func TestLogUnknownFields(t *testing.T) {
	// This test verifies that logUnknownFields doesn't panic and logs appropriately
	jsonData := `{
		"model": "claude-3-opus-20240229",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hello"}],
		"custom_field": "custom_value",
		"extra_number": 42,
		"nested_object": {"key": "value"}
	}`

	var req AnthropicRequest
	// This should not panic
	logUnknownFields(context.Background(), []byte(jsonData), req, "test request")

	// Test with invalid JSON
	invalidJSON := `{invalid json}`
	logUnknownFields(context.Background(), []byte(invalidJSON), req, "invalid request")
}

func TestGetStructFieldJSONNames(t *testing.T) {
	var req AnthropicRequest
	fields := getStructFieldJSONNames(req)

	// Check that we get the expected fields
	expectedFields := []string{
		"model", "max_tokens", "messages", "system",
		"stop_sequences", "stream", "temperature", "top_p",
		"top_k", "metadata", "tools", "tool_choice", "thinking", "output_config",
		"container", "context_management", "service_tier", "inference_geo",
		"speed", "cache_control", "mcp_servers",
	}

	if len(fields) != len(expectedFields) {
		t.Errorf("Expected %d fields, got %d: %v",
			len(expectedFields), len(fields), fields)
	}

	// Check that all expected fields are present
	for _, expected := range expectedFields {
		found := false
		for _, field := range fields {
			if field == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected field '%s' not found in %v", expected, fields)
		}
	}
}

func TestUnknownFieldsIntegration(t *testing.T) {
	// Test that unknown fields are properly ignored during unmarshaling
	jsonData := `{
		"model": "claude-3-opus-20240229",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hello"}],
		"unknown_field": "this should be ignored",
		"another_unknown": 123
	}`

	var req AnthropicRequest
	err := json.Unmarshal([]byte(jsonData), &req)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Verify that known fields are properly parsed
	if req.Model != "claude-3-opus-20240229" {
		t.Errorf("Expected model 'claude-3-opus-20240229', got '%s'", req.Model)
	}
	if req.MaxTokens != 100 {
		t.Errorf("Expected max_tokens 100, got %d", req.MaxTokens)
	}
	if len(req.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(req.Messages))
	}

	// Unknown fields should not affect the parsing
}

func TestDetectUnknownFieldPathsNested(t *testing.T) {
	jsonData := `{
		"candidates": [{
			"index": 0,
			"content": {
				"role": "model",
				"parts": [{"text": "hello", "newPart": true}]
			},
			"extraCandidate": true
		}],
		"usageMetadata": {
			"promptTokenCount": 1,
			"candidatesTokenCount": 2,
			"totalTokenCount": 3,
			"newUsage": 4
		},
		"responseId": "resp_123",
		"unknownTop": true
	}`

	fields, err := detectUnknownFieldPaths([]byte(jsonData), GoogleResponse{})
	if err != nil {
		t.Fatalf("detectUnknownFieldPaths() error = %v", err)
	}

	for _, want := range []string{
		"candidates[].content.parts[].newPart",
		"candidates[].extraCandidate",
		"usageMetadata.newUsage",
		"unknownTop",
	} {
		found := false
		for _, field := range fields {
			if field == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected unknown field path %q in %v", want, fields)
		}
	}
}

func TestWarnUnknownFieldsLogsNestedPaths(t *testing.T) {
	jsonData := `{
		"candidates": [{
			"index": 0,
			"content": {
				"role": "model",
				"parts": [{"text": "hello", "newPart": true}]
			},
			"extraCandidate": true
		}],
		"usageMetadata": {
			"promptTokenCount": 1,
			"candidatesTokenCount": 2,
			"totalTokenCount": 3
		},
		"unexpectedTop": true
	}`

	logs := captureWarnLogs(t, func() {
		warnUnknownFields(context.Background(), []byte(jsonData), GoogleResponse{}, "Google response")
	})

	for _, want := range []string{
		`Unknown JSON fields in Google response (ignored):`,
		`candidates[].content.parts[].newPart`,
		`candidates[].extraCandidate`,
		`unexpectedTop`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestDecodeEndpointRequestWarnsBeforeRejectingUnknownFields(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{})
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		bytes.NewBufferString(`{
			"model":"gpt-5",
			"messages":[{"role":"user","content":"hi"}],
			"unknown_field":true
		}`),
	)

	logs := captureWarnLogs(t, func() {
		var decoded OpenAIRequest
		err := server.decodeEndpointRequest(req, &decoded)
		if err == nil {
			t.Fatal("decodeEndpointRequest() error = nil, want unknown field rejection")
		}
		if !strings.Contains(err.Error(), `unknown field "unknown_field"`) {
			t.Fatalf("decodeEndpointRequest() error = %v, want unknown field rejection", err)
		}
	})

	if !strings.Contains(logs, `Unknown JSON fields in client request (rejected): unknown_field`) {
		t.Fatalf("request warning not found in logs:\n%s", logs)
	}
}

func TestRewriteOpenAIResponseModelWarnsOnUnknownFields(t *testing.T) {
	logs := captureWarnLogs(t, func() {
		resp, err := rewriteOpenAIResponseModel(
			context.Background(),
			[]byte(`{
				"id":"chatcmpl_123",
				"object":"chat.completion",
				"created":123,
				"model":"gpt-5.4-nano",
				"choices":[],
				"unexpected_top":true
			}`),
			"gpt-5",
			"OpenAI response",
		)
		if err != nil {
			t.Fatalf("rewriteOpenAIResponseModel() error = %v", err)
		}
		if resp.Model != "gpt-5" {
			t.Fatalf("Model = %q, want %q", resp.Model, "gpt-5")
		}
	})

	if !strings.Contains(logs, `Unknown JSON fields in OpenAI response (ignored): unexpected_top`) {
		t.Fatalf("response warning not found in logs:\n%s", logs)
	}
}
