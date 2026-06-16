package core

import (
	"lmtools/internal/providers"
	"os"
	"testing"
)

// jsonInt extracts an integer JSON field (decoded as float64) from a request body.
func jsonInt(t *testing.T, jsonData map[string]interface{}, key string) (int, bool) {
	t.Helper()
	value, exists := jsonData[key]
	if !exists {
		return 0, false
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("%s should be a number, got %T", key, value)
	}
	return int(number), true
}

func buildMaxTokensRequest(t *testing.T, provider, model string, maxTokens int, stream bool) map[string]interface{} {
	t.Helper()
	cfg, apiKeyFile, err := createTestConfig(provider, model, false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)
	cfg.MaxTokens = maxTokens

	messages := []TypedMessage{NewTextMessage("user", "Hello")}
	req, _, err := buildProviderTestRequest(cfg, messages, model, "You are helpful", nil, nil, stream)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}
	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}
	return jsonData
}

func TestAnthropicRequestJSON_MaxTokensDefault(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     int
	}{
		{"anthropic opus", "anthropic", "claude-opus-4-8", providers.DefaultClaudeOpusMaxTokens},
		{"anthropic sonnet", "anthropic", "claude-sonnet-4-6", providers.DefaultClaudeDefaultMaxTokens},
		{"argo claude opus", "argo", "claude-opus-4-8", providers.DefaultClaudeOpusMaxTokens},
		{"argo claude sonnet", "argo", "claude-sonnet-4-6", providers.DefaultClaudeDefaultMaxTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Non-streaming: the modern Anthropic wire must still include the
			// default (no >= 21000 drop on the modern path).
			jsonData := buildMaxTokensRequest(t, tt.provider, tt.model, 0, false)
			got, exists := jsonInt(t, jsonData, "max_tokens")
			if !exists {
				t.Fatalf("max_tokens missing for %s/%s", tt.provider, tt.model)
			}
			if got != tt.want {
				t.Errorf("max_tokens = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAnthropicRequestJSON_MaxTokensExplicit(t *testing.T) {
	for _, provider := range []string{"anthropic", "argo"} {
		t.Run(provider, func(t *testing.T) {
			jsonData := buildMaxTokensRequest(t, provider, "claude-opus-4-8", 1234, false)
			got, exists := jsonInt(t, jsonData, "max_tokens")
			if !exists {
				t.Fatalf("max_tokens missing for provider %s", provider)
			}
			if got != 1234 {
				t.Errorf("explicit max_tokens = %d, want 1234", got)
			}
		})
	}
}

func TestArgoOpenAIRequestJSON_MaxTokens(t *testing.T) {
	// Non-claude Argo models render to the OpenAI wire. The default injection is
	// Claude-only, so without an explicit value no token limit is emitted.
	jsonData := buildMaxTokensRequest(t, "argo", "gpt-4", 0, false)
	if _, exists := jsonData["max_tokens"]; exists {
		t.Error("max_tokens should not be set for non-claude Argo models by default")
	}
	if _, exists := jsonData["max_completion_tokens"]; exists {
		t.Error("max_completion_tokens should not be set without an explicit value")
	}

	// With an explicit value, the OpenAI wire uses max_completion_tokens.
	jsonData = buildMaxTokensRequest(t, "argo", "gpt-4", 2000, false)
	got, exists := jsonInt(t, jsonData, "max_completion_tokens")
	if !exists {
		t.Fatal("max_completion_tokens should be set for explicit -max-tokens on OpenAI wire")
	}
	if got != 2000 {
		t.Errorf("max_completion_tokens = %d, want 2000", got)
	}
	if _, exists := jsonData["max_tokens"]; exists {
		t.Error("OpenAI wire should use max_completion_tokens, not max_tokens")
	}
}
