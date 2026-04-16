package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"testing"
)

func float64Ptr(v float64) *float64 {
	return &v
}

func TestConvertAnthropicToGoogle_TypedRenderer(t *testing.T) {
	converter := &Converter{}
	topK := 7

	req := &AnthropicRequest{
		Model:       "gemini-1.5-pro",
		MaxTokens:   512,
		System:      json.RawMessage(`"Be concise."`),
		Temperature: float64Ptr(0.2),
		TopP:        float64Ptr(0.9),
		TopK:        &topK,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"What is the weather in Paris?"`),
			},
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"text","text":"I will check."},
					{"type":"tool_use","id":"tool_123","name":"get_weather","input":{"location":"Paris"}}
				]`),
			},
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"tool_result","tool_use_id":"tool_123","content":"Sunny and 22C"}
				]`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "get_weather",
				Description: "Get the weather for a city",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
		},
	}

	googleReq, err := converter.ConvertAnthropicToGoogle(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToGoogle() error = %v", err)
	}

	if googleReq.GenerationConfig == nil {
		t.Fatal("GenerationConfig should be present")
	}
	if googleReq.GenerationConfig.MaxOutputTokens == nil || *googleReq.GenerationConfig.MaxOutputTokens != 512 {
		t.Fatalf("MaxOutputTokens = %v, want 512", googleReq.GenerationConfig.MaxOutputTokens)
	}
	if googleReq.GenerationConfig.TopK == nil || *googleReq.GenerationConfig.TopK != topK {
		t.Fatalf("TopK = %v, want %d", googleReq.GenerationConfig.TopK, topK)
	}

	if googleReq.SystemInstruction == nil {
		t.Fatal("SystemInstruction should be present")
	}
	if got := googleReq.SystemInstruction.Parts[0].Text; got != "Be concise." {
		t.Fatalf("system instruction = %q, want %q", got, "Be concise.")
	}
	if len(googleReq.Contents) != 3 {
		t.Fatalf("len(Contents) = %d, want 3", len(googleReq.Contents))
	}
	if got := googleReq.Contents[0].Role; got != "user" {
		t.Fatalf("user role = %q, want %q", got, "user")
	}
	if got := googleReq.Contents[1].Role; got != "model" {
		t.Fatalf("assistant role = %q, want %q", got, "model")
	}

	assistantParts := googleReq.Contents[1].Parts
	if len(assistantParts) != 2 {
		t.Fatalf("assistant parts = %d, want 2", len(assistantParts))
	}
	if assistantParts[0].Text != "I will check." {
		t.Fatalf("assistant text = %q, want %q", assistantParts[0].Text, "I will check.")
	}
	if assistantParts[1].FunctionCall == nil {
		t.Fatal("assistant function call should be present")
	}
	if got := assistantParts[1].ThoughtSignature; got != core.GoogleDummyThoughtSignature {
		t.Fatalf("thought signature = %q, want %q", got, core.GoogleDummyThoughtSignature)
	}
	if assistantParts[1].FunctionCall.Name != "get_weather" {
		t.Fatalf("function call name = %q, want %q", assistantParts[1].FunctionCall.Name, "get_weather")
	}
	if got := assistantParts[1].FunctionCall.Args["location"]; got != "Paris" {
		t.Fatalf("function call args = %v, want Paris", assistantParts[1].FunctionCall.Args)
	}

	toolResultParts := googleReq.Contents[2].Parts
	if len(toolResultParts) != 1 || toolResultParts[0].FunctionResp == nil {
		t.Fatalf("tool result parts = %+v, want single functionResponse", toolResultParts)
	}
	if toolResultParts[0].FunctionResp.Name != "tool_123" {
		t.Fatalf("function response name = %q, want %q", toolResultParts[0].FunctionResp.Name, "tool_123")
	}
	if got := toolResultParts[0].FunctionResp.Response["content"]; got != "Sunny and 22C" {
		t.Fatalf("function response payload = %+v, want content=Sunny and 22C", toolResultParts[0].FunctionResp.Response)
	}

	if len(googleReq.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(googleReq.Tools))
	}
	if len(googleReq.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("len(FunctionDeclarations) = %d, want 1", len(googleReq.Tools[0].FunctionDeclarations))
	}
	if googleReq.Tools[0].FunctionDeclarations[0].Name != "get_weather" {
		t.Fatalf("tool name = %q, want %q", googleReq.Tools[0].FunctionDeclarations[0].Name, "get_weather")
	}
}

func TestTypedToGoogleRequest_PreservesTextThoughtSignature(t *testing.T) {
	req, err := TypedToGoogleRequest(TypedRequest{
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "Solve this."},
				},
			},
			{
				Role: string(core.RoleAssistant),
				Blocks: []core.Block{
					core.TextBlock{
						Text:             "Let me think.",
						ThoughtSignature: "sig-text-123",
					},
				},
			},
		},
	}, "gemini-3.1-flash-lite-preview", nil)
	if err != nil {
		t.Fatalf("TypedToGoogleRequest() error = %v", err)
	}

	if len(req.Contents) != 2 {
		t.Fatalf("len(Contents) = %d, want 2", len(req.Contents))
	}

	assistantParts := req.Contents[1].Parts
	if len(assistantParts) != 1 {
		t.Fatalf("assistant parts = %d, want 1", len(assistantParts))
	}
	if got := assistantParts[0].Text; got != "Let me think." {
		t.Fatalf("assistant text = %q, want %q", got, "Let me think.")
	}
	if got := assistantParts[0].ThoughtSignature; got != "sig-text-123" {
		t.Fatalf("thought signature = %q, want %q", got, "sig-text-123")
	}
}
