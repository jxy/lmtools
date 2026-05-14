package core

import "testing"

func TestPrepareRequestPayloadAnthropicStripsInlineSystemAndConvertsTools(t *testing.T) {
	toolChoice := &ToolChoice{Type: "tool", Name: "lookup_weather"}
	payload, err := PrepareRequestPayload(
		"anthropic",
		"claude-opus-4-1-20250805",
		[]TypedMessage{
			{
				Role: string(RoleSystem),
				Blocks: []Block{
					TextBlock{Text: "inline system"},
				},
			},
			{
				Role: string(RoleUser),
				Blocks: []Block{
					TextBlock{Text: "hello"},
				},
			},
		},
		"explicit system",
		[]ToolDefinition{
			{
				Name:        "lookup_weather",
				Description: "Get the weather",
				InputSchema: map[string]interface{}{
					"type": "object",
				},
			},
		},
		toolChoice,
		true,
	)
	if err != nil {
		t.Fatalf("PrepareRequestPayload() error = %v", err)
	}

	if got := payload.System; got != "explicit system" {
		t.Fatalf("System = %q, want %q", got, "explicit system")
	}
	if got := len(payload.Messages); got != 1 {
		t.Fatalf("len(Messages) = %d, want 1", got)
	}

	tools, ok := payload.Tools.([]AnthropicTool)
	if !ok {
		t.Fatalf("Tools type = %T, want []AnthropicTool", payload.Tools)
	}
	if len(tools) != 1 || tools[0].Name != "lookup_weather" {
		t.Fatalf("Tools = %+v, want lookup_weather", tools)
	}

	choice, ok := payload.ToolChoice.(AnthropicToolChoice)
	if !ok {
		t.Fatalf("ToolChoice type = %T, want AnthropicToolChoice", payload.ToolChoice)
	}
	if choice.Type != "tool" || choice.Name != "lookup_weather" {
		t.Fatalf("ToolChoice = %+v, want tool lookup_weather", choice)
	}
}

func TestPrepareRequestPayloadAnthropicConvertsCustomToolChoice(t *testing.T) {
	payload, err := PrepareRequestPayload(
		"anthropic",
		"claude-opus-4-1-20250805",
		[]TypedMessage{NewTextMessage(string(RoleUser), "patch")},
		"",
		[]ToolDefinition{{
			Type:        "custom",
			Name:        "apply_patch",
			Description: "Apply a patch.",
		}},
		&ToolChoice{Type: "tool", Name: "apply_patch"},
		false,
	)
	if err != nil {
		t.Fatalf("PrepareRequestPayload() error = %v", err)
	}

	tools, ok := payload.Tools.([]AnthropicTool)
	if !ok || len(tools) != 1 {
		t.Fatalf("Tools = %T len=%d, want one Anthropic tool", payload.Tools, len(tools))
	}
	if tools[0].Name != "apply_patch" || len(tools[0].InputSchema) == 0 {
		t.Fatalf("Tools = %+v, want wrapped custom tool", tools)
	}
	choice, ok := payload.ToolChoice.(AnthropicToolChoice)
	if !ok || choice.Type != "tool" || choice.Name != "apply_patch" {
		t.Fatalf("ToolChoice = %#v, want forced apply_patch tool", payload.ToolChoice)
	}
}

func TestPrepareRequestPayloadAnthropicDropsMissingToolChoice(t *testing.T) {
	payload, err := PrepareRequestPayload(
		"anthropic",
		"claude-opus-4-1-20250805",
		[]TypedMessage{NewTextMessage(string(RoleUser), "lookup")},
		"",
		[]ToolDefinition{{
			Type:        "function",
			Name:        "lookup",
			Description: "Lookup.",
			InputSchema: map[string]interface{}{"type": "object"},
		}},
		&ToolChoice{Type: "tool", Name: "missing"},
		false,
	)
	if err != nil {
		t.Fatalf("PrepareRequestPayload() error = %v", err)
	}
	if payload.ToolChoice != nil {
		t.Fatalf("ToolChoice = %#v, want nil for missing converted tool", payload.ToolChoice)
	}
}

func TestPrepareRequestPayloadOutOfBandPrefersInlineSystemWhenConfigNotExplicit(t *testing.T) {
	payload, err := PrepareRequestPayloadWithSystemExplicit(
		"anthropic",
		"claude-opus-4-1-20250805",
		[]TypedMessage{
			NewTextMessage(string(RoleSystem), "session system"),
			NewTextMessage(string(RoleUser), "hello"),
		},
		"default system",
		false,
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("PrepareRequestPayloadWithSystemExplicit() error = %v", err)
	}
	if got := payload.System; got != "session system" {
		t.Fatalf("System = %q, want session system", got)
	}
	if got := len(payload.Messages); got != 1 {
		t.Fatalf("len(Messages) = %d, want 1", got)
	}
	if got := payload.Messages[0].Role; got != string(RoleUser) {
		t.Fatalf("remaining role = %q, want user", got)
	}
}

