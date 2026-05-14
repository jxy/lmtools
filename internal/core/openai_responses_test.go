package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildOpenAIResponsesRequest(t *testing.T) {
	cfg := RequestOptions{
		Provider:        "openai",
		ProviderURL:     "https://example.test/v1",
		Model:           "gpt-5.4-nano",
		System:          "Use the CLI system prompt.",
		OpenAIResponses: true,
		JSONMode:        true,
		Effort:          "low",
	}

	req, body, err := BuildRequest(cfg, "Return JSON.")
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	if got := req.URL.String(); got != "https://example.test/v1/responses" {
		t.Fatalf("URL = %q, want responses endpoint", got)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("request body is invalid JSON: %v", err)
	}
	if decoded["model"] != "gpt-5.4-nano" {
		t.Fatalf("model = %v", decoded["model"])
	}
	if _, ok := decoded["messages"]; ok {
		t.Fatalf("Responses request should not contain chat messages: %s", body)
	}
	if _, ok := decoded["input"]; !ok {
		t.Fatalf("Responses request missing input: %s", body)
	}
	if decoded["instructions"] != "Use the CLI system prompt." {
		t.Fatalf("instructions = %v, want system prompt", decoded["instructions"])
	}
	input, ok := decoded["input"].([]interface{})
	if !ok {
		t.Fatalf("input = %T, want array", decoded["input"])
	}
	for _, item := range input {
		itemMap, ok := item.(map[string]interface{})
		if ok && itemMap["role"] == "system" {
			t.Fatalf("Responses input should not contain system message: %s", body)
		}
	}
	if text, ok := decoded["text"].(map[string]interface{}); !ok || text["format"] == nil {
		t.Fatalf("Responses request missing text.format: %s", body)
	}
}

func TestBuildOpenAIResponsesRequestPreservesCustomTools(t *testing.T) {
	cfg := RequestOptions{
		Provider:        "openai",
		ProviderURL:     "https://example.test/v1",
		Model:           "gpt-5.4-nano",
		OpenAIResponses: true,
	}
	toolDefs := []ToolDefinition{{
		Type:        "custom",
		Name:        "apply_patch",
		Description: "Apply a patch.",
		Format: map[string]interface{}{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": "start: /.+/",
		},
	}}
	messages := []TypedMessage{NewTextMessage(string(RoleUser), "patch")}

	_, body, err := BuildChatRequest(cfg, messages, ChatBuildOptions{
		ModelOverride: "gpt-5.4-nano",
		ToolDefs:      toolDefs,
	})
	if err != nil {
		t.Fatalf("BuildChatRequest() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("request body is invalid JSON: %v", err)
	}
	tools, _ := decoded["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want one custom tool", decoded["tools"])
	}
	tool, _ := tools[0].(map[string]interface{})
	format, _ := tool["format"].(map[string]interface{})
	if tool["type"] != "custom" || tool["name"] != "apply_patch" || format["syntax"] != "lark" || format["definition"] != "start: /.+/" {
		t.Fatalf("custom tool = %#v", tool)
	}
}

