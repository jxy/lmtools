package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strings"
	"testing"
)

func TestOpenAIResponsesRequestToTypedStrict(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model:        "gpt-5.4-nano",
		Instructions: "Use concise JSON.",
		Input: []interface{}{
			map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "Return a city."},
				},
			},
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup",
				"arguments": `{"city":"Chicago"}`,
			},
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  "ok",
			},
		},
		Reasoning:       &OpenAIResponsesReasoning{Effort: "low"},
		MaxOutputTokens: intPtr(64),
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	if typed.Developer != "Use concise JSON." {
		t.Fatalf("developer = %q", typed.Developer)
	}
	if typed.ReasoningEffort != "low" {
		t.Fatalf("reasoning effort = %q", typed.ReasoningEffort)
	}
	if typed.MaxTokens == nil || *typed.MaxTokens != 64 {
		t.Fatalf("max tokens = %v", typed.MaxTokens)
	}
	if len(typed.Messages) != 3 {
		t.Fatalf("messages = %+v", typed.Messages)
	}
}

func TestOpenAIResponsesRefusalPartsReadRefusalField(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: []interface{}{
			map[string]interface{}{
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "refusal", "refusal": "I can't help with that."},
					map[string]interface{}{"type": "output_refusal", "refusal": "I still can't help with that."},
				},
			},
		},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	if len(typed.Messages) != 1 {
		t.Fatalf("messages = %+v, want one assistant message", typed.Messages)
	}
	if typed.Messages[0].Role != string(core.RoleAssistant) {
		t.Fatalf("role = %q, want assistant", typed.Messages[0].Role)
	}

	want := []string{"I can't help with that.", "I still can't help with that."}
	if len(typed.Messages[0].Blocks) != len(want) {
		t.Fatalf("blocks = %#v, want %d text blocks", typed.Messages[0].Blocks, len(want))
	}
	for i, block := range typed.Messages[0].Blocks {
		text, ok := block.(core.TextBlock)
		if !ok {
			t.Fatalf("block[%d] = %T, want core.TextBlock", i, block)
		}
		if text.Text != want[i] {
			t.Fatalf("block[%d].Text = %q, want %q", i, text.Text, want[i])
		}
	}
}

func TestOpenAIResponsesAnthropicWireDefaultMaxTokens(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     int
	}{
		{
			name:     "anthropic opus uses max opus default",
			provider: constants.ProviderAnthropic,
			model:    "claude-opus-4-1-20250805",
			want:     defaultResponsesClaudeOpusMaxTokens,
		},
		{
			name:     "anthropic sonnet uses default claude max",
			provider: constants.ProviderAnthropic,
			model:    "claude-sonnet-4-5-20250929",
			want:     defaultResponsesClaudeDefaultMaxTokens,
		},
		{
			name:     "anthropic haiku uses default claude max",
			provider: constants.ProviderAnthropic,
			model:    "claude-haiku-4-5",
			want:     defaultResponsesClaudeDefaultMaxTokens,
		},
		{
			name:     "argo claude opus uses max opus default",
			provider: constants.ProviderArgo,
			model:    "claude-opus-4-1",
			want:     defaultResponsesClaudeOpusMaxTokens,
		},
		{
			name:     "argo claude alias uses default claude max",
			provider: constants.ProviderArgo,
			model:    "claudesonnet4",
			want:     defaultResponsesClaudeDefaultMaxTokens,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typed := TypedRequest{Messages: []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), "hi")}}
			typed = ensureResponsesAnthropicWireMaxTokens(typed, tt.provider, tt.model)
			anthReq, err := TypedToAnthropicRequest(typed, tt.model)
			if err != nil {
				t.Fatalf("TypedToAnthropicRequest() error = %v", err)
			}
			if anthReq.MaxTokens != tt.want {
				t.Fatalf("MaxTokens = %d, want %d", anthReq.MaxTokens, tt.want)
			}
		})
	}
}

