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
