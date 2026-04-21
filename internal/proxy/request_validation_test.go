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
		"model": "gpt-4o-mini",
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
	if resp.Error.Message != `field "metadata" is not supported when proxying to provider "openai"` {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

func TestHandleOpenAIRejectsUnsupportedResponseFormatForArgo(t *testing.T) {
	server := NewMinimalTestServer(t, &Config{
		Provider: constants.ProviderArgo,
		ArgoUser: "fixture-user",
	})

	body := `{
		"model": "claude-test",
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
	if resp.Error.Message != `field "response_format" is not supported when proxying to provider "anthropic"` {
		t.Fatalf("message = %q", resp.Error.Message)
	}
}

func TestValidateAnthropicOpus47Features(t *testing.T) {
	tests := []struct {
		name     string
		req      AnthropicRequest
		provider string
		wantErr  string
	}{
		{
			name:     "allows adaptive thinking and effort on official opus 4.7 anthropic route",
			provider: constants.ProviderAnthropic,
			req: AnthropicRequest{
				Model:     "claude-opus-4-7",
				MaxTokens: 100,
				Messages:  []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
				Thinking: &AnthropicThinking{
					Type:    "adaptive",
					Display: "summarized",
				},
				OutputConfig: &AnthropicOutputConfig{Effort: "xhigh"},
			},
		},
		{
			name:     "rejects opus 4.7 fields on argo",
			provider: constants.ProviderArgo,
			req: AnthropicRequest{
				Model:        "claude-opus-4-7",
				Messages:     []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
				OutputConfig: &AnthropicOutputConfig{Effort: "high"},
			},
			wantErr: `anthropic Opus 4.7 thinking/output_config fields are only supported when proxying to provider "anthropic"`,
		},
		{
			name:     "rejects opus 4.7 fields on non opus model",
			provider: constants.ProviderAnthropic,
			req: AnthropicRequest{
				Model:        "claude-sonnet-4-5",
				Messages:     []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
				OutputConfig: &AnthropicOutputConfig{Effort: "high"},
			},
			wantErr: `anthropic Opus 4.7 thinking/output_config fields require model "claude-opus-4-7"`,
		},
		{
			name:     "rejects budget tokens with adaptive thinking",
			provider: constants.ProviderAnthropic,
			req: AnthropicRequest{
				Model:    "claude-opus-4-7",
				Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
				Thinking: &AnthropicThinking{
					Type:         "adaptive",
					BudgetTokens: 1024,
				},
			},
			wantErr: `thinking.budget_tokens is not valid with thinking.type="adaptive"`,
		},
		{
			name:     "rejects unknown effort",
			provider: constants.ProviderAnthropic,
			req: AnthropicRequest{
				Model:        "claude-opus-4-7",
				Messages:     []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
				OutputConfig: &AnthropicOutputConfig{Effort: "extreme"},
			},
			wantErr: "output_config.effort must be one of low, medium, high, xhigh, max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAnthropicRequestForProvider(&tt.req, tt.provider)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAnthropicRequestForProvider() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected validation error")
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}
