package proxy

import (
	"context"
	"encoding/json"
	"testing"
)

func convertImageRequestToGoogle(t *testing.T, content string) *GoogleRequest {
	t.Helper()
	req := &AnthropicRequest{
		Model:     "gemini-test",
		MaxTokens: 64,
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(content),
		}},
	}
	googleReq, err := ConvertAnthropicToGoogle(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToGoogle() error = %v", err)
	}
	if len(googleReq.Contents) != 1 || len(googleReq.Contents[0].Parts) != 2 {
		t.Fatalf("Contents = %+v, want one message with two parts", googleReq.Contents)
	}
	return googleReq
}

func TestConvertAnthropicToGoogleRendersBase64ImageInline(t *testing.T) {
	googleReq := convertImageRequestToGoogle(t, `[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},
		{"type":"text","text":"what is this?"}
	]`)

	parts := googleReq.Contents[0].Parts
	if parts[0].InlineData == nil {
		t.Fatalf("parts[0] = %+v, want an inlineData part", parts[0])
	}
	if parts[0].InlineData.MimeType != "image/png" || parts[0].InlineData.Data != "iVBORw0KGgo=" {
		t.Fatalf("inlineData = %+v, want image/png with the client's payload", parts[0].InlineData)
	}
	if parts[0].Text != "" {
		t.Fatalf("inlineData part also carries text %q", parts[0].Text)
	}
	if parts[1].Text != "what is this?" {
		t.Fatalf("parts[1] = %+v, want the prompt", parts[1])
	}
}

func TestConvertAnthropicToGoogleKeepsURLImageAsTextMention(t *testing.T) {
	googleReq := convertImageRequestToGoogle(t, `[
		{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},
		{"type":"text","text":"what is this?"}
	]`)

	parts := googleReq.Contents[0].Parts
	if parts[0].InlineData != nil {
		t.Fatalf("URL image rendered as inlineData: %+v", parts[0].InlineData)
	}
	if parts[0].Text != "[Image: https://example.com/a.png]" {
		t.Fatalf("parts[0] = %+v, want the text mention", parts[0])
	}
}
