package core

import (
	"encoding/json"
	"lmtools/internal/prompts"
	"testing"
)

func TestConfiguredSystemPromptDoesNotUseDefault(t *testing.T) {
	cfg := TestRequestConfig{}

	if got := configuredSystemPrompt(cfg); got != "" {
		t.Fatalf("configuredSystemPrompt() = %q, want empty string", got)
	}
	if got := resolvedSystemPrompt(cfg, ""); got != prompts.DefaultSystemPrompt {
		t.Fatalf("resolvedSystemPrompt() = %q, want default prompt %q", got, prompts.DefaultSystemPrompt)
	}
}

func TestResolvedSystemPromptPrefersOverride(t *testing.T) {
	cfg := TestRequestConfig{
		System: "configured prompt",
	}

	if got := resolvedSystemPrompt(cfg, "override prompt"); got != "override prompt" {
		t.Fatalf("resolvedSystemPrompt() = %q, want override prompt", got)
	}
}

// TestAnthropicSystemPromptNoDouplication verifies that system prompts are not duplicated for Anthropic
func TestAnthropicSystemPromptNoDouplication(t *testing.T) {
	cfg := TestRequestConfig{
		User:        "test",
		Model:       "claude-3-opus-20240229",
		System:      "You are a helpful assistant",
		Provider:    "anthropic",
		ProviderURL: "https://api.anthropic.com/v1",
		StreamChat:  false,
		ToolEnabled: false,
	}

	req, body, err := BuildRequest(cfg, "Hello")
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	// Parse the request body
	var reqData map[string]interface{}
	if err := json.Unmarshal(body, &reqData); err != nil {
		t.Fatalf("Failed to parse request body: %v", err)
	}

	// Check that system is in the top-level field
	system, hasSystem := reqData["system"]
	if !hasSystem {
		t.Error("Expected 'system' field in Anthropic request")
	}
	if system != cfg.System {
		t.Errorf("Expected system=%q, got %q", cfg.System, system)
	}

	// Check that system is NOT in messages
	messages, ok := reqData["messages"].([]interface{})
	if !ok {
		t.Fatal("Expected 'messages' to be an array")
	}

	for _, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := msgMap["role"].(string); role == "system" {
			t.Error("Found system message in messages array - this is a duplication")
		}
	}

	// Verify the request URL
	if req.URL.String() != "https://api.anthropic.com/v1/messages" {
		t.Errorf("Unexpected URL: %s", req.URL.String())
	}
}

// TestArgoSystemPromptRouting verifies that Argo matches the native wire format
// chosen from the model name.
func TestArgoSystemPromptInMessages(t *testing.T) {
	tests := []struct {
		model        string
		expectField  string
		expectInline bool
	}{
		{
			model:        "claude-3-opus-20240229",
			expectField:  "system",
			expectInline: false,
		},
		{
			model:        "gpt-4",
			expectField:  "",
			expectInline: true,
		},
		{
			model:        "gemini-pro",
			expectField:  "",
			expectInline: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			cfg := TestRequestConfig{
				User:        "test",
				Model:       tt.model,
				System:      "You are a helpful assistant",
				Provider:    "argo",
				StreamChat:  false,
				ToolEnabled: false,
			}

			_, body, err := BuildRequest(cfg, "Hello")
			if err != nil {
				t.Fatalf("Failed to build request: %v", err)
			}

			// Parse the request body
			var reqData map[string]interface{}
			if err := json.Unmarshal(body, &reqData); err != nil {
				t.Fatalf("Failed to parse request body: %v", err)
			}

			if tt.expectField == "system" {
				if reqData["system"] != cfg.System {
					t.Fatalf("Expected top-level system=%q, got %v", cfg.System, reqData["system"])
				}
			} else if _, ok := reqData["system"]; ok {
				t.Fatalf("Did not expect top-level system field, got %v", reqData["system"])
			}

			messages, ok := reqData["messages"].([]interface{})
			if !ok {
				t.Fatal("Expected 'messages' to be an array")
			}

			firstMsg, ok := messages[0].(map[string]interface{})
			if !ok {
				t.Fatal("Expected first message to be a map")
			}

			role, _ := firstMsg["role"].(string)
			if tt.expectInline {
				if len(messages) < 2 {
					t.Fatal("Expected at least 2 messages (system + user)")
				}
				if role != "system" {
					t.Errorf("Expected first message role to be 'system', got %q", role)
				}

				content, _ := firstMsg["content"].(string)
				if content != cfg.System {
					t.Errorf("Expected system content=%q, got %q", cfg.System, content)
				}
			} else {
				if role != "user" {
					t.Errorf("Expected first message role to be 'user', got %q", role)
				}
			}
		})
	}
}

