package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// mockSessionStore tracks all saved messages to detect duplicates
type mockSessionStore struct {
	path           string
	savedMessages  []savedMessage
	messageCounter int
}

type savedMessage struct {
	role      string
	text      string
	toolCalls []ToolCall
	results   []ToolResult
}

func (m *mockSessionStore) GetPath() string {
	return m.path
}

func (m *mockSessionStore) UpdatePath(newPath string) {
	m.path = newPath
}

func (m *mockSessionStore) SaveAssistant(ctx context.Context, text string, toolCalls []ToolCall, model string) (string, string, error) {
	m.messageCounter++
	m.savedMessages = append(m.savedMessages, savedMessage{
		role:      "assistant",
		text:      text,
		toolCalls: toolCalls,
	})
	return m.path, fmt.Sprintf("msg_%04d", m.messageCounter), nil
}

func (m *mockSessionStore) SaveToolResults(ctx context.Context, results []ToolResult, additionalText string) (string, string, error) {
	m.messageCounter++
	m.savedMessages = append(m.savedMessages, savedMessage{
		role:    "user",
		text:    additionalText,
		results: results,
	})
	return m.path, fmt.Sprintf("msg_%04d", m.messageCounter), nil
}

// TestMultipleToolRoundsNoDuplicates verifies that assistant messages are not duplicated
// across multiple rounds of tool execution
func TestMultipleToolRoundsNoDuplicates(t *testing.T) {
	tests := []struct {
		name          string
		rounds        int
		expectedSaves int // Expected number of assistant saves
		description   string
	}{
		{
			name:          "single_round",
			rounds:        1,
			expectedSaves: 2, // Initial + response after tools
			description:   "Single round should save initial and final response",
		},
		{
			name:          "two_rounds",
			rounds:        2,
			expectedSaves: 3, // Initial + 2 responses (one per round)
			description:   "Two rounds should save exactly 3 assistant messages",
		},
		{
			name:          "three_rounds",
			rounds:        3,
			expectedSaves: 4, // Initial + 3 responses (one per round)
			description:   "Three rounds should save exactly 4 assistant messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock components
			store := &mockSessionStore{path: "/test/session"}

			// Simulate tool execution with the fixed logic
			initialText := "I'll help you with that."
			initialCalls := []ToolCall{{
				ID:   "call_001",
				Name: "test_tool",
				Args: json.RawMessage(`{"arg": "value1"}`),
			}}

			// Run simulated tool execution
			finalText := simulateToolExecution(t, store, tt.rounds, initialText, initialCalls, "test-model")

			// Verify no duplicates in saved messages
			assistantSaves := 0
			seenMessages := make(map[string]bool)

			for i, msg := range store.savedMessages {
				if msg.role == "assistant" {
					assistantSaves++

					// Create a unique key for this message
					key := fmt.Sprintf("%s|%s|%d", msg.role, msg.text, len(msg.toolCalls))
					if len(msg.toolCalls) > 0 {
						key += "|" + msg.toolCalls[0].ID
					}

					// Check for duplicates
					if seenMessages[key] {
						t.Errorf("Duplicate assistant message found at index %d: %s", i, key)

						// Log all messages for debugging
						t.Logf("All saved messages:")
						for j, m := range store.savedMessages {
							t.Logf("  [%d] Role: %s, Text: %s, ToolCalls: %d",
								j, m.role, truncate(m.text, 50), len(m.toolCalls))
						}
					}
					seenMessages[key] = true
				}
			}

			// Verify expected number of saves
			if assistantSaves != tt.expectedSaves {
				t.Errorf("Expected %d assistant saves, got %d", tt.expectedSaves, assistantSaves)

				// Log all messages for debugging
				t.Logf("All saved messages:")
				for i, m := range store.savedMessages {
					t.Logf("  [%d] Role: %s, Text: %s, ToolCalls: %d, Results: %d",
						i, m.role, truncate(m.text, 50), len(m.toolCalls), len(m.results))
				}
			}

			// Verify we got a final response
			if finalText == "" {
				t.Error("Expected non-empty final text")
			}
		})
	}
}