func TestBuildOpenAIResponsesRequestPreservesFunctionToolStrict(t *testing.T) {
	cfg := RequestOptions{
		Provider:        "openai",
		ProviderURL:     "https://example.test/v1",
		Model:           "gpt-5.4-nano",
		OpenAIResponses: true,
	}
	strictTrue := true
	strictFalse := false
	tests := []struct {
		name        string
		strict      *bool
		wantPresent bool
		wantValue   bool
	}{
		{name: "true", strict: &strictTrue, wantPresent: true, wantValue: true},
		{name: "false", strict: &strictFalse, wantPresent: true, wantValue: false},
		{name: "nil", strict: nil, wantPresent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolDefs := []ToolDefinition{{
				Name:        "lookup_city",
				Description: "Look up structured information for a city.",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"city": map[string]interface{}{"type": "string"},
					},
					"required":             []string{"city"},
					"additionalProperties": false,
				},
				Strict: tt.strict,
			}}
			messages := []TypedMessage{NewTextMessage(string(RoleUser), "city")}

			_, body, err := BuildChatRequest(cfg, messages, ChatBuildOptions{
				ModelOverride: "gpt-5.4-nano",
				ToolDefs:      toolDefs,
			})
			if err != nil {
				t.Fatalf("BuildChatRequest() error = %v", err)
			}

			var decoded map[string]interface{}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("request body is invalid JSON: %v", err)
			}
			tools, _ := decoded["tools"].([]interface{})
			if len(tools) != 1 {
				t.Fatalf("tools = %#v, want one function tool", decoded["tools"])
			}
			tool, _ := tools[0].(map[string]interface{})
			got, present := tool["strict"]
			if present != tt.wantPresent {
				t.Fatalf("tool strict present = %v, want %v; tool = %#v", present, tt.wantPresent, tool)
			}
			if !present {
				return
			}
			gotBool, ok := got.(bool)
			if !ok || gotBool != tt.wantValue {
				t.Fatalf("tool strict = %#v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestMarshalOpenAIResponsesInputPreservesCustomToolCalls(t *testing.T) {
	input := marshalOpenAIResponsesInput([]TypedMessage{
		{
			Role: string(RoleAssistant),
			Blocks: []Block{ToolUseBlock{
				ID:          "call_1",
				Type:        "custom",
				Name:        "apply_patch",
				InputString: "*** Begin Patch\n*** End Patch\n",
			}},
		},
		{
			Role: string(RoleUser),
			Blocks: []Block{ToolResultBlock{
				ToolUseID: "call_1",
				Type:      "custom",
				Content:   "ok",
			}},
		},
	})
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2: %#v", len(input), input)
	}
	call, _ := input[0].(map[string]interface{})
	if call["type"] != "custom_tool_call" || call["input"] != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("custom call item = %#v", call)
	}
	output, _ := input[1].(map[string]interface{})
	if output["type"] != "custom_tool_call_output" || output["output"] != "ok" {
		t.Fatalf("custom output item = %#v", output)
	}
}

func TestBuildOpenAIResponsesRequestSystemPrecedence(t *testing.T) {
	tests := []struct {
		name                string
		cfgSystem           string
		effectiveSystem     string
		systemExplicitlySet bool
		wantInstructions    string
		wantInstructionsKey bool
	}{
		{
			name:                "session system wins over non-explicit default",
			cfgSystem:           "Default CLI system prompt.",
			effectiveSystem:     "Default CLI system prompt.",
			systemExplicitlySet: false,
			wantInstructions:    "Session system prompt.",
			wantInstructionsKey: true,
		},
		{
			name:                "explicit cli system wins over session system",
			cfgSystem:           "Explicit CLI system prompt.",
			effectiveSystem:     "Explicit CLI system prompt.",
			systemExplicitlySet: true,
			wantInstructions:    "Explicit CLI system prompt.",
			wantInstructionsKey: true,
		},
		{
			name:                "explicit empty cli system removes session system",
			cfgSystem:           "",
			effectiveSystem:     "",
			systemExplicitlySet: true,
			wantInstructionsKey: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RequestOptions{
				Provider:            "openai",
				ProviderURL:         "https://example.test/v1",
				Model:               "gpt-5.4-nano",
				System:              tt.cfgSystem,
				EffectiveSystem:     tt.effectiveSystem,
				SystemExplicitlySet: tt.systemExplicitlySet,
				OpenAIResponses:     true,
			}
			messages := []TypedMessage{
				NewTextMessage(string(RoleSystem), "Session system prompt."),
				NewTextMessage(string(RoleUser), "Continue."),
			}

			_, body, err := BuildChatRequest(cfg, messages, ChatBuildOptions{})
			if err != nil {
				t.Fatalf("BuildChatRequest() error = %v", err)
			}

			var decoded map[string]interface{}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("request body is invalid JSON: %v", err)
			}
			gotInstructions, hasInstructions := decoded["instructions"]
			if hasInstructions != tt.wantInstructionsKey {
				t.Fatalf("instructions presence = %v, want %v; body = %s", hasInstructions, tt.wantInstructionsKey, body)
			}
			if tt.wantInstructionsKey && gotInstructions != tt.wantInstructions {
				t.Fatalf("instructions = %v, want %q", gotInstructions, tt.wantInstructions)
			}
			assertResponsesInputHasNoSystemMessage(t, decoded, body)
		})
	}
}

func assertResponsesInputHasNoSystemMessage(t *testing.T, decoded map[string]interface{}, body []byte) {
	t.Helper()
	input, ok := decoded["input"].([]interface{})
	if !ok {
		t.Fatalf("input = %T, want array", decoded["input"])
	}
	for _, item := range input {
		itemMap, ok := item.(map[string]interface{})
		if ok && itemMap["role"] == "system" {
			t.Fatalf("Responses input should not contain system message: %s", body)
		}
	}
}