func TestOpenAIResponsesAnthropicWirePreservesExplicitMaxTokens(t *testing.T) {
	typed := TypedRequest{
		Messages:  []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), "hi")},
		MaxTokens: intPtr(64),
	}
	typed = ensureResponsesAnthropicWireMaxTokens(typed, constants.ProviderAnthropic, "claude-opus-4-1")
	anthReq, err := TypedToAnthropicRequest(typed, "claude-opus-4-1")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if anthReq.MaxTokens != 64 {
		t.Fatalf("MaxTokens = %d, want explicit 64", anthReq.MaxTokens)
	}
}

func TestOpenAIResponsesAnthropicWireDoesNotDefaultNonClaudeArgo(t *testing.T) {
	typed := TypedRequest{Messages: []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), "hi")}}
	typed = ensureResponsesAnthropicWireMaxTokens(typed, constants.ProviderArgo, "gpt-5")
	if typed.MaxTokens != nil {
		t.Fatalf("MaxTokens = %v, want nil for non-Claude Argo", *typed.MaxTokens)
	}
}

func TestOpenAIResponsesFunctionCallOutputArrayToTyped(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: []interface{}{
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "first"},
					map[string]interface{}{"type": "input_text", "text": "second"},
				},
			},
		},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	if len(typed.Messages) != 1 || len(typed.Messages[0].Blocks) != 1 {
		t.Fatalf("messages = %+v", typed.Messages)
	}
	if typed.Messages[0].Role != string(core.RoleUser) {
		t.Fatalf("tool result role = %q, want user", typed.Messages[0].Role)
	}
	block, ok := typed.Messages[0].Blocks[0].(core.ToolResultBlock)
	if !ok {
		t.Fatalf("block = %T", typed.Messages[0].Blocks[0])
	}
	if block.Content != "first\nsecond" {
		t.Fatalf("tool result content = %q", block.Content)
	}
}

func TestOpenAIResponsesFunctionCallOutputPreservesToolName(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_1",
				"name":      "lookup_city",
				"arguments": `{"city":"Chicago"}`,
			},
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  `{"country":"United States"}`,
			},
		},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	if len(typed.Messages) != 2 || len(typed.Messages[1].Blocks) != 1 {
		t.Fatalf("messages = %+v", typed.Messages)
	}
	block, ok := typed.Messages[1].Blocks[0].(core.ToolResultBlock)
	if !ok {
		t.Fatalf("block = %T", typed.Messages[1].Blocks[0])
	}
	if block.Name != "lookup_city" {
		t.Fatalf("tool result name = %q, want lookup_city", block.Name)
	}
}

func TestOpenAIResponsesFunctionCallOutputRendersAsAnthropicUserToolResult(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: []interface{}{
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_1",
				"output":  "tool result",
			},
		},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	anthReq, err := TypedToAnthropicRequest(typed, "claude-test")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if len(anthReq.Messages) != 1 {
		t.Fatalf("anthropic messages = %+v", anthReq.Messages)
	}
	if anthReq.Messages[0].Role != core.RoleUser {
		t.Fatalf("anthropic role = %q, want user", anthReq.Messages[0].Role)
	}
	var content []map[string]interface{}
	if err := json.Unmarshal(anthReq.Messages[0].Content, &content); err != nil {
		t.Fatalf("unmarshal anthropic content: %v; raw=%s", err, string(anthReq.Messages[0].Content))
	}
	if len(content) != 1 || content[0]["type"] != "tool_result" || content[0]["tool_use_id"] != "call_1" {
		t.Fatalf("anthropic content = %#v, want tool_result for call_1", content)
	}
}

func TestOpenAIResponsesRequiredToolChoiceMapsToCompatibilityProviders(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model:      "gpt-5.4-nano",
		Input:      "use a tool",
		ToolChoice: "required",
		Tools: []map[string]interface{}{{
			"type":        "function",
			"name":        "lookup",
			"description": "Look up a value.",
			"parameters": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		}},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	anthReq, err := TypedToAnthropicRequest(typed, "claude-test")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if anthReq.ToolChoice == nil || anthReq.ToolChoice.Type != "any" {
		t.Fatalf("anthropic tool_choice = %+v, want type any", anthReq.ToolChoice)
	}

	googleReq, err := TypedToGoogleRequest(typed, "gemini-test", nil)
	if err != nil {
		t.Fatalf("TypedToGoogleRequest() error = %v", err)
	}
	if googleReq.ToolConfig == nil || googleReq.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Fatalf("google tool_config = %+v, want mode ANY", googleReq.ToolConfig)
	}
}

