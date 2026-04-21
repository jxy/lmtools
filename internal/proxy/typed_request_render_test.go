package proxy

import (
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