func TestParseOpenAIResponsesWithTools(t *testing.T) {
	data := []byte(`{
		"output": [
			{"type":"message","content":[{"type":"output_text","text":"Checking."}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"city\":\"Chicago\"}"}
		]
	}`)
	text, calls, err := parseOpenAIResponsesWithTools(data)
	if err != nil {
		t.Fatalf("parseOpenAIResponsesWithTools() error = %v", err)
	}
	if text != "Checking." {
		t.Fatalf("text = %q", text)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if got := strings.TrimSpace(string(calls[0].Args)); got != `{"city":"Chicago"}` {
		t.Fatalf("args = %s", got)
	}
}

func TestParseOpenAIResponsesRefusalContent(t *testing.T) {
	data := []byte(`{
		"output": [
			{"type":"message","content":[{"type":"refusal","refusal":"I cannot help with that."}]}
		]
	}`)
	resp, err := parseOpenAIResponses(data, false)
	if err != nil {
		t.Fatalf("parseOpenAIResponses() error = %v", err)
	}
	if resp.Text != "I cannot help with that." {
		t.Fatalf("text = %q, want refusal text", resp.Text)
	}
	if len(resp.Blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(resp.Blocks))
	}
	text, ok := resp.Blocks[0].(TextBlock)
	if !ok {
		t.Fatalf("block type = %T, want TextBlock", resp.Blocks[0])
	}
	if text.Text != "I cannot help with that." {
		t.Fatalf("block text = %q, want refusal text", text.Text)
	}
}

func TestMarshalOpenAIResponsesInputPreservesItemOrder(t *testing.T) {
	input := marshalOpenAIResponsesInput([]TypedMessage{{
		Role: string(RoleAssistant),
		Blocks: []Block{
			ReasoningBlock{
				Provider:         "openai",
				Type:             "reasoning",
				ID:               "rs_1",
				EncryptedContent: "enc_1",
			},
			ToolUseBlock{
				ID:    "call_1",
				Name:  "lookup",
				Input: json.RawMessage(`{"q":"x"}`),
			},
			TextBlock{Text: "done"},
		},
	}})
	if len(input) != 3 {
		t.Fatalf("input length = %d, want 3: %#v", len(input), input)
	}
	for i, wantType := range []string{"reasoning", "function_call", "message"} {
		item, ok := input[i].(map[string]interface{})
		if !ok {
			t.Fatalf("input[%d] type = %T", i, input[i])
		}
		if got := item["type"]; got != wantType {
			t.Fatalf("input[%d].type = %v, want %s; input=%#v", i, got, wantType, input)
		}
	}
}

func TestOpenAIResponsesStreamState(t *testing.T) {
	state := NewOpenAIResponsesStreamState()

	text, calls, done, err := state.ParseLine(`data: {"type":"response.output_text.delta","delta":"Hi"}`)
	if err != nil {
		t.Fatalf("ParseLine(text) error = %v", err)
	}
	if text != "Hi" || len(calls) != 0 || done {
		t.Fatalf("text event = text %q calls %+v done %v", text, calls, done)
	}

	_, _, _, err = state.ParseLine(`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"lookup"}}`)
	if err != nil {
		t.Fatalf("ParseLine(item added) error = %v", err)
	}
	_, _, _, err = state.ParseLine(`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"city\""}`)
	if err != nil {
		t.Fatalf("ParseLine(args delta 1) error = %v", err)
	}
	_, _, _, err = state.ParseLine(`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":":\"Chicago\"}"}`)
	if err != nil {
		t.Fatalf("ParseLine(args delta 2) error = %v", err)
	}
	_, calls, done, err = state.ParseLine(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"city\":\"Chicago\"}"}}`)
	if err != nil {
		t.Fatalf("ParseLine(item done) error = %v", err)
	}
	if done || len(calls) != 1 {
		t.Fatalf("item done calls = %+v done = %v", calls, done)
	}
	if got := string(calls[0].Args); got != `{"city":"Chicago"}` {
		t.Fatalf("stream args = %s", got)
	}
}

func TestOpenAIResponsesStreamStateFailedEventIsFatal(t *testing.T) {
	state := NewOpenAIResponsesStreamState()

	_, _, _, err := state.ParseLine(`data: {"type":"response.failed","response":{"error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}}`)
	if err == nil {
		t.Fatal("ParseLine(response.failed) error = nil, want fatal provider error")
	}
	if !isFatalStreamError(err) {
		t.Fatalf("ParseLine(response.failed) error is not fatal: %v", err)
	}
	for _, want := range []string{"quota exceeded", "insufficient_quota", "billing_hard_limit_reached"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ParseLine(response.failed) error = %q, want %q", err.Error(), want)
		}
	}
}