func TestOpenAIResponsesFunctionToolsConvert(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: "use a tool",
		Tools: []map[string]interface{}{{
			"type":        "function",
			"name":        "lookup",
			"description": "Look up a value.",
			"parameters": map[string]interface{}{
				"type": "object",
			},
		}},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	if len(typed.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(typed.Tools))
	}
	if typed.Tools[0].Name != "lookup" || typed.Tools[0].Description != "Look up a value." {
		t.Fatalf("tool = %#v", typed.Tools[0])
	}
}

func TestOpenAIResponsesFunctionToolStrictRendersToAnthropic(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "claude-opus-4-7",
		Input: "use a tool",
		Tools: []map[string]interface{}{{
			"type":        "function",
			"name":        "lookup",
			"description": "Look up a value.",
			"strict":      true,
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string"},
				},
				"required":             []interface{}{"query"},
				"additionalProperties": false,
			},
		}},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	anthReq, err := TypedToAnthropicRequest(typed, "claude-opus-4-7")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if len(anthReq.Tools) != 1 {
		t.Fatalf("anthropic tools len = %d, want 1", len(anthReq.Tools))
	}
	if anthReq.Tools[0].Strict == nil || *anthReq.Tools[0].Strict != true {
		t.Fatalf("anthropic tool strict = %#v, want true", anthReq.Tools[0].Strict)
	}
}

func TestOpenAIResponsesCustomToolsConvertToChatShape(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: "use a tool",
		Tools: []map[string]interface{}{
			{
				"type":        "custom",
				"name":        "apply_patch",
				"description": "Use apply_patch.",
				"format": map[string]interface{}{
					"type":       "grammar",
					"syntax":     "lark",
					"definition": "start: /.+/",
				},
			},
			{
				"type":        "function",
				"name":        "lookup",
				"description": "Look up a value.",
				"parameters":  map[string]interface{}{"type": "object"},
			},
			{"type": "file_search", "vector_store_ids": []interface{}{"vs_1"}},
		},
		ToolChoice: map[string]interface{}{"type": "custom", "name": "apply_patch"},
	}

	var typed TypedRequest
	var err error
	logs := captureWarnLogs(t, func() {
		typed, err = OpenAIResponsesRequestToTyped(context.Background(), req)
	})
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTyped() error = %v", err)
	}
	if len(typed.Tools) != 2 || typed.Tools[0].Type != "custom" || typed.Tools[0].Name != "apply_patch" || typed.Tools[1].Name != "lookup" {
		t.Fatalf("tools = %#v, want custom apply_patch and function lookup", typed.Tools)
	}
	if typed.ToolChoice == nil || typed.ToolChoice.Type != "tool" || typed.ToolChoice.Name != "apply_patch" {
		t.Fatalf("tool choice = %#v, want custom apply_patch choice", typed.ToolChoice)
	}
	if want := `Dropping unsupported Responses tool type "file_search" at index 2`; !strings.Contains(logs, want) {
		t.Fatalf("warning %q not found in logs:\n%s", want, logs)
	}
	if strings.Contains(logs, `tool_choice type "custom"`) {
		t.Fatalf("custom tool choice should not be dropped; logs:\n%s", logs)
	}

	chatReq, err := TypedToOpenAIRequest(typed, "gpt-public")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}
	if len(chatReq.Tools) != 2 || chatReq.Tools[0].Type != "custom" {
		t.Fatalf("chat tools = %#v", chatReq.Tools)
	}
	custom := openAICustomToolMap(chatReq.Tools[0].Custom)
	format := openAICustomToolMap(custom["format"])
	grammar := openAICustomToolMap(format["grammar"])
	if custom["name"] != "apply_patch" || format["type"] != "grammar" || grammar["syntax"] != "lark" || grammar["definition"] != "start: /.+/" {
		t.Fatalf("chat custom tool = %#v", chatReq.Tools[0].Custom)
	}
	choice, ok := chatReq.ToolChoice.(map[string]interface{})
	if !ok || choice["type"] != "custom" {
		t.Fatalf("chat tool_choice = %#v, want custom choice", chatReq.ToolChoice)
	}
	choiceCustom := openAICustomToolMap(choice["custom"])
	if choiceCustom["name"] != "apply_patch" {
		t.Fatalf("chat custom choice = %#v", chatReq.ToolChoice)
	}

	backReq, err := TypedToOpenAIResponsesRequest(typed, "gpt-public")
	if err != nil {
		t.Fatalf("TypedToOpenAIResponsesRequest() error = %v", err)
	}
	if len(backReq.Tools) != 2 || backReq.Tools[0]["type"] != "custom" {
		t.Fatalf("responses tools = %#v", backReq.Tools)
	}
	backFormat := openAICustomToolMap(backReq.Tools[0]["format"])
	if backFormat["type"] != "grammar" || backFormat["syntax"] != "lark" || backFormat["definition"] != "start: /.+/" {
		t.Fatalf("responses custom format = %#v", backReq.Tools[0]["format"])
	}

	anthReq, err := TypedToAnthropicRequest(typed, "claude-test")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if len(anthReq.Tools) != 2 || anthReq.Tools[0].Name != "apply_patch" {
		t.Fatalf("anthropic tools = %#v", anthReq.Tools)
	}
	schema, _ := anthReq.Tools[0].InputSchema.(map[string]interface{})
	properties, _ := schema["properties"].(map[string]interface{})
	if _, ok := properties[core.CustomToolInputField]; !ok {
		t.Fatalf("anthropic custom schema = %#v", anthReq.Tools[0].InputSchema)
	}
	if anthReq.ToolChoice == nil || anthReq.ToolChoice.Type != "tool" || anthReq.ToolChoice.Name != "apply_patch" {
		t.Fatalf("anthropic tool_choice = %#v", anthReq.ToolChoice)
	}
}

