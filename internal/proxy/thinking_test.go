package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"strings"
	"testing"
)

func TestThinkingFieldConversion(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name            string
		model           string
		thinking        *AnthropicThinking
		expectReasoning bool
		expectThinking  bool
	}{
		{
			name:  "GPT model with thinking enabled",
			model: "gpt-4",
			thinking: &AnthropicThinking{
				Type:         "enabled",
				BudgetTokens: 31999,
			},
			expectReasoning: true,
			expectThinking:  false,
		},
		{
			name:  "O3 model with thinking enabled",
			model: "o3-mini",
			thinking: &AnthropicThinking{
				Type:         "enabled",
				BudgetTokens: 25000,
			},
			expectReasoning: true,
			expectThinking:  false,
		},
		{
			name:  "Claude model with thinking enabled",
			model: "claude-opus-4",
			thinking: &AnthropicThinking{
				Type:         "enabled",
				BudgetTokens: 31999,
			},
			expectReasoning: false,
			expectThinking:  true,
		},
		{
			name:  "Claude model with adaptive summarized thinking",
			model: "claude-opus-4-7",
			thinking: &AnthropicThinking{
				Type:    "adaptive",
				Display: "summarized",
			},
			expectReasoning: false,
			expectThinking:  true,
		},
		{
			name:  "GPT model converts adaptive thinking",
			model: "gpt-5",
			thinking: &AnthropicThinking{
				Type:    "adaptive",
				Display: "summarized",
			},
			expectReasoning: true,
			expectThinking:  false,
		},
		{
			name:            "Model without thinking",
			model:           "gpt-4",
			thinking:        nil,
			expectReasoning: false,
			expectThinking:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test OpenAI conversion
			if strings.HasPrefix(tt.model, "gpt") || strings.HasPrefix(tt.model, "o3") || strings.HasPrefix(tt.model, "o4") {
				req := &AnthropicRequest{
					Model:     tt.model,
					MaxTokens: 1000,
					Messages: []AnthropicMessage{
						{
							Role:    core.RoleUser,
							Content: json.RawMessage(`"Test message"`),
						},
					},
					Thinking: tt.thinking,
				}

				openAIReq, err := ConvertAnthropicToOpenAI(ctx, req)
				if err != nil {
					t.Fatalf("Failed to convert to OpenAI: %v", err)
				}

				if tt.expectReasoning && openAIReq.ReasoningEffort != "high" {
					t.Errorf("Expected reasoning_effort=high, got %s", openAIReq.ReasoningEffort)
				}
				if !tt.expectReasoning && openAIReq.ReasoningEffort != "" {
					t.Errorf("Expected no reasoning_effort, got %s", openAIReq.ReasoningEffort)
				}
			}

			// Test Argo conversion
			req := &AnthropicRequest{
				Model:     tt.model,
				MaxTokens: 1000,
				Messages: []AnthropicMessage{
					{
						Role:    core.RoleUser,
						Content: json.RawMessage(`"Test message"`),
					},
				},
				Thinking: tt.thinking,
			}

			argoReq, err := ConvertAnthropicToArgo(ctx, req, "testuser")
			if err != nil {
				t.Fatalf("Failed to convert to Argo: %v", err)
			}

			if tt.expectReasoning && argoReq.ReasoningEffort != "high" {
				t.Errorf("Expected reasoning_effort=high for Argo, got %s", argoReq.ReasoningEffort)
			}
			if !tt.expectReasoning && argoReq.ReasoningEffort != "" {
				t.Errorf("Expected no reasoning_effort for Argo, got %s", argoReq.ReasoningEffort)
			}

			if tt.expectThinking && argoReq.Thinking == nil {
				t.Errorf("Expected thinking field to be passed through for Claude model")
			}
			if tt.expectThinking && tt.thinking.Display != "" && argoReq.Thinking != nil && argoReq.Thinking.Display != tt.thinking.Display {
				t.Errorf("Thinking display = %q, want %q", argoReq.Thinking.Display, tt.thinking.Display)
			}
			if !tt.expectThinking && argoReq.Thinking != nil {
				t.Errorf("Expected no thinking field, got %+v", argoReq.Thinking)
			}
		})
	}
}

func TestConvertAnthropicToArgoPreservesAdaptiveSummarizedThinkingAndEffort(t *testing.T) {
	req := &AnthropicRequest{
		Model:     "claude-opus-4-7",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{{
			Role:    core.RoleUser,
			Content: json.RawMessage(`"solve carefully"`),
		}},
		Thinking:     &AnthropicThinking{Type: "adaptive", Display: "summarized"},
		OutputConfig: &AnthropicOutputConfig{Effort: "xhigh"},
	}

	got, err := ConvertAnthropicToArgo(context.Background(), req, "testuser")
	if err != nil {
		t.Fatalf("ConvertAnthropicToArgo() error = %v", err)
	}
	if got.Thinking == nil || got.Thinking.Type != "adaptive" || got.Thinking.Display != "summarized" {
		t.Fatalf("Thinking = %+v, want adaptive summarized", got.Thinking)
	}
	if got.OutputConfig == nil || got.OutputConfig.Effort != "xhigh" {
		t.Fatalf("OutputConfig = %+v, want effort=xhigh", got.OutputConfig)
	}
	if got.ReasoningEffort != "" {
		t.Fatalf("ReasoningEffort = %q, want Anthropic output_config only", got.ReasoningEffort)
	}
}