// TestDuplicateDetectionInRequests verifies that the message builder doesn't create
// duplicate messages when building requests
func TestDuplicateDetectionInRequests(t *testing.T) {
	store := &mockSessionStore{path: "/test/session"}

	// Simulate the problematic scenario:
	// 1. Initial assistant with tool calls
	_, _, _ = store.SaveAssistant(context.Background(), "Let me check that.", []ToolCall{{
		ID:   "call_001",
		Name: "tool1",
		Args: json.RawMessage(`{}`),
	}}, "model")

	// 2. Tool results
	_, _, _ = store.SaveToolResults(context.Background(), []ToolResult{{
		ID:     "call_001",
		Output: "Result 1",
	}}, "")

	// 3. Assistant response with more tool calls (this would be saved twice in the bug)
	_, _, _ = store.SaveAssistant(context.Background(), "Now let me check something else.", []ToolCall{{
		ID:   "call_002",
		Name: "tool2",
		Args: json.RawMessage(`{}`),
	}}, "model")

	// Build messages for request
	messages := buildMessagesFromStore(store)

	// Check for duplicate assistant messages
	assistantMessages := make(map[string]int)
	for i, msg := range messages {
		if msg.Role == "assistant" {
			key := fmt.Sprintf("%s_%d_blocks", msg.Role, len(msg.Blocks))
			if len(msg.Blocks) > 0 {
				if tb, ok := msg.Blocks[0].(TextBlock); ok {
					key = tb.Text
				}
			}

			if prev, exists := assistantMessages[key]; exists {
				t.Errorf("Duplicate assistant message found at indices %d and %d: %s", prev, i, key)
			}
			assistantMessages[key] = i
		}
	}

	// Should have exactly 2 assistant messages
	if len(assistantMessages) != 2 {
		t.Errorf("Expected 2 unique assistant messages, got %d", len(assistantMessages))
	}
}

func TestInitialToolResponsePreservesResponseBlocks(t *testing.T) {
	toolCalls := []ToolCall{{
		ID:   "call_001",
		Name: "lookup",
		Args: json.RawMessage(`{"q":"x"}`),
	}}
	initial := Response{
		ToolCalls: toolCalls,
		Blocks: []Block{
			ReasoningBlock{
				Provider:         "openai",
				Type:             "reasoning",
				ID:               "rs_001",
				Status:           "completed",
				EncryptedContent: "enc_001",
			},
			ToolUseBlock{
				ID:    "call_001",
				Name:  "lookup",
				Input: json.RawMessage(`{"q":"x"}`),
			},
		},
	}

	response := initialToolResponse(ToolContext{
		InitialResponse: initial,
		InitialCalls:    toolCalls,
	})
	if len(response.Blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(response.Blocks))
	}

	store := NewMemorySessionStore("", "")
	if err := persistAssistantRound(context.Background(), store, response, "gpt-5", nil); err != nil {
		t.Fatalf("persistAssistantRound() error = %v", err)
	}
	messages, err := store.Messages("")
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	reasoning, ok := messages[0].Blocks[0].(ReasoningBlock)
	if !ok {
		t.Fatalf("first block type = %T, want ReasoningBlock", messages[0].Blocks[0])
	}
	if reasoning.EncryptedContent != "enc_001" {
		t.Fatalf("EncryptedContent = %q, want enc_001", reasoning.EncryptedContent)
	}
}

// Helper functions

func simulateToolExecution(t *testing.T, store SessionStore, maxRounds int, initialText string, initialCalls []ToolCall, model string) string {
	// This simulates the fixed handleToolExecution logic
	ctx := context.Background()
	rounds := 0
	text := initialText
	toolCalls := initialCalls
	finalText := text

	for rounds < maxRounds && len(toolCalls) > 0 {
		rounds++

		// Save assistant's response with tool calls only on first round
		// (This is the fix we're testing)
		if rounds == 1 {
			_, _, err := store.SaveAssistant(ctx, text, toolCalls, model)
			if err != nil {
				t.Fatal(err)
			}
		}

		// Simulate tool execution
		results := []ToolResult{{
			ID:     toolCalls[0].ID,
			Output: fmt.Sprintf("Result for round %d", rounds),
		}}

		// Save tool results
		_, _, err := store.SaveToolResults(ctx, results, "")
		if err != nil {
			t.Fatal(err)
		}

		// Simulate model response
		if rounds < maxRounds {
			// More tool calls
			text = fmt.Sprintf("Round %d response with more tools", rounds)
			toolCalls = []ToolCall{{
				ID:   fmt.Sprintf("call_%03d", rounds+1),
				Name: "test_tool",
				Args: json.RawMessage(fmt.Sprintf(`{"round": %d}`, rounds+1)),
			}}
		} else {
			// Final response without tools
			text = "Final response without tools"
			toolCalls = nil
		}

		if text != "" {
			finalText = text
		}

		// Save response if we have content or tool calls
		// (This happens at the end of each round)
		if text != "" || len(toolCalls) > 0 {
			_, _, err := store.SaveAssistant(ctx, text, toolCalls, model)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	return finalText
}

func buildMessagesFromStore(store *mockSessionStore) []TypedMessage {
	var messages []TypedMessage

	for _, saved := range store.savedMessages {
		msg := TypedMessage{Role: saved.role}

		if saved.text != "" {
			msg.Blocks = append(msg.Blocks, TextBlock{Text: saved.text})
		}

		for _, tc := range saved.toolCalls {
			msg.Blocks = append(msg.Blocks, ToolUseBlock{
				ID:    tc.ID,
				Name:  tc.Name,
				Input: tc.Args,
			})
		}

		for _, tr := range saved.results {
			msg.Blocks = append(msg.Blocks, ToolResultBlock{
				ToolUseID: tr.ID,
				Content:   tr.Output,
			})
		}

		messages = append(messages, msg)
	}

	return messages
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