func TestOpenAIChatCustomToolsConvertToResponsesShape(t *testing.T) {
	chatReq := &OpenAIRequest{
		Model: "gpt-public",
		Messages: []OpenAIMessage{{
			Role:    core.RoleUser,
			Content: "use a tool",
		}},
		Tools: []OpenAITool{{
			Type: "custom",
			Custom: map[string]interface{}{
				"name":        "apply_patch",
				"description": "Use apply_patch.",
				"format": map[string]interface{}{
					"type": "grammar",
					"grammar": map[string]interface{}{
						"syntax":     "lark",
						"definition": "start: /.+/",
					},
				},
			},
		}},
		ToolChoice: map[string]interface{}{
			"type":   "custom",
			"custom": map[string]interface{}{"name": "apply_patch"},
		},
	}

	typed, err := OpenAIRequestToTypedStrict(chatReq)
	if err != nil {
		t.Fatalf("OpenAIRequestToTypedStrict() error = %v", err)
	}
	if len(typed.Tools) != 1 || typed.Tools[0].Type != "custom" || typed.Tools[0].Name != "apply_patch" {
		t.Fatalf("typed tools = %#v", typed.Tools)
	}
	if typed.ToolChoice == nil || typed.ToolChoice.Name != "apply_patch" {
		t.Fatalf("typed tool choice = %#v", typed.ToolChoice)
	}
	responsesReq, err := TypedToOpenAIResponsesRequest(typed, "gpt-public")
	if err != nil {
		t.Fatalf("TypedToOpenAIResponsesRequest() error = %v", err)
	}
	format := openAICustomToolMap(responsesReq.Tools[0]["format"])
	if format["type"] != "grammar" || format["syntax"] != "lark" || format["definition"] != "start: /.+/" {
		t.Fatalf("responses custom format = %#v", responsesReq.Tools[0]["format"])
	}
	if choice, ok := responsesReq.ToolChoice.(map[string]string); !ok || choice["type"] != "custom" || choice["name"] != "apply_patch" {
		t.Fatalf("responses tool_choice = %#v", responsesReq.ToolChoice)
	}
}

