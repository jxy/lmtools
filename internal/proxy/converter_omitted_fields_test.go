package proxy

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"os"
	"strings"
	"testing"
)

func TestConversionWarningsForDroppedFields(t *testing.T) {
	converter := &Converter{}

	logs := captureWarnLogs(t, func() {
		topK := 10
		req := &AnthropicRequest{
			Model:             "gpt-5",
			MaxTokens:         100,
			TopK:              &topK,
			ContextManagement: map[string]interface{}{"clear_tool_results": true},
			ServiceTier:       "standard_only",
			Messages: []AnthropicMessage{
				{
					Role: core.RoleUser,
					Content: json.RawMessage(`[
						{"type":"thinking","thinking":"private"},
						{"type":"text","text":"Hello"}
					]`),
				},
			},
		}
		_, err := converter.ConvertAnthropicToOpenAI(context.Background(), req)
		if err != nil {
			t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
		}
	})

	for _, want := range []string{
		`Dropping Anthropic field "top_k" while converting to OpenAI`,
		`Dropping Anthropic field "context_management" while converting to OpenAI`,
		`Converting Anthropic field "service_tier" to OpenAI with limited fidelity`,
		`Dropping Anthropic field "content[].thinking" while converting to OpenAI`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestOpenAIConversionWarningsForDroppedFields(t *testing.T) {
	converter := &Converter{}
	trueValue := true

	logs := captureWarnLogs(t, func() {
		n := 2
		req := &OpenAIRequest{
			Model:            "claude-opus-4-7",
			Messages:         []OpenAIMessage{{Role: core.RoleUser, Content: "hi"}},
			N:                &n,
			WebSearchOptions: map[string]interface{}{"search_context_size": "low"},
			ServiceTier:      "flex",
			ExtraBody:        map[string]interface{}{"vendor_option": true},
			Temperature:      float64Ptr(0.2),
			ResponseFormat: &ResponseFormat{
				Type: "json_schema",
				JSONSchema: &OpenAIJSONSchema{
					Name:        "answer",
					Description: "Answer schema.",
					Schema:      map[string]interface{}{"type": "object"},
					Strict:      &trueValue,
				},
			},
			Tools: []OpenAITool{{
				Type:     "function",
				Function: OpenAIFunc{Name: "lookup", Parameters: map[string]interface{}{"type": "object"}, Strict: &trueValue},
			}},
		}
		_, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), req)
		if err != nil {
			t.Fatalf("ConvertOpenAIRequestToAnthropic() error = %v", err)
		}
	})

	for _, want := range []string{
		`Dropping OpenAI field "n" while converting to Anthropic`,
		`Dropping OpenAI field "web_search_options" while converting to Anthropic`,
		`Dropping OpenAI field "service_tier" while converting to Anthropic`,
		`Dropping OpenAI field "extra_body" while converting to Anthropic`,
		`Dropping OpenAI field "temperature" while converting to Anthropic`,
		`Converting OpenAI field "response_format.json_schema.name" to Anthropic with limited fidelity`,
		`Converting OpenAI field "response_format.json_schema.description" to Anthropic with limited fidelity`,
		`Converting OpenAI field "response_format.json_schema.strict" to Anthropic with limited fidelity`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestOpenAIToGoogleWarningsForDroppedModernFields(t *testing.T) {
	trueValue := true
	n := 2
	seed := 123
	logprobs := true
	req := &OpenAIRequest{
		Model:                "gemini-2.5-pro",
		Messages:             []OpenAIMessage{{Role: core.RoleUser, Content: "hi", Name: "named-user"}},
		N:                    &n,
		Metadata:             map[string]interface{}{"request_id": "abc"},
		User:                 "end-user",
		ServiceTier:          "flex",
		Seed:                 &seed,
		WebSearchOptions:     map[string]interface{}{"search_context_size": "low"},
		PromptCacheKey:       "cache-key",
		PromptCacheRetention: "24h",
		ParallelToolCalls:    &trueValue,
		Logprobs:             &logprobs,
		ExtraBody:            map[string]interface{}{"vendor_option": true},
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &OpenAIJSONSchema{
				Schema: map[string]interface{}{"type": "object"},
				Strict: &trueValue,
			},
		},
		Tools: []OpenAITool{
			{Type: "custom", Custom: map[string]interface{}{"name": "freeform"}},
			{Type: "function", Function: OpenAIFunc{Name: "lookup", Parameters: map[string]interface{}{"type": "object"}, Strict: &trueValue}},
		},
	}

	logs := captureWarnLogs(t, func() {
		warnOpenAIRequestDropsForGoogle(context.Background(), req)
	})

	for _, want := range []string{
		`Dropping OpenAI field "n" while converting to Google`,
		`Dropping OpenAI field "metadata" while converting to Google`,
		`Dropping OpenAI field "service_tier" while converting to Google`,
		`Dropping OpenAI field "web_search_options" while converting to Google`,
		`Dropping OpenAI field "prompt_cache_key" while converting to Google`,
		`Dropping OpenAI field "parallel_tool_calls" while converting to Google`,
		`Dropping OpenAI field "logprobs" while converting to Google`,
		`Dropping OpenAI field "extra_body" while converting to Google`,
		`Converting OpenAI field "response_format.json_schema.strict" to Google with limited fidelity`,
		`Dropping OpenAI field "messages[].name" while converting to Google`,
		`Converting OpenAI field "tools[].custom" to Google with limited fidelity`,
		`Converting OpenAI field "tools[].function.strict" to Google with limited fidelity`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestOpenAIInstructionRoleWarningsForAnthropic(t *testing.T) {
	converter := &Converter{}
	req := &OpenAIRequest{
		Model: "claude-opus-4-7",
		Messages: []OpenAIMessage{
			{Role: core.RoleDeveloper, Content: "prefix"},
			{Role: core.RoleUser, Content: "user"},
			{Role: core.RoleSystem, Content: "mid-stream"},
		},
	}

	logs := captureWarnLogs(t, func() {
		_, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), req)
		if err != nil {
			t.Fatalf("ConvertOpenAIRequestToAnthropic() error = %v", err)
		}
	})

	for _, want := range []string{
		`Converting OpenAI field "messages[0].role" to Anthropic with limited fidelity`,
		`Converting OpenAI field "messages[2].role" to Anthropic with limited fidelity`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestOpenAIInstructionRoleWarningsForGoogle(t *testing.T) {
	req := &OpenAIRequest{
		Model: "gemini-2.5-pro",
		Messages: []OpenAIMessage{
			{Role: core.RoleDeveloper, Content: "prefix"},
			{Role: core.RoleUser, Content: "user"},
			{Role: core.RoleSystem, Content: "mid-stream"},
		},
	}

	logs := captureWarnLogs(t, func() {
		warnOpenAIRequestDropsForGoogle(context.Background(), req)
	})

	for _, want := range []string{
		`Converting OpenAI field "messages[0].role" to Google with limited fidelity`,
		`Converting OpenAI field "messages[2].role" to Google with limited fidelity`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestAnthropicToGoogleWarningsForDroppedModernFields(t *testing.T) {
	converter := &Converter{}

	logs := captureWarnLogs(t, func() {
		req := &AnthropicRequest{
			Model:             "gemini-2.5-pro",
			MaxTokens:         100,
			Metadata:          map[string]interface{}{"request_id": "abc"},
			ServiceTier:       "priority",
			Container:         "container_123",
			ContextManagement: map[string]interface{}{"clear_tool_results": true},
			MCPServers:        []interface{}{map[string]interface{}{"type": "url", "url": "https://example.test/mcp"}},
			OutputConfig: &AnthropicOutputConfig{
				Format: map[string]interface{}{"type": "json_schema", "schema": map[string]interface{}{"type": "object"}},
			},
			Messages: []AnthropicMessage{
				{
					Role: core.RoleUser,
					Content: json.RawMessage(`[
						{"type":"text","text":"Hello","cache_control":{"type":"ephemeral"}}
					]`),
				},
			},
			Tools: []AnthropicTool{
				{
					Type:         "web_search_20250305",
					Name:         "web_search",
					CacheControl: &AnthropicCacheControl{Type: "ephemeral"},
				},
			},
		}
		_, err := converter.ConvertAnthropicToGoogle(context.Background(), req)
		if err != nil {
			t.Fatalf("ConvertAnthropicToGoogle() error = %v", err)
		}
	})

	for _, want := range []string{
		`Dropping Anthropic field "metadata" while converting to Google`,
		`Dropping Anthropic field "service_tier" while converting to Google`,
		`Dropping Anthropic field "container" while converting to Google`,
		`Dropping Anthropic field "context_management" while converting to Google`,
		`Dropping Anthropic field "mcp_servers" while converting to Google`,
		`Converting Anthropic field "output_config.format" to Google with limited fidelity`,
		`Dropping Anthropic field "tools[].web_search_20250305" while converting to Google`,
		`Dropping Anthropic field "tools[].cache_control" while converting to Google`,
		`Dropping Anthropic field "content[].cache_control" while converting to Google`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("warning %q not found in logs:\n%s", want, logs)
		}
	}
}

func TestArgoConversionWarningsUseWarnLevel(t *testing.T) {
	converter := NewConverter(NewModelMapper(&Config{Provider: constants.ProviderArgo}))

	logs := captureWarnLogs(t, func() {
		topK := 5
		req := &AnthropicRequest{
			Model:     "gpt-5",
			MaxTokens: 100,
			TopK:      &topK,
			Messages: []AnthropicMessage{
				{Role: core.RoleUser, Content: json.RawMessage(`"Hello Argo"`)},
			},
		}
		_, err := converter.ConvertAnthropicToArgo(context.Background(), req, "testuser")
		if err != nil {
			t.Fatalf("ConvertAnthropicToArgo() error = %v", err)
		}
	})

	if !strings.Contains(logs, `[WARN]`) || !strings.Contains(logs, `Dropping Anthropic field "top_k"`) {
		t.Fatalf("expected warn-level top_k conversion log, got:\n%s", logs)
	}
}

func captureWarnLogs(t *testing.T, fn func()) string {
	t.Helper()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stderr = w

	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("warn"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	); err != nil {
		t.Fatalf("InitializeWithOptions() error = %v", err)
	}

	fn()

	_ = w.Close()
	captured, _ := io.ReadAll(r)
	os.Stderr = oldStderr

	logger.ResetForTesting()
	_ = logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
		logger.WithComponent("test"),
	)

	return string(captured)
}
