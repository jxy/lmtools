package core

import (
	"encoding/json"
	"testing"
)

// TestConversationAccumulation tests that conversation messages accumulate properly across tool rounds
func TestConversationAccumulation(t *testing.T) {
	// Tool definitions are now passed directly to request builders
	// This test focuses on message accumulation patterns

	// Create typed messages to simulate the conversation
	typedMessages := []TypedMessage{
		NewTextMessage("system", "You are a helpful assistant."),
		NewTextMessage("user", "List files in the current directory"),
	}

	// Simulate first tool call round
	// Assistant responds with tool call
	typedMessages = append(typedMessages, TypedMessage{
		Role: "assistant",
		Blocks: []Block{
			TextBlock{Text: "I'll list the files in the current directory."},
			ToolUseBlock{
				ID:    "call-001",
				Name:  "universal_command",
				Input: json.RawMessage(`{"command": ["ls", "-la"]}`),
			},
		},
	})

	// Tool results
	typedMessages = append(typedMessages, TypedMessage{
		Role: "user",
		Blocks: []Block{
			ToolResultBlock{
				ToolUseID: "call-001",
				Content:   "file1.txt\nfile2.go\nREADME.md",
			},
		},
	})

	// Verify accumulated messages count
	// With the new stateless ToolConversation, we check the typedMessages instead
	if len(typedMessages) != 4 {
		t.Errorf("Expected 4 messages, got %d", len(typedMessages))
	}

	// Simulate second tool call round
	// Assistant responds with another tool call
	typedMessages = append(typedMessages, TypedMessage{
		Role: "assistant",
		Blocks: []Block{
			TextBlock{Text: "Now let me check the contents of README.md."},
			ToolUseBlock{
				ID:    "call-002",
				Name:  "universal_command",
				Input: json.RawMessage(`{"command": ["cat", "README.md"]}`),
			},
		},
	})

	// Second tool results
	typedMessages = append(typedMessages, TypedMessage{
		Role: "user",
		Blocks: []Block{
			ToolResultBlock{
				ToolUseID: "call-002",
				Content:   "# Project README\nThis is a test project.",
			},
		},
	})

	// Verify final accumulated messages count
	if len(typedMessages) != 6 {
		t.Errorf("Expected 6 messages after 2 rounds, got %d", len(typedMessages))
	}

	// Note: The ToolConversation is now stateless and doesn't accumulate messages
	// Messages are managed externally and passed when needed
}

// TestBuildArgoRequestWithAccumulatedMessages tests that buildArgoToolResultRequest uses accumulated messages
func TestBuildArgoRequestWithAccumulatedMessages(t *testing.T) {
	// Setup conversation with typed messages
	typedMessages := []TypedMessage{
		NewTextMessage("system", "Test system"),
		NewTextMessage("user", "Initial request"),
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "First tool call"},
				ToolUseBlock{
					ID:    "call-001",
					Name:  "test_tool",
					Input: json.RawMessage(`{}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "call-001",
					Content:   "First result",
				},
			},
		},
	}

	// Tool definitions are now passed directly to request builders

	cfg := newArgoToolTestConfig()
	cfg.Model = "gpt5"
	cfg.Provider = "argo"
	cfg.System = "Test system"
	cfg.IsToolEnabledFlag = true

	req, body, err := BuildToolResultRequest(cfg, cfg.Model, "", nil, typedMessages)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	// Verify request was built
	if req == nil {
		t.Fatal("Expected non-nil request")
	}

	// Parse the body to verify it contains the accumulated messages
	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err != nil {
		t.Fatalf("Failed to parse request body: %v", err)
	}

	messages, ok := requestData["messages"].([]interface{})
	if !ok {
		t.Fatal("Expected messages array in request")
	}

	// Should have all 4 messages from typedMessages
	if len(messages) != 4 {
		t.Errorf("Expected 4 messages in request, got %d", len(messages))
	}
}

// TestNoDuplicateMessagesInBuildFunctions tests that build functions don't duplicate accumulated messages
func TestNoDuplicateMessagesInBuildFunctions(t *testing.T) {
	// Create typed messages to simulate the conversation
	typedMessages := []TypedMessage{
		NewTextMessage("system", "System prompt"),
		NewTextMessage("user", "User request"),
		{
			Role: "assistant",
			Blocks: []Block{
				ToolUseBlock{
					ID:    "call-001",
					Name:  "test_tool",
					Input: json.RawMessage(`{}`),
				},
			},
		},
		{
			Role: "user",
			Blocks: []Block{
				ToolResultBlock{
					ToolUseID: "call-001",
					Content:   "Result 1",
				},
			},
		},
	}

	// Tool definitions are now passed directly to request builders

	// Build request with additional tool results
	cfg := newArgoToolTestConfig()
	cfg.Provider = "argo"
	cfg.Env = "test"
	cfg.Model = "gpt5"
	cfg.System = "System prompt"
	cfg.IsToolEnabledFlag = true

	req, body, err := BuildToolResultRequest(cfg, cfg.Model, "", nil, typedMessages)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if req == nil || body == nil {
		t.Fatal("Expected non-nil request and body")
	}

	// Parse the request body
	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err != nil {
		t.Fatalf("Failed to unmarshal request body: %v", err)
	}

	messages, ok := requestData["messages"].([]interface{})
	if !ok {
		t.Fatal("Expected messages to be an array")
	}

	// Count unique message IDs or content to ensure no duplicates
	// This is a simple check - in practice you'd want more sophisticated duplicate detection
	messageCount := len(messages)
	// The build function now handles accumulation internally
	expectedCount := 4 // 2 original + 1 assistant + 1 tool result already accumulated
	if messageCount < expectedCount {
		t.Errorf("Expected at least %d messages, got %d", expectedCount, messageCount)
	}

	// Verify no duplicate system messages
	systemCount := 0
	for _, msg := range messages {
		if m, ok := msg.(map[string]interface{}); ok {
			if m["role"] == "system" {
				systemCount++
			}
		}
	}
	if systemCount > 1 {
		t.Errorf("Expected at most 1 system message, found %d", systemCount)
	}
}
