package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"testing"
)

func TestDecodeStrictJSONAcceptsModernOpenAIChatFields(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5",
		"messages": [
			{"role": "developer", "content": "follow policy"},
			{"role": "user", "content": "hi"}
		],
		"reasoning_effort": "xhigh",
		"verbosity": "low",
		"metadata": {"request_id": "abc"},
		"store": false,
		"service_tier": "flex",
		"seed": 123,
		"modalities": ["text"],
		"audio": {"format": "wav", "voice": "alloy"},
		"prediction": {"type": "content", "content": "cached"},
		"web_search_options": {"search_context_size": "low"},
		"prompt_cache_key": "cache-key",
		"prompt_cache_retention": "24h",
		"safety_identifier": "safe-user",
		"parallel_tool_calls": false,
		"logprobs": true,
		"top_logprobs": 3,
		"stream_options": {"include_usage": true, "include_obfuscation": true},
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "answer",
				"schema": {"type": "object"},
				"strict": true
			}
		},
		"tools": [
			{"type": "custom", "custom": {"name": "freeform"}}
		]
	}`)

	var req OpenAIRequest
	if err := decodeStrictJSON(body, &req); err != nil {
		t.Fatalf("decodeStrictJSON() error = %v", err)
	}
	if req.Messages[0].Role != core.RoleDeveloper {
		t.Fatalf("first role = %q, want developer", req.Messages[0].Role)
	}
	if req.ReasoningEffort != "xhigh" || req.Verbosity != "low" || req.ServiceTier != "flex" {
		t.Fatalf("modern scalar fields not decoded: %+v", req)
	}
	if req.ResponseFormat == nil || req.ResponseFormat.JSONSchema == nil || req.ResponseFormat.JSONSchema.Name != "answer" {
		t.Fatalf("response_format not decoded: %+v", req.ResponseFormat)
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage || req.StreamOptions.IncludeObfuscation == nil || !*req.StreamOptions.IncludeObfuscation {
		t.Fatalf("stream_options not decoded: %+v", req.StreamOptions)
	}
	if len(req.Tools) != 1 || req.Tools[0].Custom == nil {
		t.Fatalf("custom tool not decoded: %+v", req.Tools)
	}
}

func TestDecodeStrictJSONAcceptsModernAnthropicFields(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-7",
		"max_tokens": 128,
		"messages": [{"role":"user","content":"hi"}],
		"container": "container_123",
		"context_management": {"clear_tool_results": true},
		"service_tier": "priority",
		"inference_geo": "us",
		"speed": "standard",
		"cache_control": {"type": "ephemeral", "ttl": "1h"},
		"mcp_servers": [{"type": "url", "url": "https://example.test/mcp"}],
		"output_config": {
			"effort": "high",
			"format": {"type": "json_schema", "schema": {"type": "object"}}
		},
		"tools": [{
			"type": "web_search_20250305",
			"name": "web_search",
			"cache_control": {"type": "ephemeral"}
		}]
	}`)

	var req AnthropicRequest
	if err := decodeStrictJSON(body, &req); err != nil {
		t.Fatalf("decodeStrictJSON() error = %v", err)
	}
	if req.Container != "container_123" || req.ServiceTier != "priority" || req.InferenceGeo != "us" {
		t.Fatalf("modern Anthropic fields not decoded: %+v", req)
	}
	if req.OutputConfig == nil || req.OutputConfig.Effort != "high" || req.OutputConfig.Format == nil {
		t.Fatalf("output_config not decoded: %+v", req.OutputConfig)
	}
	if len(req.MCPServers) != 1 || len(req.Tools) != 1 || req.Tools[0].CacheControl == nil {
		t.Fatalf("mcp/tools cache fields not decoded: %+v", req)
	}
}

func TestOpenAIToAnthropicConvertsDeveloperEffortAndStructuredOutput(t *testing.T) {
	converter := &Converter{}
	req := &OpenAIRequest{
		Model: "claude-opus-4-7",
		Messages: []OpenAIMessage{
			{Role: core.RoleDeveloper, Content: "developer instructions"},
			{Role: core.RoleSystem, Content: "system instructions"},
			{Role: core.RoleUser, Content: "return json"},
		},
		ReasoningEffort: "xhigh",
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &OpenAIJSONSchema{
				Name:   "answer",
				Schema: map[string]interface{}{"type": "object"},
			},
		},
		User:        "end-user",
		ServiceTier: "default",
		StreamOptions: &OpenAIStreamOptions{
			IncludeUsage: true,
		},
	}

	got, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertOpenAIRequestToAnthropic() error = %v", err)
	}
	system, err := extractSystemContent(got.System)
	if err != nil {
		t.Fatalf("extractSystemContent() error = %v", err)
	}
	if system != "developer instructions\nsystem instructions" {
		t.Fatalf("system = %q, want leading instruction order preserved", system)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != core.RoleUser {
		t.Fatalf("Messages = %+v, want user message only after collapsing instruction prefix", got.Messages)
	}
	if got.OutputConfig == nil || got.OutputConfig.Effort != "xhigh" || got.OutputConfig.Format == nil {
		t.Fatalf("OutputConfig = %+v, want effort and format", got.OutputConfig)
	}
	format, ok := got.OutputConfig.Format.(map[string]interface{})
	if !ok || format["name"] != nil || format["description"] != nil || format["strict"] != nil {
		t.Fatalf("OutputConfig.Format = %+v, want only Anthropic-supported format fields", got.OutputConfig.Format)
	}
	if got.Metadata["user_id"] != "end-user" || got.ServiceTier != "standard_only" {
		t.Fatalf("metadata/service tier not converted: metadata=%v service_tier=%q", got.Metadata, got.ServiceTier)
	}
	if got.Metadata[constants.IncludeUsageKey] != true {
		t.Fatalf("stream include_usage metadata not converted: metadata=%v", got.Metadata)
	}
}

