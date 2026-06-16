package core

import (
	"lmtools/internal/providers"
	"os"
	"testing"
)

func buildLegacyArgoMaxTokensRequest(t *testing.T, model string, maxTokens int, stream bool, tools []ToolDefinition) map[string]interface{} {
	t.Helper()
	cfg, apiKeyFile, err := createTestConfig("argo", model, false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)
	cfg.ArgoLegacy = true
	cfg.MaxTokens = maxTokens

	messages := []TypedMessage{NewTextMessage("user", "Hello")}
	req, _, err := buildProviderTestRequest(cfg, messages, model, "You are helpful", tools, nil, stream)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}
	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}
	return jsonData
}

func TestLegacyArgoClaudeMaxTokens_DropsLargeNonStreaming(t *testing.T) {
	// Default for a non-opus Claude model is 64000 (>= 21000); on the legacy
	// non-streaming route it must be dropped entirely.
	jsonData := buildLegacyArgoMaxTokensRequest(t, "claude-sonnet-4-6", 0, false, nil)
	if _, exists := jsonData["max_tokens"]; exists {
		t.Errorf("max_tokens should be dropped for legacy non-streaming Claude >= 21000, got %v", jsonData["max_tokens"])
	}
}

func TestLegacyArgoClaudeMaxTokens_KeepsSmallNonStreaming(t *testing.T) {
	jsonData := buildLegacyArgoMaxTokensRequest(t, "claude-sonnet-4-6", 4096, false, nil)
	got, exists := jsonInt(t, jsonData, "max_tokens")
	if !exists {
		t.Fatal("max_tokens should be kept for legacy non-streaming Claude < 21000")
	}
	if got != 4096 {
		t.Errorf("max_tokens = %d, want 4096", got)
	}
}

func TestLegacyArgoClaudeMaxTokens_KeepsLargeStreaming(t *testing.T) {
	// Real streaming (no tools) uses the streaming endpoint, which accepts large
	// max_tokens, so the 64000 default is kept.
	jsonData := buildLegacyArgoMaxTokensRequest(t, "claude-sonnet-4-6", 0, true, nil)
	got, exists := jsonInt(t, jsonData, "max_tokens")
	if !exists {
		t.Fatal("max_tokens should be kept for legacy streaming Claude without tools")
	}
	if got != providers.DefaultClaudeDefaultMaxTokens {
		t.Errorf("max_tokens = %d, want %d", got, providers.DefaultClaudeDefaultMaxTokens)
	}
}

func TestLegacyArgoClaudeMaxTokens_DropsLargeStreamingWithTools(t *testing.T) {
	// Streaming with tools falls back to the non-streaming endpoint, so a large
	// value is dropped just like a non-streaming request.
	tools := createTestTools()
	jsonData := buildLegacyArgoMaxTokensRequest(t, "claude-sonnet-4-6", 0, true, tools)
	if _, exists := jsonData["max_tokens"]; exists {
		t.Errorf("max_tokens should be dropped for legacy streaming-with-tools Claude >= 21000, got %v", jsonData["max_tokens"])
	}
}