func TestOpenAIResponsesNamespaceToolsFlattenToChat(t *testing.T) {
	trueValue := true
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: "use a namespaced tool",
		Tools: []map[string]interface{}{{
			"type":        "namespace",
			"name":        "mcp__computer_use__",
			"description": "Computer Use tools.",
			"tools": []interface{}{
				map[string]interface{}{
					"type":        "function",
					"name":        "click",
					"description": "Click at a screen coordinate.",
					"parameters": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": false,
					},
					"strict": true,
				},
				map[string]interface{}{
					"type":        "custom",
					"name":        "apply_patch",
					"description": "Use apply_patch.",
					"format": map[string]interface{}{
						"type":       "grammar",
						"syntax":     "lark",
						"definition": "start: /.+/",
					},
				},
			},
		}},
		ToolChoice: map[string]interface{}{"type": "function", "namespace": "mcp__computer_use__", "name": "click"},
	}

	typed, err := OpenAIResponsesRequestToTypedStrict(req)
	if err != nil {
		t.Fatalf("OpenAIResponsesRequestToTypedStrict() error = %v", err)
	}
	if len(typed.Tools) != 2 {
		t.Fatalf("typed tools len = %d, want 2", len(typed.Tools))
	}
	if typed.Tools[0].Name != "mcp__computer_use__click" || typed.Tools[0].Namespace != "mcp__computer_use__" || typed.Tools[0].OriginalName != "click" {
		t.Fatalf("flattened function tool = %#v", typed.Tools[0])
	}
	if typed.Tools[0].Strict == nil || *typed.Tools[0].Strict != trueValue {
		t.Fatalf("flattened function strict = %#v", typed.Tools[0].Strict)
	}
	if typed.ToolChoice == nil || typed.ToolChoice.Name != "mcp__computer_use__click" {
		t.Fatalf("typed tool choice = %#v", typed.ToolChoice)
	}

	chatReq, err := TypedToOpenAIRequest(typed, "gpt-public")
	if err != nil {
		t.Fatalf("TypedToOpenAIRequest() error = %v", err)
	}
	if len(chatReq.Tools) != 2 {
		t.Fatalf("chat tools len = %d, want 2", len(chatReq.Tools))
	}
	if chatReq.Tools[0].Function.Name != "mcp__computer_use__click" || !strings.Contains(chatReq.Tools[0].Function.Description, "Original tool name: click.") {
		t.Fatalf("chat function tool = %#v", chatReq.Tools[0])
	}
	if chatReq.Tools[0].Function.Strict == nil || *chatReq.Tools[0].Function.Strict != trueValue {
		t.Fatalf("chat function strict = %#v", chatReq.Tools[0].Function.Strict)
	}
	custom := openAICustomToolMap(chatReq.Tools[1].Custom)
	if custom["name"] != "mcp__computer_use__apply_patch" {
		t.Fatalf("chat custom tool = %#v", chatReq.Tools[1].Custom)
	}
	format := openAICustomToolMap(custom["format"])
	grammar := openAICustomToolMap(format["grammar"])
	if grammar["syntax"] != "lark" || grammar["definition"] != "start: /.+/" {
		t.Fatalf("chat custom grammar = %#v", custom["format"])
	}

	backReq, err := TypedToOpenAIResponsesRequest(typed, "gpt-public")
	if err != nil {
		t.Fatalf("TypedToOpenAIResponsesRequest() error = %v", err)
	}
	if len(backReq.Tools) != 1 || backReq.Tools[0]["type"] != "namespace" {
		t.Fatalf("responses tools = %#v, want namespace", backReq.Tools)
	}
	nested, _ := backReq.Tools[0]["tools"].([]map[string]interface{})
	if len(nested) != 2 || nested[0]["name"] != "click" || nested[1]["name"] != "apply_patch" {
		t.Fatalf("responses namespace nested tools = %#v", backReq.Tools[0]["tools"])
	}
	choice, _ := backReq.ToolChoice.(map[string]string)
	if choice["type"] != "function" || choice["namespace"] != "mcp__computer_use__" || choice["name"] != "click" {
		t.Fatalf("responses tool choice = %#v", backReq.ToolChoice)
	}
}