func TestOpenAIResponsesStreamStateTopLevelErrorEventIsFatal(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "typed error",
			line: `data: {"type":"error","error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}`,
			want: []string{"quota exceeded", "insufficient_quota", "billing_hard_limit_reached"},
		},
		{
			name: "error envelope without type",
			line: `data: {"error":{"message":"quota exceeded","type":"insufficient_quota","code":"billing_hard_limit_reached"}}`,
			want: []string{"quota exceeded", "insufficient_quota", "billing_hard_limit_reached"},
		},
		{
			name: "typed error without payload",
			line: `data: {"type":"error"}`,
			want: []string{"OpenAI responses stream error"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := NewOpenAIResponsesStreamState()
			_, _, _, err := state.ParseLine(tt.line)
			if err == nil {
				t.Fatal("ParseLine(error) error = nil, want fatal provider error")
			}
			if !isFatalStreamError(err) {
				t.Fatalf("ParseLine(error) error is not fatal: %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("ParseLine(error) error = %q, want %q", err.Error(), want)
				}
			}
		})
	}
}

func TestOpenAIResponsesStreamStateIncompleteFinalizesBufferedToolCall(t *testing.T) {
	state := NewOpenAIResponsesStreamState()

	lines := []string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"city\""}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":":\"Chicago\"}"}`,
	}
	for _, line := range lines {
		if _, _, _, err := state.ParseLine(line); err != nil {
			t.Fatalf("ParseLine(%s) error = %v", line, err)
		}
	}

	_, calls, done, err := state.ParseLine(`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)
	if err != nil {
		t.Fatalf("ParseLine(response.incomplete) error = %v", err)
	}
	if !done {
		t.Fatal("ParseLine(response.incomplete) done = false, want true")
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v, want one finalized call", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "lookup" {
		t.Fatalf("tool call = %+v, want call_1 lookup", calls[0])
	}
	if got := string(calls[0].Args); got != `{"city":"Chicago"}` {
		t.Fatalf("tool call args = %s, want Chicago JSON", got)
	}
}

func TestOpenAIResponsesStreamStatePreservesReasoningBlocks(t *testing.T) {
	state := NewOpenAIResponsesStreamState()

	lines := []string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","summary":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"Looked up the city."}],"encrypted_content":"enc_1"}}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
		`data: {"type":"response.output_item.done","output_index":2,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{\"city\":\"Chicago\"}"}}`,
	}
	for _, line := range lines {
		if _, _, _, err := state.ParseLine(line); err != nil {
			t.Fatalf("ParseLine(%s) error = %v", line, err)
		}
	}

	blocks := state.Blocks()
	if len(blocks) != 3 {
		t.Fatalf("len(blocks) = %d, want 3: %#v", len(blocks), blocks)
	}
	reasoning, ok := blocks[0].(ReasoningBlock)
	if !ok {
		t.Fatalf("blocks[0] = %T, want ReasoningBlock", blocks[0])
	}
	if reasoning.Provider != "openai" || reasoning.ID != "rs_1" || reasoning.EncryptedContent != "enc_1" {
		t.Fatalf("reasoning block = %#v", reasoning)
	}
	if !strings.Contains(string(reasoning.Summary), "Looked up the city.") {
		t.Fatalf("reasoning summary = %s", reasoning.Summary)
	}
	text, ok := blocks[1].(TextBlock)
	if !ok || text.Text != "Done." {
		t.Fatalf("blocks[1] = %#v, want text Done.", blocks[1])
	}
	tool, ok := blocks[2].(ToolUseBlock)
	if !ok || tool.ID != "call_1" || tool.Name != "lookup" || string(tool.Input) != `{"city":"Chicago"}` {
		t.Fatalf("blocks[2] = %#v, want lookup tool call", blocks[2])
	}
}
