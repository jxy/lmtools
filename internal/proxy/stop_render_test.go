package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
	"strings"
	"testing"
)

func TestRenderTypedToOpenAIRequestPreservesStop(t *testing.T) {
	typed := TypedRequest{
		Messages: []core.TypedMessage{
			core.NewTextMessage("user", "hello"),
		},
		Stop: []string{"END"},
	}
	req, err := TypedToOpenAIRequest(typed, "gpt-test")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(body), `"stop":["END"]`) {
		t.Fatalf("OpenAI request should preserve stop, got %s", body)
	}
}

func TestRenderTypedToArgoRequestPreservesStop(t *testing.T) {
	typed := TypedRequest{
		Messages: []core.TypedMessage{
			core.NewTextMessage("user", "hello"),
		},
		Stop: []string{"END"},
	}
	openAIReq, err := TypedToArgoRequest(typed, "gpt-test", "user")
	if err != nil {
		t.Fatalf("TypedToArgoRequest(openai) error = %v", err)
	}
	openAIBody, _ := json.Marshal(openAIReq)
	if !strings.Contains(string(openAIBody), `"stop":["END"]`) {
		t.Fatalf("Argo OpenAI request should preserve stop, got %s", openAIBody)
	}

	claudeReq, err := TypedToArgoRequest(typed, "claude-sonnet-4", "user")
	if err != nil {
		t.Fatalf("TypedToArgoRequest(claude) error = %v", err)
	}
	claudeBody, _ := json.Marshal(claudeReq)
	if !strings.Contains(string(claudeBody), `"stop":["END"]`) {
		t.Fatalf("Argo Claude request should preserve stop, got %s", claudeBody)
	}
}
