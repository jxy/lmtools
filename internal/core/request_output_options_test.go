package core

import "testing"

func TestOpenAIRequestOutputOptions(t *testing.T) {
	cfg := newProviderSpecTestConfig("openai", "gpt-5", "https://api.openai.com/v1")
	cfg.Effort = "max"
	cfg.JSONMode = true

	_, body, err := BuildChatRequest(cfg, []TypedMessage{NewTextMessage("user", "hi")}, ChatBuildOptions{})
	if err != nil {
		t.Fatalf("BuildChatRequest() error = %v", err)
	}
	payload := decodeRequestBody(t, body)

	if payload["reasoning_effort"] != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh", payload["reasoning_effort"])
	}
	format, ok := payload["response_format"].(map[string]interface{})
	if !ok || format["type"] != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", payload["response_format"])
	}
}

func TestAnthropicRequestOutputOptions(t *testing.T) {
	cfg := newProviderSpecTestConfig("anthropic", "claude-opus-4-7", "https://api.anthropic.com/v1")
	cfg.Effort = "minimal"
	cfg.JSONSchema = []byte(`{"type":"object","properties":{"answer":{"type":"string"}}}`)

	_, body, err := BuildChatRequest(cfg, []TypedMessage{NewTextMessage("user", "hi")}, ChatBuildOptions{})
	if err != nil {
		t.Fatalf("BuildChatRequest() error = %v", err)
	}
	payload := decodeRequestBody(t, body)

	outputConfig, ok := payload["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("output_config = %#v, want object", payload["output_config"])
	}
	if outputConfig["effort"] != "low" {
		t.Fatalf("output_config.effort = %v, want low", outputConfig["effort"])
	}
	format, ok := outputConfig["format"].(map[string]interface{})
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("output_config.format = %#v, want json_schema", outputConfig["format"])
	}
	schema, ok := format["schema"].(map[string]interface{})
	if !ok || schema["type"] != "object" {
		t.Fatalf("output_config.format.schema = %#v, want object schema", format["schema"])
	}
}

func TestGoogleRequestOutputOptions(t *testing.T) {
	cfg := newProviderSpecTestConfig("google", "gemini-2.5-pro", "https://generativelanguage.googleapis.com/v1beta")
	cfg.Effort = "none"
	cfg.JSONSchema = []byte(`{"type":"object","properties":{"answer":{"type":"string"}}}`)

	_, body, err := BuildChatRequest(cfg, []TypedMessage{NewTextMessage("user", "hi")}, ChatBuildOptions{})
	if err != nil {
		t.Fatalf("BuildChatRequest() error = %v", err)
	}
	payload := decodeRequestBody(t, body)

	generationConfig, ok := payload["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatalf("generationConfig = %#v, want object", payload["generationConfig"])
	}
	if generationConfig["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType = %v, want application/json", generationConfig["responseMimeType"])
	}
	thinkingConfig, ok := generationConfig["thinkingConfig"].(map[string]interface{})
	if !ok || thinkingConfig["thinkingBudget"] != float64(0) {
		t.Fatalf("thinkingConfig = %#v, want thinkingBudget 0", generationConfig["thinkingConfig"])
	}
	if _, ok := generationConfig["responseJsonSchema"].(map[string]interface{}); !ok {
		t.Fatalf("responseJsonSchema = %#v, want object", generationConfig["responseJsonSchema"])
	}
}

func TestArgoRequestOutputOptions(t *testing.T) {
	cfg := newProviderSpecTestConfig("argo", "gpt5", "")
	cfg.Effort = "high"
	cfg.JSONMode = true

	_, body, err := BuildChatRequest(cfg, []TypedMessage{NewTextMessage("user", "hi")}, ChatBuildOptions{})
	if err != nil {
		t.Fatalf("BuildChatRequest() error = %v", err)
	}
	payload := decodeRequestBody(t, body)

	if payload["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high", payload["reasoning_effort"])
	}
	format, ok := payload["response_format"].(map[string]interface{})
	if !ok || format["type"] != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", payload["response_format"])
	}
}