func TestPrepareRequestPayloadOutOfBandExplicitEmptySystemRemovesInlineSystem(t *testing.T) {
	payload, err := PrepareRequestPayloadWithSystemExplicit(
		"anthropic",
		"claude-opus-4-1-20250805",
		[]TypedMessage{
			NewTextMessage(string(RoleSystem), "session system"),
			NewTextMessage(string(RoleUser), "hello"),
		},
		"",
		true,
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("PrepareRequestPayloadWithSystemExplicit() error = %v", err)
	}
	if payload.System != "" {
		t.Fatalf("System = %q, want empty", payload.System)
	}
	if got := len(payload.Messages); got != 1 {
		t.Fatalf("len(Messages) = %d, want 1", got)
	}
	if got := payload.Messages[0].Role; got != string(RoleUser) {
		t.Fatalf("remaining role = %q, want user", got)
	}
}

func TestPrependSystemMessage(t *testing.T) {
	messages := []TypedMessage{
		{
			Role: string(RoleUser),
			Blocks: []Block{
				TextBlock{Text: "hello"},
			},
		},
	}

	withSystem := PrependSystemMessage(messages, "be concise")
	if len(withSystem) != 2 {
		t.Fatalf("len(PrependSystemMessage()) = %d, want 2", len(withSystem))
	}
	if withSystem[0].Role != string(RoleSystem) {
		t.Fatalf("first role = %q, want %q", withSystem[0].Role, RoleSystem)
	}

	block, ok := withSystem[0].Blocks[0].(TextBlock)
	if !ok {
		t.Fatalf("first block type = %T, want TextBlock", withSystem[0].Blocks[0])
	}
	if block.Text != "be concise" {
		t.Fatalf("first block text = %q, want %q", block.Text, "be concise")
	}
}

func TestPrependSystemMessageKeepsLeadingSystem(t *testing.T) {
	original := []TypedMessage{
		{
			Role: string(RoleSystem),
			Blocks: []Block{
				TextBlock{Text: "already here"},
			},
		},
		{
			Role: string(RoleUser),
			Blocks: []Block{
				TextBlock{Text: "hello"},
			},
		},
	}

	withSystem := PrependSystemMessage(original, "be concise")
	if len(withSystem) != len(original) {
		t.Fatalf("len(PrependSystemMessage()) = %d, want %d", len(withSystem), len(original))
	}
	block, ok := withSystem[0].Blocks[0].(TextBlock)
	if !ok {
		t.Fatalf("first block type = %T, want TextBlock", withSystem[0].Blocks[0])
	}
	if block.Text != "already here" {
		t.Fatalf("first block text = %q, want %q", block.Text, "already here")
	}
}

func TestPrepareRequestPayloadAnthropicRejectsAudioBlocks(t *testing.T) {
	_, err := PrepareRequestPayload(
		"anthropic",
		"claude-haiku-4-5",
		[]TypedMessage{
			{
				Role: string(RoleUser),
				Blocks: []Block{
					TextBlock{Text: "Transcribe this"},
					AudioBlock{Data: "base64-audio", Format: "wav"},
				},
			},
		},
		"",
		nil,
		nil,
		false,
	)
	if err == nil {
		t.Fatal("expected PrepareRequestPayload() to reject audio blocks for anthropic")
	}
	if got := err.Error(); got != "anthropic provider does not support audio input blocks" {
		t.Fatalf("PrepareRequestPayload() error = %q, want anthropic audio rejection", got)
	}
}

func TestPrepareRequestPayloadArgoRejectsAudioBlocks(t *testing.T) {
	cfg := NewTestRequestConfig()
	cfg.Provider = "argo"
	cfg.Model = "gpt5mini"
	cfg.Env = "http://argo.example.test"

	_, _, err := buildChatRequestFromTyped(
		cfg,
		[]TypedMessage{
			{
				Role: string(RoleUser),
				Blocks: []Block{
					TextBlock{Text: "Transcribe this"},
					AudioBlock{Data: "base64-audio", Format: "wav"},
				},
			},
		},
		"gpt5mini",
		"",
		false,
		nil,
		nil,
		false,
	)
	if err == nil {
		t.Fatal("expected buildChatRequestFromTyped() to reject audio blocks for argo")
	}
	if got := err.Error(); got != "argo provider does not support audio input blocks" {
		t.Fatalf("buildChatRequestFromTyped() error = %q, want argo audio rejection", got)
	}
}