func TestOpenAIChatNamespacedToolCallsRestoreResponsesNamespace(t *testing.T) {
	typed := TypedRequest{
		Tools: []core.ToolDefinition{
			{Type: "function", Namespace: "mcp__computer_use__", OriginalName: "click", Name: "mcp__computer_use__click"},
			{Type: "custom", Namespace: "mcp__computer_use__", OriginalName: "apply_patch", Name: "mcp__computer_use__apply_patch"},
		},
	}
	chatResp := &OpenAIResponse{
		ID:    "chatcmpl_1",
		Model: "gpt-public",
		Choices: []OpenAIChoice{{
			Message: OpenAIMessage{
				Role: core.RoleAssistant,
				ToolCalls: []ToolCall{
					{
						ID:   "call_click",
						Type: "function",
						Function: FunctionCall{
							Name:      "mcp__computer_use__click",
							Arguments: `{"x":100,"y":200}`,
						},
					},
					{
						ID:   "call_patch",
						Type: "custom",
						Custom: &CustomToolCall{
							Name:  "mcp__computer_use__apply_patch",
							Input: "*** Begin Patch\n*** End Patch\n",
						},
					},
				},
			},
			FinishReason: "tool_calls",
		}},
	}

	converter := NewConverter(nil)
	anth := converter.ConvertOpenAIToAnthropicWithToolNameRegistry(chatResp, "gpt-public", responseToolNameRegistryFromCoreTools(typed.Tools))
	responses := converter.ConvertAnthropicResponseToOpenAIResponses(anth, "gpt-public")
	if len(responses.Output) != 2 {
		t.Fatalf("responses output len = %d, want 2: %+v", len(responses.Output), responses.Output)
	}
	if responses.Output[0].Type != "function_call" || responses.Output[0].Namespace != "mcp__computer_use__" || responses.Output[0].Name != "click" {
		t.Fatalf("function output = %+v", responses.Output[0])
	}
	if responses.Output[1].Type != "custom_tool_call" || responses.Output[1].Namespace != "mcp__computer_use__" || responses.Output[1].Name != "apply_patch" {
		t.Fatalf("custom output = %+v", responses.Output[1])
	}

	chatRoundTrip := converter.ConvertOpenAIResponsesToOpenAI(responses, "gpt-public")
	calls := chatRoundTrip.Choices[0].Message.ToolCalls
	if calls[0].Function.Name != "mcp__computer_use__click" || calls[1].Custom.Name != "mcp__computer_use__apply_patch" {
		t.Fatalf("roundtrip chat calls = %+v", calls)
	}
}

func TestAnthropicCustomToolWrapperConvertsToOpenAIResponsesCustomCall(t *testing.T) {
	converter := NewConverter(nil)
	registry := responseToolNameRegistryFromCoreTools([]core.ToolDefinition{{
		Type: "custom",
		Name: "apply_patch",
	}})
	anth := &AnthropicResponse{
		ID:         "msg_1",
		Type:       "message",
		Role:       core.RoleAssistant,
		Model:      "claude-test",
		StopReason: "tool_use",
		Content: []AnthropicContentBlock{{
			Type:  "tool_use",
			ID:    "toolu_1",
			Name:  "apply_patch",
			Input: map[string]interface{}{core.CustomToolInputField: "*** Begin Patch\n*** End Patch\n"},
		}},
	}

	resp := converter.ConvertAnthropicResponseToOpenAIResponsesWithToolNameRegistry(anth, "gpt-public", registry)
	if len(resp.Output) != 1 || resp.Output[0].Type != "custom_tool_call" {
		t.Fatalf("responses output = %+v, want custom_tool_call", resp.Output)
	}
	if resp.Output[0].Name != "apply_patch" || resp.Output[0].Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("custom call = %+v", resp.Output[0])
	}

	blocks := AnthropicBlocksToCoreWithToolNameRegistry(anth.Content, registry)
	toolUse, ok := blocks[0].(core.ToolUseBlock)
	if !ok || toolUse.Type != "custom" || toolUse.InputString != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("core blocks = %#v", blocks)
	}
}

