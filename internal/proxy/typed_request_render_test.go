package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
	"testing"
)

func TestTypedToAnthropicRequestOmitsTopPWhenTemperaturePresent(t *testing.T) {
	req, err := TypedToAnthropicRequest(TypedRequest{
		MaxTokens:   intPtr(32),
		Temperature: float64Ptr(0.2),
		TopP:        float64Ptr(0.9),
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "hi"},
				},
			},
		},
	}, "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Fatalf("Temperature = %v, want 0.2", req.Temperature)
	}
	if req.TopP != nil {
		t.Fatalf("TopP = %v, want nil when temperature is set", req.TopP)
	}
}

func TestTypedToAnthropicRequestPreservesTopPWithoutTemperature(t *testing.T) {
	req, err := TypedToAnthropicRequest(TypedRequest{
		MaxTokens: intPtr(32),
		TopP:      float64Ptr(0.9),
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "hi"},
				},
			},
		},
	}, "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if req.TopP == nil || *req.TopP != 0.9 {
		t.Fatalf("TopP = %v, want 0.9", req.TopP)
	}
}

func TestTypedToAnthropicRequestOmitsTemperatureForOpus47(t *testing.T) {
	req, err := TypedToAnthropicRequest(TypedRequest{
		MaxTokens:   intPtr(32),
		Temperature: float64Ptr(0.2),
		TopP:        float64Ptr(0.9),
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "hi"},
				},
			},
		},
	}, "claude-opus-4-7")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if req.Temperature != nil {
		t.Fatalf("Temperature = %v, want nil for Opus 4.7", req.Temperature)
	}
	if req.TopP != nil {
		t.Fatalf("TopP = %v, want nil when source temperature was supplied", req.TopP)
	}
}

func TestTypedToAnthropicRequestPreservesOpus47ThinkingConfig(t *testing.T) {
	req, err := TypedToAnthropicRequest(TypedRequest{
		MaxTokens: intPtr(128),
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "solve carefully"},
				},
			},
		},
		Thinking: &AnthropicThinking{
			Type:    "adaptive",
			Display: "summarized",
		},
		OutputConfig: &AnthropicOutputConfig{Effort: "xhigh"},
	}, "claude-opus-4-7")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if req.Thinking == nil || req.Thinking.Type != "adaptive" || req.Thinking.Display != "summarized" {
		t.Fatalf("Thinking = %+v, want adaptive summarized", req.Thinking)
	}
	if req.OutputConfig == nil || req.OutputConfig.Effort != "xhigh" {
		t.Fatalf("OutputConfig = %+v, want effort=xhigh", req.OutputConfig)
	}
}

func TestTypedToAnthropicRequestAllowsOutputConfigForIntermediateNonAnthropicModels(t *testing.T) {
	req, err := TypedToAnthropicRequest(TypedRequest{
		MaxTokens: intPtr(64),
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "return json"},
				},
			},
		},
		ResponseFormat:  &ResponseFormat{Type: "json_object"},
		ReasoningEffort: "high",
	}, "gemini-3.1-flash-lite-preview")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if req.OutputConfig == nil || req.OutputConfig.Format == nil || req.OutputConfig.Effort != "high" {
		t.Fatalf("OutputConfig = %+v, want preserved intermediate output_config", req.OutputConfig)
	}
}

func TestTypedToAnthropicRequestPreservesStrictToolUse(t *testing.T) {
	trueValue := true
	req, err := TypedToAnthropicRequest(TypedRequest{
		MaxTokens: intPtr(32),
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "lookup"},
				},
			},
		},
		Tools: []core.ToolDefinition{
			{
				Name:        "lookup",
				Description: "Lookup a value.",
				InputSchema: map[string]interface{}{
					"type":                 "object",
					"properties":           map[string]interface{}{},
					"additionalProperties": false,
				},
				Strict: &trueValue,
			},
		},
	}, "claude-opus-4-7")
	if err != nil {
		t.Fatalf("TypedToAnthropicRequest() error = %v", err)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
	}
	if req.Tools[0].Strict == nil || *req.Tools[0].Strict != trueValue {
		t.Fatalf("Anthropic tool strict = %#v, want true", req.Tools[0].Strict)
	}
	data, err := json.Marshal(req.Tools[0])
	if err != nil {
		t.Fatalf("json.Marshal(tool) error = %v", err)
	}
	var rendered map[string]interface{}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("json.Unmarshal(tool) error = %v", err)
	}
	if rendered["strict"] != true {
		t.Fatalf("serialized Anthropic tool strict = %#v, want true; json = %s", rendered["strict"], data)
	}
}
