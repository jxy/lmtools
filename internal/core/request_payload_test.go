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
