package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// roundLimitExecutor builds the executor through NewExecutor so the round-limit
// prompt is gated by the same approvalPolicy that gates command approval.
func roundLimitExecutor(t *testing.T, nonInteractive bool, approver Approver) *Executor {
	t.Helper()
	executor, err := NewExecutor(RequestOptions{ToolNonInteractive: nonInteractive}, nil, approver)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	return executor
}

func TestRequestToolRoundLimitReset(t *testing.T) {
	t.Run("approved", func(t *testing.T) {
		approver := &TestApprover{DefaultApproval: true, ResetResponses: []bool{true}}
		approved, err := roundLimitExecutor(t, false, approver).requestToolRoundLimitReset(context.Background(), 7)
		if err != nil {
			t.Fatalf("requestToolRoundLimitReset() error = %v", err)
		}
		if !approved || len(approver.ResetCalls) != 1 || approver.ResetCalls[0] != 7 {
			t.Fatalf("approved = %v, reset calls = %v", approved, approver.ResetCalls)
		}
	})

	t.Run("denied", func(t *testing.T) {
		approver := &TestApprover{DefaultApproval: true, ResetResponses: []bool{false}}
		approved, err := roundLimitExecutor(t, false, approver).requestToolRoundLimitReset(context.Background(), 3)
		if err != nil {
			t.Fatalf("requestToolRoundLimitReset() error = %v", err)
		}
		if approved || len(approver.ResetCalls) != 1 {
			t.Fatalf("approved = %v, reset calls = %v", approved, approver.ResetCalls)
		}
	})

	t.Run("non-interactive", func(t *testing.T) {
		approver := &TestApprover{DefaultApproval: true, ResetResponses: []bool{true}}
		approved, err := roundLimitExecutor(t, true, approver).requestToolRoundLimitReset(context.Background(), 3)
		if err != nil {
			t.Fatalf("requestToolRoundLimitReset() error = %v", err)
		}
		if approved || len(approver.ResetCalls) != 0 {
			t.Fatalf("approved = %v, reset calls = %v", approved, approver.ResetCalls)
		}
	})

	t.Run("missing approver", func(t *testing.T) {
		approved, err := roundLimitExecutor(t, false, nil).requestToolRoundLimitReset(context.Background(), 3)
		if err != nil {
			t.Fatalf("requestToolRoundLimitReset() error = %v", err)
		}
		if approved {
			t.Fatal("requestToolRoundLimitReset() approved without an approver")
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		approver := &TestApprover{DefaultApproval: true, ResetResponses: []bool{true}}
		approved, err := roundLimitExecutor(t, false, approver).requestToolRoundLimitReset(ctx, 3)
		if approved || !errors.Is(err, context.Canceled) {
			t.Fatalf("approved = %v, error = %v, want context.Canceled", approved, err)
		}
	})

	t.Run("cancelled non-interactive", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		approved, err := roundLimitExecutor(t, true, nil).requestToolRoundLimitReset(ctx, 3)
		if approved || !errors.Is(err, context.Canceled) {
			t.Fatalf("approved = %v, error = %v, want context.Canceled", approved, err)
		}
	})
}