// TestSessionSystemPromptHandling tests system prompt extraction from sessions
func TestSessionSystemPromptHandling(t *testing.T) {
	// Create typed messages with system prompt
	messages := []TypedMessage{
		NewTextMessage("system", "You are a helpful assistant"),
		NewTextMessage("user", "Hello"),
		NewTextMessage("assistant", "Hi there!"),
	}

	// Test splitSystem
	system, rest := splitSystem(messages)
	if system != "You are a helpful assistant" {
		t.Errorf("Expected system=%q, got %q", "You are a helpful assistant", system)
	}
	if len(rest) != 2 {
		t.Errorf("Expected 2 remaining messages, got %d", len(rest))
	}
	if rest[0].Role != "user" {
		t.Errorf("Expected first remaining message to be user, got %s", rest[0].Role)
	}

	// Test with no system message
	messagesNoSystem := []TypedMessage{
		NewTextMessage("user", "Hello"),
		NewTextMessage("assistant", "Hi there!"),
	}
	system2, rest2 := splitSystem(messagesNoSystem)
	if system2 != "" {
		t.Errorf("Expected empty system, got %q", system2)
	}
	if len(rest2) != 2 {
		t.Errorf("Expected 2 messages unchanged, got %d", len(rest2))
	}
}

// TestProviderSystemHandling verifies each provider handles system prompts correctly
func TestProviderSystemHandling(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		providerURL  string
		expectField  string // "system", "systemInstruction", or "inline"
		expectInline bool   // whether system should be in messages array
	}{
		{
			name:         "OpenAI",
			provider:     "openai",
			providerURL:  "https://api.openai.com/v1",
			expectField:  "",
			expectInline: true,
		},
		{
			name:         "Anthropic",
			provider:     "anthropic",
			providerURL:  "https://api.anthropic.com/v1",
			expectField:  "system",
			expectInline: false,
		},
		{
			name:         "Google",
			provider:     "google",
			providerURL:  "https://generativelanguage.googleapis.com/v1beta",
			expectField:  "systemInstruction",
			expectInline: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := TestRequestConfig{
				User:        "test",
				Model:       "test-model",
				System:      "You are a helpful assistant",
				Provider:    tt.provider,
				ProviderURL: tt.providerURL,
				StreamChat:  false,
				ToolEnabled: false,
			}

			_, body, err := BuildRequest(cfg, "Hello")
			if err != nil {
				t.Fatalf("Failed to build request: %v", err)
			}

			// Parse the request body
			var reqData map[string]interface{}
			if err := json.Unmarshal(body, &reqData); err != nil {
				t.Fatalf("Failed to parse request body: %v", err)
			}

			// Check for expected system field
			if tt.expectField != "" {
				if _, ok := reqData[tt.expectField]; !ok {
					t.Errorf("Expected %q field in request", tt.expectField)
				}
			}

			// Check inline system message
			if tt.provider != "google" { // Google uses different message structure
				messages, ok := reqData["messages"].([]interface{})
				if ok {
					hasSystemInline := false
					for _, msg := range messages {
						if msgMap, ok := msg.(map[string]interface{}); ok {
							if role, _ := msgMap["role"].(string); role == "system" {
								hasSystemInline = true
								break
							}
						}
					}
					if hasSystemInline != tt.expectInline {
						t.Errorf("Expected inline system=%v, got %v", tt.expectInline, hasSystemInline)
					}
				}
			}
		})
	}
}
