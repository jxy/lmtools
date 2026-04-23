package proxy

import (
	"encoding/json"
	"lmtools/internal/constants"
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

func TestValidateAnthropicAllowsWarnOnlyFieldsForConvertedProviders(t *testing.T) {
	req := &AnthropicRequest{
		Model:     "gpt-4o-mini",
		MaxTokens: 10,
		Messages:  []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		Metadata:  map[string]interface{}{"request_id": "123"},
		TopK:      intPtr(40),
	}

	if err := validateAnthropicRequestForProvider(req, constants.ProviderArgo); err != nil {
		t.Fatalf("validateAnthropicRequestForProvider() error = %v", err)
	}
}

func TestValidateOpenAIAllowsWarnOnlyFieldsForConvertedProviders(t *testing.T) {
	req := &OpenAIRequest{
		Model:    "claude-test",
		Messages: []OpenAIMessage{{Role: "user", Content: "hi"}},
		ResponseFormat: &ResponseFormat{
			Type: "json_object",
		},
		StreamOptions: &OpenAIStreamOptions{IncludeUsage: true},
	}

	if err := validateOpenAIRequestForProvider(req, constants.ProviderArgo, "gpt-5"); err != nil {
		t.Fatalf("validateOpenAIRequestForProvider() error = %v", err)
	}
}

func TestValidateOpenAIRejectsAnthropicOutputConfigFeaturesOnNonOpusTarget(t *testing.T) {
	req := &OpenAIRequest{
		Model:           "claude-test",
		Messages:        []OpenAIMessage{{Role: "user", Content: "hi"}},
		ReasoningEffort: "high",
		ResponseFormat:  &ResponseFormat{Type: "json_object"},
		StreamOptions:   &OpenAIStreamOptions{IncludeUsage: true},
	}

	err := validateOpenAIRequestForProvider(req, constants.ProviderAnthropic, "claude-sonnet-4-5")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if got := err.Error(); got != `anthropic Opus 4.7 thinking/output_config fields require model "claude-opus-4-7"` {
		t.Fatalf("error = %q, want opus 4.7 validation", got)
	}
}

func TestValidateOpenAIAllowsAnthropicOutputConfigFeaturesOnOpusTarget(t *testing.T) {
	req := &OpenAIRequest{
		Model:           "claude-test",
		Messages:        []OpenAIMessage{{Role: "user", Content: "hi"}},
		ReasoningEffort: "xhigh",
		ResponseFormat:  &ResponseFormat{Type: "json_schema", JSONSchema: &OpenAIJSONSchema{Schema: map[string]interface{}{"type": "object"}}},
	}

	if err := validateOpenAIRequestForProvider(req, constants.ProviderArgo, "claude-opus-4-7"); err != nil {
		t.Fatalf("validateOpenAIRequestForProvider() error = %v", err)
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
			name:     "allows opus 4.7 fields on argo conversion route",
			provider: constants.ProviderArgo,
			req: AnthropicRequest{
				Model:        "claude-opus-4-7",
				Messages:     []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
				OutputConfig: &AnthropicOutputConfig{Effort: "high"},
			},
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
