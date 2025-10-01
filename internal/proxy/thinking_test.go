package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"testing"
)

func TestThinkingFieldConversion(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)
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
			if tt.model == "gpt-4" || tt.model == "o3-mini" {
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

				openAIReq, err := converter.ConvertAnthropicToOpenAI(ctx, req)
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

			argoReq, err := converter.ConvertAnthropicToArgo(ctx, req, "testuser")
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
			if !tt.expectThinking && argoReq.Thinking != nil {
				t.Errorf("Expected no thinking field, got %+v", argoReq.Thinking)
			}
		})
	}
}