func TestAnthropicNamespacedCustomToolWrapperRestoresResponsesNamespace(t *testing.T) {
	converter := NewConverter(nil)
	registry := responseToolNameRegistryFromCoreTools([]core.ToolDefinition{{
		Type:         "custom",
		Namespace:    "mcp__computer_use__",
		OriginalName: "apply_patch",
		Name:         "mcp__computer_use__apply_patch",
	}})
	anth := &AnthropicResponse{
		ID:         "msg_1",
		Type:       "message",
		Role:       core.RoleAssistant,
		Model:      "claude-test",
		StopReason: "tool_use",
		Content: []AnthropicContentBlock{{
			Type:  "tool_use",
			ID:    "toolu_1",
			Name:  "mcp__computer_use__apply_patch",
			Input: map[string]interface{}{core.CustomToolInputField: "raw patch"},
		}},
	}

	resp := converter.ConvertAnthropicResponseToOpenAIResponsesWithToolNameRegistry(anth, "gpt-public", registry)
	if len(resp.Output) != 1 {
		t.Fatalf("responses output = %+v", resp.Output)
	}
	item := resp.Output[0]
	if item.Type != "custom_tool_call" || item.Namespace != "mcp__computer_use__" || item.Name != "apply_patch" || item.Input != "raw patch" {
		t.Fatalf("custom output = %+v", item)
	}
}

func TestOpenAIResponsesRejectsMalformedFunctionTool(t *testing.T) {
	_, err := OpenAIResponsesRequestToTypedStrict(&OpenAIResponsesRequest{
		Model: "gpt-5.4-nano",
		Input: "use a tool",
		Tools: []map[string]interface{}{{
			"type":       "function",
			"parameters": map[string]interface{}{"type": "object"},
		}},
	})
	if err == nil {
		t.Fatal("expected conversion error")
	}
	if err.Error() != "responses function tool at index 0 is missing name" {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestOpenAIResponsesPromptRejectedByConvertedProviderConversion(t *testing.T) {
	_, err := OpenAIResponsesRequestToTypedStrict(&OpenAIResponsesRequest{
		Model:  "gpt-5.4-nano",
		Input:  "hi",
		Prompt: map[string]interface{}{"id": "pmpt_123"},
	})
	if err == nil {
		t.Fatal("expected conversion error")
	}
	if err.Error() != "prompt is only supported for direct OpenAI Responses passthrough" {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestConvertAnthropicResponseToOpenAIResponsesPreservesMaxTokenTruncation(t *testing.T) {
	converter := NewConverter(nil)
	resp := converter.ConvertAnthropicResponseToOpenAIResponses(&AnthropicResponse{
		ID:         "msg_backend",
		Type:       "message",
		Role:       core.RoleAssistant,
		Model:      "claude-test",
		StopReason: "max_tokens",
		Content: []AnthropicContentBlock{{
			Type: "text",
			Text: "partial",
		}},
	}, "claude-public")

	if resp.Status != "incomplete" {
		t.Fatalf("status = %q, want incomplete", resp.Status)
	}
	details, ok := resp.IncompleteDetails.(map[string]interface{})
	if !ok || details["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete details = %#v, want max_output_tokens reason", resp.IncompleteDetails)
	}
	if len(resp.Output) != 1 || resp.Output[0].Status != "incomplete" {
		t.Fatalf("output = %+v, want incomplete message item", resp.Output)
	}
}

func TestConvertOpenAIResponsesToOpenAIChat(t *testing.T) {
	converter := NewConverter(nil)
	resp := &OpenAIResponsesResponse{
		ID:     "resp_1",
		Status: "completed",
		Model:  "gpt-5.4-nano",
		Output: []OpenAIResponsesOutputItem{
			{
				Type: "message",
				Content: []OpenAIResponsesContentPart{{
					Type: "output_text",
					Text: "Checking.",
				}},
			},
			{
				ID:        "fc_1",
				Type:      "function_call",
				CallID:    "call_1",
				Name:      "lookup",
				Arguments: `{"city":"Chicago"}`,
			},
		},
	}

	got := converter.ConvertOpenAIResponsesToOpenAI(resp, "gpt-public")
	if got.Model != "gpt-public" {
		t.Fatalf("model = %q", got.Model)
	}
	if len(got.Choices) != 1 || got.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("choices = %+v", got.Choices)
	}
	if got.Choices[0].Message.Content != "Checking." {
		t.Fatalf("content = %#v", got.Choices[0].Message.Content)
	}
	if len(got.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", got.Choices[0].Message.ToolCalls)
	}
}

func TestOpenAIResponsesCustomToolCallsConvertToChat(t *testing.T) {
	converter := NewConverter(nil)
	resp := &OpenAIResponsesResponse{
		ID:     "resp_1",
		Status: "completed",
		Model:  "gpt-5.4-nano",
		Output: []OpenAIResponsesOutputItem{{
			ID:     "ctc_1",
			Type:   "custom_tool_call",
			CallID: "call_1",
			Name:   "apply_patch",
			Input:  "*** Begin Patch\n*** End Patch\n",
		}},
	}

	chat := converter.ConvertOpenAIResponsesToOpenAI(resp, "gpt-public")
	calls := chat.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Type != "custom" || calls[0].Custom == nil {
		t.Fatalf("chat tool calls = %+v, want custom tool call", calls)
	}
	if calls[0].Custom.Name != "apply_patch" || calls[0].Custom.Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("chat custom call = %+v", calls[0].Custom)
	}

	anth := converter.ConvertOpenAIToAnthropic(chat, "gpt-public")
	roundTrip := converter.ConvertAnthropicResponseToOpenAIResponses(anth, "gpt-public")
	if len(roundTrip.Output) != 1 || roundTrip.Output[0].Type != "custom_tool_call" {
		t.Fatalf("roundtrip responses output = %+v", roundTrip.Output)
	}
	if roundTrip.Output[0].Name != "apply_patch" || roundTrip.Output[0].Input != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("roundtrip custom call = %+v", roundTrip.Output[0])
	}
}