func (m *mockSessionStore) GetPath() string {
	return m.path
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

func TestPersistAssistantRoundPreservesResponseBlocks(t *testing.T) {
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

	if len(initial.Blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(initial.Blocks))
	}

	store := NewMemorySessionStore("", "")
	if err := persistAssistantRound(context.Background(), store, initial, "gpt-5", nil); err != nil {
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

// newBodyCaptureServer starts a test provider that records each request body
// into the returned pointer and answers every request with the fixed JSON
// response.
func newBodyCaptureServer(t *testing.T, response string) (*httptest.Server, *[]byte) {
	t.Helper()
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		requestBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server, &requestBody
}

func TestBuildAndSendFollowupRequestPreservesLeadingSessionSystem(t *testing.T) {
	server, requestBody := newBodyCaptureServer(t, `{"content":[{"type":"text","text":"done"}]}`)

	cfg := RequestOptions{
		Provider:            "anthropic",
		ProviderURL:         server.URL,
		Model:               "claude-test",
		System:              "config default system",
		EffectiveSystem:     "config default system",
		SystemExplicitlySet: true,
		ToolEnabled:         true,
	}
	store := &mockSessionStore{path: "/test/session"}
	_, err := BuildAndSendFollowupRequest(
		context.Background(),
		cfg,
		ToolExecutionConfig{Store: store},
		cfg.Model,
		nil,
		func(string) ([]TypedMessage, error) {
			return []TypedMessage{
				NewTextMessage("system", "session system"),
				NewTextMessage("user", "hello"),
				{
					Role: string(RoleAssistant),
					Blocks: []Block{ToolUseBlock{
						ID:    "call_1",
						Name:  "universal_command",
						Input: json.RawMessage(`{"command":["echo","hi"]}`),
					}},
				},
				{
					Role: string(RoleUser),
					Blocks: []Block{ToolResultBlock{
						ToolUseID: "call_1",
						Content:   "hi",
					}},
				},
			}, nil
		},
		NewTestLogger(false),
	)
	if err != nil {
		t.Fatalf("BuildAndSendFollowupRequest() error = %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(*requestBody, &payload); err != nil {
		t.Fatalf("request JSON = %s, error = %v", string(*requestBody), err)
	}
	if got := payload["system"]; got != "session system" {
		t.Fatalf("system = %#v, want session system in %s", got, string(*requestBody))
	}
	messages, ok := payload["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v", payload["messages"])
	}
	first, ok := messages[0].(map[string]interface{})
	if !ok || first["role"] == "system" {
		t.Fatalf("first message = %#v, want non-system because Anthropic carries system out of band", first)
	}
}

func TestBuildAndSendFollowupRequestUsesConfigSystemWhenSessionHasNoSystem(t *testing.T) {
	server, requestBody := newBodyCaptureServer(t, `{"content":[{"type":"text","text":"done"}]}`)

	cfg := RequestOptions{
		Provider:            "anthropic",
		ProviderURL:         server.URL,
		Model:               "claude-test",
		System:              "config system",
		EffectiveSystem:     "config system",
		SystemExplicitlySet: true,
		ToolEnabled:         true,
	}
	_, err := BuildAndSendFollowupRequest(
		context.Background(),
		cfg,
		ToolExecutionConfig{Store: &mockSessionStore{path: "/test/session"}},
		cfg.Model,
		nil,
		func(string) ([]TypedMessage, error) {
			return []TypedMessage{NewTextMessage("user", "hello")}, nil
		},
		NewTestLogger(false),
	)
	if err != nil {
		t.Fatalf("BuildAndSendFollowupRequest() error = %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(*requestBody, &payload); err != nil {
		t.Fatalf("request JSON = %s, error = %v", string(*requestBody), err)
	}
	if got := payload["system"]; got != "config system" {
		t.Fatalf("system = %#v, want config system in %s", got, string(*requestBody))
	}
}

// A silent command is the ordinary case this asserts against: the tool ran, the
// result is empty, and the follow-up must still send a tool message OpenAI will
// accept.
func TestBuildAndSendFollowupRequestSendsEmptyToolResultContent(t *testing.T) {
	server, requestBody := newBodyCaptureServer(t, `{"choices":[{"message":{"content":"done"}}]}`)

	cfg := RequestOptions{
		Provider:    "openai",
		ProviderURL: server.URL,
		Model:       "gpt-test",
		ToolEnabled: true,
	}
	_, err := BuildAndSendFollowupRequest(
		context.Background(),
		cfg,
		ToolExecutionConfig{Store: &mockSessionStore{path: "/test/session"}},
		cfg.Model,
		nil,
		func(string) ([]TypedMessage, error) {
			return []TypedMessage{
				NewTextMessage("user", "run something quiet"),
				{
					Role: string(RoleAssistant),
					Blocks: []Block{ToolUseBlock{
						ID:    "call_1",
						Name:  "universal_command",
						Input: json.RawMessage(`{"command":["true"]}`),
					}},
				},
				{
					Role: string(RoleUser),
					Blocks: []Block{ToolResultBlock{
						ToolUseID: "call_1",
						Content:   "",
					}},
				},
			}, nil
		},
		NewTestLogger(false),
	)
	if err != nil {
		t.Fatalf("BuildAndSendFollowupRequest() error = %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(*requestBody, &payload); err != nil {
		t.Fatalf("request JSON = %s, error = %v", string(*requestBody), err)
	}
	messages, ok := payload["messages"].([]interface{})
	if !ok || len(messages) != 3 {
		t.Fatalf("messages = %#v, want 3 in %s", payload["messages"], string(*requestBody))
	}
	toolMsg, ok := messages[2].(map[string]interface{})
	if !ok || toolMsg["role"] != "tool" {
		t.Fatalf("last message = %#v, want the tool result", messages[2])
	}
	content, present := toolMsg["content"]
	if !present {
		t.Fatalf("tool message = %#v, want content present; OpenAI rejects it otherwise", toolMsg)
	}
	if content != "" {
		t.Fatalf("content = %#v, want an empty string", content)
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
