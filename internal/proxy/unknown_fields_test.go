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

func TestDecodeEndpointRequestWarnsButAcceptsUnknownFields(t *testing.T) {
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
		if err != nil {
			t.Fatalf("decodeEndpointRequest() error = %v, want success with warning", err)
		}
		if decoded.Model != "gpt-5" {
			t.Fatalf("decoded.Model = %q, want %q", decoded.Model, "gpt-5")
		}
	})

	if !strings.Contains(logs, `Unknown JSON fields in client request (ignored): unknown_field`) {
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

func TestArgoOpenAIResponseFieldsDoNotWarn(t *testing.T) {
	logs := captureWarnLogs(t, func() {
		warnUnknownFields(context.Background(), []byte(`{
			"id":"chatcmpl_123",
			"object":"chat.completion",
			"created":123,
			"model":"gpt-5.4-nano",
			"choices":[{
				"index":0,
				"finish_reason":"stop",
				"logprobs":null,
				"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}},
				"message":{
					"role":"assistant",
					"content":"ok",
					"function_call":null,
					"tool_calls":null,
					"refusal":null,
					"annotations":[],
					"audio":null
				}
			}],
			"prompt_filter_results":[{"prompt_index":0,"content_filter_results":{}}],
			"service_tier":"default",
			"system_fingerprint":null,
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
		}`), OpenAIResponse{}, "Argo OpenAI response")
	})

	if strings.Contains(logs, "Unknown JSON fields") {
		t.Fatalf("unexpected unknown-field warning:\n%s", logs)
	}
}

func TestArgoOpenAIStreamFieldsDoNotWarn(t *testing.T) {
	logs := captureWarnLogs(t, func() {
		warnUnknownFields(context.Background(), []byte(`{
			"id":"chatcmpl_123",
			"object":"chat.completion.chunk",
			"created":123,
			"model":"gpt-5.4-nano",
			"choices":[{
				"index":0,
				"delta":{
					"role":"assistant",
					"content":"",
					"function_call":null,
					"tool_calls":null,
					"refusal":null
				},
				"finish_reason":null,
				"logprobs":null,
				"content_filter_results":{"hate":{"filtered":false,"severity":"safe"}}
			}],
			"prompt_filter_results":[{"prompt_index":0,"content_filter_results":{}}],
			"service_tier":"default",
			"system_fingerprint":null,
			"usage":null,
			"obfuscation":"abc"
		}`), OpenAIStreamChunk{}, "Argo OpenAI stream chunk")
	})

	if strings.Contains(logs, "Unknown JSON fields") {
		t.Fatalf("unexpected unknown-field warning:\n%s", logs)
	}
}