func TestOpenAIResponsesResponseMarshalOmitsOutputText(t *testing.T) {
	resp := OpenAIResponsesResponse{
		ID:         "resp_1",
		Object:     "response",
		Status:     "completed",
		Model:      "gpt-test",
		OutputText: "Checking.",
		Output: []OpenAIResponsesOutputItem{{
			Type: "message",
			Role: core.RoleAssistant,
			Content: []OpenAIResponsesContentPart{{
				Type: "output_text",
				Text: "Checking.",
			}},
		}},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := got["output_text"]; ok {
		t.Fatalf("marshaled response unexpectedly contains output_text: %s", string(data))
	}
	text, ok := lookupStatefulJSONPath(got, "output.0.content.0.text")
	if !ok || text != "Checking." {
		t.Fatalf("nested output text = %#v, want Checking. in %s", text, string(data))
	}
}

func TestRewriteResponsesBodyModelPreservesUnknownFields(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"gpt-5.4-nano-2026-03-17","background":false,"text":{"format":{"type":"text"},"verbosity":"medium"},"tools":[{"type":"function","name":"lookup","strict":true}],"billing":{"payer":"developer"}}`)

	rewritten := rewriteResponsesBodyModel(body, "gpt-public")

	var got map[string]interface{}
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("rewriteResponsesBodyModel returned invalid JSON: %v", err)
	}
	if got["model"] != "gpt-public" {
		t.Fatalf("model = %v", got["model"])
	}
	if _, ok := got["background"]; !ok {
		t.Fatalf("background field was not preserved: %+v", got)
	}
	if _, ok := got["billing"]; !ok {
		t.Fatalf("billing field was not preserved: %+v", got)
	}
	tools, ok := got["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %+v", got["tools"])
	}
	tool, ok := tools[0].(map[string]interface{})
	if !ok || tool["strict"] != true {
		t.Fatalf("tool strict flag was not preserved: %+v", tools[0])
	}
	text, ok := got["text"].(map[string]interface{})
	if !ok || text["verbosity"] != "medium" {
		t.Fatalf("text metadata was not preserved: %+v", got["text"])
	}
}