func TestOpenAIToAnthropicPreservesMidConversationInstructionOrderByCoercingRole(t *testing.T) {
	converter := &Converter{}
	req := &OpenAIRequest{
		Model: "claude-opus-4-7",
		Messages: []OpenAIMessage{
			{Role: core.RoleUser, Content: "first"},
			{Role: core.RoleDeveloper, Content: "stay in order"},
			{Role: core.RoleAssistant, Content: "second"},
		},
	}

	got, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertOpenAIRequestToAnthropic() error = %v", err)
	}
	if got.System != nil {
		t.Fatalf("System = %s, want nil with no leading instruction prefix", string(got.System))
	}
	if len(got.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(got.Messages))
	}
	if got.Messages[1].Role != core.RoleUser {
		t.Fatalf("Messages[1].Role = %q, want user", got.Messages[1].Role)
	}
	contentText, _, err := parseAnthropicMessageContent(got.Messages[1].Content)
	if err != nil {
		t.Fatalf("parseAnthropicMessageContent() error = %v", err)
	}
	if contentText == nil || *contentText != "stay in order" {
		t.Fatalf("Messages[1].Content = %v, want preserved developer text", contentText)
	}
}

func TestAnthropicToOpenAIConvertsEffortFormatMetadataAndServiceTier(t *testing.T) {
	converter := &Converter{}
	req := &AnthropicRequest{
		Model:       "gpt-5",
		MaxTokens:   128,
		Messages:    []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"return json"`)}},
		Metadata:    map[string]interface{}{"request_id": "abc"},
		ServiceTier: "standard_only",
		OutputConfig: &AnthropicOutputConfig{
			Effort: "max",
			Format: map[string]interface{}{
				"type":   "json_schema",
				"schema": map[string]interface{}{"type": "object"},
			},
		},
	}

	got, err := converter.ConvertAnthropicToOpenAI(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}
	if got.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want xhigh", got.ReasoningEffort)
	}
	if got.ResponseFormat == nil || got.ResponseFormat.Type != "json_schema" || got.ResponseFormat.JSONSchema == nil || got.ResponseFormat.JSONSchema.Name != "response" {
		t.Fatalf("ResponseFormat = %+v, want json_schema response", got.ResponseFormat)
	}
	if got.Metadata["request_id"] != "abc" || got.ServiceTier != "default" {
		t.Fatalf("metadata/service tier not converted: metadata=%v service_tier=%q", got.Metadata, got.ServiceTier)
	}
}

func TestOpenAIToGoogleConvertsDeveloperEffortAndStructuredOutput(t *testing.T) {
	req := &OpenAIRequest{
		Model: "gemini-2.5-pro",
		Messages: []OpenAIMessage{
			{Role: core.RoleDeveloper, Content: "developer instructions"},
			{Role: core.RoleSystem, Content: "system instructions"},
			{Role: core.RoleUser, Content: "return json"},
		},
		ReasoningEffort: "medium",
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &OpenAIJSONSchema{
				Schema: map[string]interface{}{"type": "object"},
			},
		},
	}

	googleReq, err := TypedToGoogleRequest(OpenAIRequestToTyped(req), req.Model, nil)
	if err != nil {
		t.Fatalf("TypedToGoogleRequest() error = %v", err)
	}
	system := googleReq.SystemInstruction.Parts[0].Text
	if system != "developer instructions\nsystem instructions" {
		t.Fatalf("systemInstruction = %q, want leading instruction order preserved", system)
	}
	if googleReq.GenerationConfig.ResponseMIMEType != "application/json" || googleReq.GenerationConfig.ResponseJSONSchema == nil {
		t.Fatalf("Google response schema not configured: %+v", googleReq.GenerationConfig)
	}
	if googleReq.GenerationConfig.ThinkingConfig == nil || googleReq.GenerationConfig.ThinkingConfig.ThinkingBudget == nil || *googleReq.GenerationConfig.ThinkingConfig.ThinkingBudget != 8192 {
		t.Fatalf("Google thinking config = %+v, want budget 8192", googleReq.GenerationConfig.ThinkingConfig)
	}
}
