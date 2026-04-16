package proxy

import (
	"bytes"
	"encoding/json"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecodeStrictJSONRejectsUnknownField(t *testing.T) {
	var req AnthropicRequest
	err := decodeStrictJSON([]byte(`{"model":"claude-test","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"unknown_field":true}`), &req)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if got := err.Error(); got != `invalid JSON in request body: unknown field "unknown_field"` {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestHandleMessagesRejectsUnsupportedMetadataForArgo(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{
		Provider: constants.ProviderArgo,
		ArgoUser: "fixture-user",
	})

	body := `{
		"model": "claude-test",
		"max_tokens": 10,
		"messages": [{"role":"user","content":"hi"}],
		"metadata": {"request_id": "123"}
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	server.handleMessages(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}

	var resp AnthropicError
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if resp.Error.Message != `field "metadata" is not supported when proxying to provider "argo"` {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

func TestHandleOpenAIRejectsUnsupportedResponseFormatForArgo(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{
		Provider: constants.ProviderArgo,
		ArgoUser: "fixture-user",
	})

	body := `{
		"model": "gpt-4o-mini",
		"messages": [{"role":"user","content":"hi"}],
		"response_format": {"type":"json_object"}
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	server.handleOpenAIChatCompletions(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}

	var resp OpenAIError
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if resp.Error.Message != `field "response_format" is not supported when proxying to provider "argo"` {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}
