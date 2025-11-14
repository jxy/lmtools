package session

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/format"
	"os"
	"testing"
	"time"
)

// TestRegenerationWithToolCalls tests that regenerating (branching from) assistant messages
// with tool calls properly preserves the tool context
func TestRegenerationWithToolCalls(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with tool interactions
		sess, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Message 0: User request
		userMsg := Message{
			Role:      "user",
			Content:   "List files and count lines",
			Timestamp: time.Now(),
		}
		result, err := AppendMessageWithToolInteraction(context.Background(), sess, userMsg, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append user message: %v", err)
		}
		if result.MessageID != "0000" {
			t.Errorf("Expected message ID 0000, got %s", result.MessageID)
		}

		// Message 1: Assistant response with tool calls
		assistantText := "I'll list the files and count the lines for you."
		toolCalls := []core.ToolCall{
			{
				ID:   "call-001",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["ls","-la"]}`),
			},
		}

		result, err = SaveAssistantResponseWithTools(context.Background(), sess, assistantText, toolCalls, "gpt5")
		if err != nil {
			t.Fatalf("Failed to save assistant response with tools: %v", err)
		}
		if result.MessageID != "0001" {
			t.Errorf("Expected message ID 0001, got %s", result.MessageID)
		}

		// Message 2: Tool results
		toolResults := []core.ToolResult{
			{
				ID:     "call-001",
				Output: "file1.txt\nfile2.txt\nfile3.txt",
			},
		}

		result, err = SaveToolResults(context.Background(), sess, toolResults, "")
		if err != nil {
			t.Fatalf("Failed to save tool results: %v", err)
		}
		if result.MessageID != "0002" {
			t.Errorf("Expected message ID 0002, got %s", result.MessageID)
		}

		// Message 3: Assistant continues with another tool call
		assistantText2 := "Found 3 files. Now counting lines:"
		toolCalls2 := []core.ToolCall{
			{
				ID:   "call-002",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["wc","-l","file1.txt","file2.txt","file3.txt"]}`),
			},
		}

		result, err = SaveAssistantResponseWithTools(context.Background(), sess, assistantText2, toolCalls2, "gpt5")
		if err != nil {
			t.Fatalf("Failed to save second assistant response: %v", err)
		}
		if result.MessageID != "0003" {
			t.Errorf("Expected message ID 0003, got %s", result.MessageID)
		}

		// Now test regeneration from message 0001 (first assistant message with tools)
		siblingPath, err := CreateSibling(context.Background(), sess.Path, "0001")
		if err != nil {
			t.Fatalf("Failed to create sibling for regeneration: %v", err)
		}

		// Load the sibling session
		siblingSession, err := LoadSession(siblingPath)
		if err != nil {
			t.Fatalf("Failed to load sibling session: %v", err)
		}

		// Build messages with tool interactions for the sibling
		messages, err := BuildMessagesWithToolInteractions(context.Background(), siblingSession.Path)
		if err != nil {
			t.Fatalf("Failed to build messages with tool interactions: %v", err)
		}

		// Check for duplicate messages
		seenMessages := make(map[string]int)
		for i, msg := range messages {
			key := fmt.Sprintf("%s_%d_blocks", msg.Role, len(msg.Blocks))
			if prevIdx, exists := seenMessages[key]; exists {
				t.Errorf("Duplicate message found at indices %d and %d: role=%s, blocks=%d",
					prevIdx, i, msg.Role, len(msg.Blocks))
			}
			seenMessages[key] = i
		}

		// Should only have the user message (0000) since we branched from 0001
		if len(messages) != 1 {
			t.Errorf("Expected 1 message after branching from 0001, got %d", len(messages))
		}

		if len(messages) > 0 && messages[0].Role != "user" {
			t.Errorf("Expected first message to be user role, got %s", messages[0].Role)
		}

		// Add a regenerated assistant response with different tool calls
		regenText := "Let me check the directory contents differently."
		regenToolCalls := []core.ToolCall{
			{
				ID:   "call-regen-001",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["find",".","-type","f"]}`),
			},
		}

		result, err = SaveAssistantResponseWithTools(context.Background(), siblingSession, regenText, regenToolCalls, "gpt5")
		if err != nil {
			t.Fatalf("Failed to save regenerated assistant response: %v", err)
		}

		// Verify the regenerated response preserved tool structure
		toolInteraction, err := LoadToolInteraction(siblingSession.Path, result.MessageID)
		if err != nil {
			t.Fatalf("Failed to load tool interaction: %v", err)
		}

		if len(toolInteraction.Calls) != 1 {
			t.Errorf("Expected 1 tool call in regenerated response, got %d", len(toolInteraction.Calls))
		}

		if len(toolInteraction.Calls) > 0 && toolInteraction.Calls[0].Name != "universal_command" {
			t.Errorf("Expected tool name 'universal_command', got '%s'", toolInteraction.Calls[0].Name)
		}
	})
}

// TestBranchingAtDifferentStagesWithTools tests branching from various points
// in a conversation that includes tool interactions
func TestBranchingAtDifferentStagesWithTools(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with a complex tool interaction flow
		sess, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Build a conversation with tools:
		// 0000: User asks question
		// 0001: Assistant responds with tool call
		// 0002: Tool results
		// 0003: Assistant final response
		// 0004: User follow-up
		// 0005: Assistant with another tool call

		// Message 0000
		_, err = AppendMessageWithToolInteraction(context.Background(), sess, Message{
			Role:      "user",
			Content:   "What's the weather?",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		// Message 0001
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"Let me check the weather for you.",
			[]core.ToolCall{{
				ID:   "weather-1",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["curl","weather.example.com"]}`),
			}},
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Message 0002
		_, err = SaveToolResults(context.Background(), sess,
			[]core.ToolResult{{
				ID:     "weather-1",
				Output: "Temperature: 72°F, Sunny",
			}},
			"",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Message 0003
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"The weather is currently 72°F and sunny.",
			nil, // No tool calls
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Message 0004
		_, err = AppendMessageWithToolInteraction(context.Background(), sess, Message{
			Role:      "user",
			Content:   "What about tomorrow?",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		// Message 0005
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"Let me check tomorrow's forecast.",
			[]core.ToolCall{{
				ID:   "weather-2",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["curl","weather.example.com/tomorrow"]}`),
			}},
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Test branching from different points
		testCases := []struct {
			branchFrom    string
			expectedCount int
			description   string
		}{
			{"0001", 1, "Branch from assistant with tool call - should only have user message"},
			{"0002", 2, "Branch from tool results - should have user and assistant with tool (not the tool results itself)"},
			{"0003", 3, "Branch from assistant after tools - should have complete first interaction"},
			{"0004", 4, "Branch from user follow-up - should have all messages up to and including previous assistant"},
			{"0005", 5, "Branch from second assistant with tools - should have all messages before it"},
		}

		for _, tc := range testCases {
			t.Run(tc.description, func(t *testing.T) {
				siblingPath, err := CreateSibling(context.Background(), sess.Path, tc.branchFrom)
				if err != nil {
					t.Fatalf("Failed to create sibling from %s: %v", tc.branchFrom, err)
				}

				siblingSession, err := LoadSession(siblingPath)
				if err != nil {
					t.Fatalf("Failed to load sibling session: %v", err)
				}

				messages, err := BuildMessagesWithToolInteractions(context.Background(), siblingSession.Path)
				if err != nil {
					t.Fatalf("Failed to build messages: %v", err)
				}

				if len(messages) != tc.expectedCount {
					t.Errorf("Branch from %s: expected %d messages, got %d",
						tc.branchFrom, tc.expectedCount, len(messages))
					for i, msg := range messages {
						t.Logf("  Message %d: Role=%s, %d blocks", i, msg.Role, len(msg.Blocks))
					}
				}
			})
		}
	})
}

// TestSessionRequestJSONWithTools verifies that the JSON structure of requests
// built from sessions with tool interactions is correct
func TestSessionRequestJSONWithTools(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with tool interactions
		sess, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Build a conversation
		_, err = AppendMessageWithToolInteraction(context.Background(), sess, Message{
			Role:      "user",
			Content:   "Calculate something",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"I'll calculate that for you.",
			[]core.ToolCall{{
				ID:   "calc-1",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["bc","-l"]}`),
			}},
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		_, err = SaveToolResults(context.Background(), sess,
			[]core.ToolResult{{
				ID:     "calc-1",
				Output: "42",
			}},
			"",
		)
		if err != nil {
			t.Fatal(err)
		}

		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"The answer is 42.",
			nil,
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Build typed messages
		typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), sess.Path)
		if err != nil {
			t.Fatalf("Failed to build typed messages: %v", err)
		}

		// Should have 4 typed messages
		if len(typedMessages) != 4 {
			t.Fatalf("Expected 4 typed messages, got %d", len(typedMessages))
		}

		// Verify message structure
		// Message 0: User text
		if typedMessages[0].Role != "user" || len(typedMessages[0].Blocks) != 1 {
			t.Errorf("Message 0: expected user role with 1 block")
		}

		// Message 1: Assistant with text + tool use
		if typedMessages[1].Role != "assistant" || len(typedMessages[1].Blocks) != 2 {
			t.Errorf("Message 1: expected assistant role with 2 blocks (text + tool use)")
		}

		// Message 2: User with tool result
		if typedMessages[2].Role != "user" || len(typedMessages[2].Blocks) != 1 {
			t.Errorf("Message 2: expected user role with 1 block (tool result)")
		}

		// Message 3: Assistant text only
		if typedMessages[3].Role != "assistant" || len(typedMessages[3].Blocks) != 1 {
			t.Errorf("Message 3: expected assistant role with 1 block (text)")
		}

		// Convert to provider format and verify JSON structure
		typedAnthMessages := core.ToAnthropicTyped(typedMessages)
		// Use the proper marshaling function that handles ContentUnion correctly
		anthropicMessages := core.MarshalAnthropicMessagesForRequest(typedAnthMessages)
		anthropicJSON, err := json.MarshalIndent(anthropicMessages, "", "  ")
		if err != nil {
			t.Fatalf("Failed to marshal Anthropic messages: %v", err)
		}

		// Verify the JSON contains expected structure
		var parsed []map[string]interface{}
		if err := json.Unmarshal(anthropicJSON, &parsed); err != nil {
			t.Fatalf("Failed to parse Anthropic JSON: %v", err)
		}

		// Check assistant message with tool use
		if len(parsed) > 1 {
			assistantMsg := parsed[1]
			content, ok := assistantMsg["content"].([]interface{})
			if !ok {
				t.Error("Assistant message content should be an array")
			} else if len(content) != 2 {
				t.Errorf("Assistant message should have 2 content blocks, got %d", len(content))
			} else {
				// Check for text block
				textBlock := content[0].(map[string]interface{})
				if textBlock["type"] != "text" {
					t.Error("First block should be text type")
				}

				// Check for tool use block
				toolBlock := content[1].(map[string]interface{})
				if toolBlock["type"] != "tool_use" {
					t.Error("Second block should be tool_use type")
				}
				if toolBlock["id"] != "calc-1" {
					t.Errorf("Tool use block should have id 'calc-1', got %v", toolBlock["id"])
				}
			}
		}
	})
}

// TestResumeSessionWithPendingTools tests resuming a session that has
// pending tool calls at various stages
func TestResumeSessionWithPendingTools(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create sessions with pending tools at different stages
		testCases := []struct {
			name        string
			setupFunc   func(*Session) error
			expectTools int
		}{
			{
				name: "Pending after first assistant response",
				setupFunc: func(sess *Session) error {
					// User message
					_, err := AppendMessageWithToolInteraction(context.Background(), sess, Message{
						Role:      "user",
						Content:   "List files",
						Timestamp: time.Now(),
					}, nil, nil)
					if err != nil {
						return err
					}

					// Assistant with tool call (no results yet)
					_, err = SaveAssistantResponseWithTools(context.Background(), sess,
						"I'll list the files:",
						[]core.ToolCall{{
							ID:   "list-1",
							Name: "universal_command",
							Args: json.RawMessage(`{"command":["ls"]}`),
						}},
						"gpt5",
					)
					return err
				},
				expectTools: 1,
			},
			{
				name: "Pending after tool results with continuation",
				setupFunc: func(sess *Session) error {
					// User message
					_, err := AppendMessageWithToolInteraction(context.Background(), sess, Message{
						Role:      "user",
						Content:   "Count files and show sizes",
						Timestamp: time.Now(),
					}, nil, nil)
					if err != nil {
						return err
					}

					// Assistant with first tool call
					_, err = SaveAssistantResponseWithTools(context.Background(), sess,
						"I'll count the files first:",
						[]core.ToolCall{{
							ID:   "count-1",
							Name: "universal_command",
							Args: json.RawMessage(`{"command":["ls","-1","|","wc","-l"]}`),
						}},
						"gpt5",
					)
					if err != nil {
						return err
					}

					// Tool results
					_, err = SaveToolResults(context.Background(), sess,
						[]core.ToolResult{{
							ID:     "count-1",
							Output: "5",
						}},
						"",
					)
					if err != nil {
						return err
					}

					// Assistant continues with another tool call
					_, err = SaveAssistantResponseWithTools(context.Background(), sess,
						"Found 5 files. Now checking sizes:",
						[]core.ToolCall{{
							ID:   "size-1",
							Name: "universal_command",
							Args: json.RawMessage(`{"command":["du","-h"]}`),
						}},
						"gpt5",
					)
					return err
				},
				expectTools: 1,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				sess, err := CreateSession("", core.NewTestLogger(false))
				if err != nil {
					t.Fatalf("Failed to create session: %v", err)
				}

				if err := tc.setupFunc(sess); err != nil {
					t.Fatalf("Setup failed: %v", err)
				}

				// Check for pending tools
				pending, err := CheckForPendingToolCalls(context.Background(), sess.Path)
				if err != nil {
					t.Fatalf("Failed to check pending tools: %v", err)
				}

				if len(pending) != tc.expectTools {
					t.Errorf("Expected %d pending tools, got %d", tc.expectTools, len(pending))
				}

				// Verify the pending tools have correct structure
				for i, tool := range pending {
					if tool.ID == "" {
						t.Errorf("Pending tool %d has empty ID", i)
					}
					if tool.Name != "universal_command" {
						t.Errorf("Pending tool %d: expected name 'universal_command', got '%s'", i, tool.Name)
					}
					if len(tool.Args) == 0 {
						t.Errorf("Pending tool %d has empty args", i)
					}
				}
			})
		}
	})
}

// TestMultipleRoundsOfToolCalls tests creating and recreating sessions with multiple
// complete rounds of tool interactions (assistant→tool→result→assistant→tool→result...)
func TestMultipleRoundsOfToolCalls(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create a session with multiple complete rounds of tool interactions
		sess, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Round 1: User asks to analyze a project
		_, err = AppendMessageWithToolInteraction(context.Background(), sess, Message{
			Role:      "user",
			Content:   "Analyze this project structure and count the files",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		// Assistant calls first tool
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"I'll analyze the project structure for you. Let me start by listing the directories.",
			[]core.ToolCall{{
				ID:   "list-dirs",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["find",".","-type","d","-maxdepth","2"]}`),
			}},
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Tool result 1
		_, err = SaveToolResults(context.Background(), sess,
			[]core.ToolResult{{
				ID:     "list-dirs",
				Output: "./\n./src\n./tests\n./docs\n./build",
			}},
			"",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Assistant continues with another tool call
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"I found 5 directories. Now let me count the files in each directory.",
			[]core.ToolCall{{
				ID:   "count-src",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["find","./src","-type","f","|","wc","-l"]}`),
			}},
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Tool result 2
		_, err = SaveToolResults(context.Background(), sess,
			[]core.ToolResult{{
				ID:     "count-src",
				Output: "42",
			}},
			"",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Assistant makes multiple tool calls at once
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"Found 42 files in src. Let me check the other directories.",
			[]core.ToolCall{
				{
					ID:   "count-tests",
					Name: "universal_command",
					Args: json.RawMessage(`{"command":["find","./tests","-type","f","|","wc","-l"]}`),
				},
				{
					ID:   "count-docs",
					Name: "universal_command",
					Args: json.RawMessage(`{"command":["find","./docs","-type","f","|","wc","-l"]}`),
				},
			},
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Multiple tool results
		_, err = SaveToolResults(context.Background(), sess,
			[]core.ToolResult{
				{
					ID:     "count-tests",
					Output: "28",
				},
				{
					ID:     "count-docs",
					Output: "15",
				},
			},
			"",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Assistant provides summary
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"Here's the complete analysis:\n- src: 42 files\n- tests: 28 files\n- docs: 15 files\n- Total: 85 files",
			nil, // No more tool calls
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Round 2: User asks follow-up question
		_, err = AppendMessageWithToolInteraction(context.Background(), sess, Message{
			Role:      "user",
			Content:   "What are the most recent files?",
			Timestamp: time.Now(),
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}

		// Assistant calls another tool
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"I'll find the most recently modified files for you.",
			[]core.ToolCall{{
				ID:   "recent-files",
				Name: "universal_command",
				Args: json.RawMessage(`{"command":["find",".","-type","f","-mtime","-1","-ls"]}`),
			}},
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Tool result
		_, err = SaveToolResults(context.Background(), sess,
			[]core.ToolResult{{
				ID:     "recent-files",
				Output: "./src/main.go\n./tests/test_new.go\n./docs/README.md",
			}},
			"",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Final assistant response
		_, err = SaveAssistantResponseWithTools(context.Background(), sess,
			"The most recently modified files (within the last 24 hours) are:\n1. ./src/main.go\n2. ./tests/test_new.go\n3. ./docs/README.md",
			nil,
			"gpt5",
		)
		if err != nil {
			t.Fatal(err)
		}

		// Verify the complete conversation structure
		messages, err := BuildMessagesWithToolInteractions(context.Background(), sess.Path)
		if err != nil {
			t.Fatalf("Failed to build messages: %v", err)
		}

		// Expected structure:
		// 0: user - initial request
		// 1: assistant - with tool call (list-dirs)
		// 2: user - tool result
		// 3: assistant - with tool call (count-src)
		// 4: user - tool result
		// 5: assistant - with 2 tool calls (count-tests, count-docs)
		// 6: user - 2 tool results
		// 7: assistant - summary (no tools)
		// 8: user - follow-up question
		// 9: assistant - with tool call (recent-files)
		// 10: user - tool result
		// 11: assistant - final response (no tools)

		expectedCount := 12
		if len(messages) != expectedCount {
			t.Errorf("Expected %d messages, got %d", expectedCount, len(messages))
			for i, msg := range messages {
				t.Logf("Message %d: Role=%s, Blocks=%d", i, msg.Role, len(msg.Blocks))
			}
		}

		// Test regeneration at various points
		testCases := []struct {
			branchFrom    string
			expectedCount int
			description   string
		}{
			{"0001", 1, "Branch from first assistant with tool - should have initial user message"},
			{"0003", 3, "Branch from second assistant with tool - should have user, assistant+tool, tool result"},
			{"0005", 5, "Branch from assistant with multiple tools - should have conversation up to that point"},
			{"0007", 7, "Branch from assistant summary - should have complete first round"},
			{"0009", 9, "Branch from assistant in second round - should have everything except last tool interaction"},
		}

		for _, tc := range testCases {
			t.Run(tc.description, func(t *testing.T) {
				siblingPath, err := CreateSibling(context.Background(), sess.Path, tc.branchFrom)
				if err != nil {
					t.Fatalf("Failed to create sibling from %s: %v", tc.branchFrom, err)
				}

				siblingSession, err := LoadSession(siblingPath)
				if err != nil {
					t.Fatalf("Failed to load sibling session: %v", err)
				}

				messages, err := BuildMessagesWithToolInteractions(context.Background(), siblingSession.Path)
				if err != nil {
					t.Fatalf("Failed to build messages: %v", err)
				}

				if len(messages) != tc.expectedCount {
					t.Errorf("Branch from %s: expected %d messages, got %d",
						tc.branchFrom, tc.expectedCount, len(messages))
				}

				// Add a regenerated response to verify it works
				if tc.branchFrom == "0005" {
					// Debug: Check what messages exist in the sibling before adding new ones
					t.Logf("Before regeneration in sibling %s:", siblingSession.Path)
					siblingMsgs, _ := GetLineage(siblingSession.Path)
					for i, m := range siblingMsgs {
						if i >= 7 {
							break
						}
						t.Logf("  Sibling msg %d: ID=%s, Role=%s", i, m.ID, m.Role)
					}

					// Regenerate the assistant response with different tool calls
					result, err := SaveAssistantResponseWithTools(context.Background(), siblingSession,
						"I'll check all directories at once for efficiency.",
						[]core.ToolCall{{
							ID:   "count-all",
							Name: "universal_command",
							Args: json.RawMessage(`{"command":["find",".","-type","f","|","wc","-l"]}`),
						}},
						"gpt5",
					)
					if err != nil {
						t.Fatalf("Failed to save regenerated response: %v", err)
					}
					t.Logf("Saved regenerated response with ID: %s", result.MessageID)

					// Debug: Check what messages exist after regeneration
					t.Logf("After regeneration:")
					siblingMsgs2, _ := GetLineage(siblingSession.Path)
					for i, m := range siblingMsgs2 {
						if i >= 7 {
							break
						}
						t.Logf("  Sibling msg %d: ID=%s, Role=%s", i, m.ID, m.Role)
					}

					// Check what files were created
					files, _ := os.ReadDir(siblingSession.Path)
					t.Logf("Files in sibling after regeneration:")
					for _, f := range files {
						if f.Name() == result.MessageID+".tools.json" || f.Name() == "0000.tools.json" {
							t.Logf("  %s", f.Name())
						}
					}

					// Verify the regenerated branch
					regenMessages, err := BuildMessagesWithToolInteractions(context.Background(), siblingSession.Path)
					if err != nil {
						t.Fatalf("Failed to build regenerated messages: %v", err)
					}

					if len(regenMessages) != tc.expectedCount+1 {
						t.Errorf("After regeneration: expected %d messages, got %d",
							tc.expectedCount+1, len(regenMessages))
					}

					// Verify the last message has the new tool call
					lastMsg := regenMessages[len(regenMessages)-1]
					if lastMsg.Role != "assistant" || len(lastMsg.Blocks) != 2 {
						t.Error("Regenerated message should be assistant with text and tool call")
					}
				}
			})
		}

		// Test that forking preserves all tool interactions
		forkedSession, err := ForkSessionWithSystemMessage(context.Background(), sess.Path, nil)
		if err != nil {
			t.Fatalf("Failed to fork session: %v", err)
		}

		// Debug: Check what files exist in the forked session
		t.Logf("Forked session path: %s", forkedSession.Path)
		forkedFiles, _ := os.ReadDir(forkedSession.Path)
		for _, f := range forkedFiles {
			t.Logf("  Forked file: %s", f.Name())
		}

		// Debug: Check the content of 0005.tools.json in both original and forked
		origTools, err := LoadToolInteraction(sess.Path, "0005")
		if err == nil && origTools != nil {
			t.Logf("Original 0005.tools.json: %d calls, %d results", len(origTools.Calls), len(origTools.Results))
			for i, call := range origTools.Calls {
				t.Logf("  Call %d: ID=%s, Name=%s", i, call.ID, call.Name)
			}
		}
		forkedTools, err := LoadToolInteraction(forkedSession.Path, "0005")
		if err == nil && forkedTools != nil {
			t.Logf("Forked 0005.tools.json: %d calls, %d results", len(forkedTools.Calls), len(forkedTools.Results))
			for i, call := range forkedTools.Calls {
				t.Logf("  Call %d: ID=%s, Name=%s", i, call.ID, call.Name)
			}
		}

		// Debug: Check the content of forked messages and their tool interactions
		forkedLineage, _ := GetLineage(forkedSession.Path)
		for i, msg := range forkedLineage {
			if i >= 3 {
				break // Only check first 3 messages
			}
			ti, _ := LoadToolInteraction(forkedSession.Path, msg.ID)
			hasTools := ti != nil && (len(ti.Calls) > 0 || len(ti.Results) > 0)
			t.Logf("Forked message %s: Role=%s, HasTools=%v", msg.ID, msg.Role, hasTools)
			if hasTools {
				t.Logf("  - Calls: %d, Results: %d", len(ti.Calls), len(ti.Results))
			}
		}

		forkedMessages, err := BuildMessagesWithToolInteractions(context.Background(), forkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to build forked messages: %v", err)
		}

		// Original session has a system message that gets skipped during forking
		// So we need to compare forked messages with original messages minus the system message
		originalMessagesNoSystem := messages
		if len(messages) > 0 && messages[0].Role == string(core.RoleSystem) {
			originalMessagesNoSystem = messages[1:]
		}

		// Debug: Log the messages to understand the mismatch
		t.Logf("Original messages count: %d", len(messages))
		t.Logf("Forked messages count: %d", len(forkedMessages))

		// Log first few messages from each to see the actual content
		for i := 0; i < 7 && i < len(messages); i++ {
			content := ""
			blockTypes := []string{}
			for _, block := range messages[i].Blocks {
				switch b := block.(type) {
				case core.TextBlock:
					blockTypes = append(blockTypes, "TextBlock")
					if content == "" {
						content = b.Text
						content = format.Truncate(content, 50)
					}
				case core.ToolUseBlock:
					blockTypes = append(blockTypes, fmt.Sprintf("ToolUseBlock(%s)", b.Name))
				case core.ToolResultBlock:
					blockTypes = append(blockTypes, fmt.Sprintf("ToolResultBlock(%s)", b.ToolUseID))
				default:
					blockTypes = append(blockTypes, fmt.Sprintf("Unknown(%T)", b))
				}
			}
			t.Logf("Original[%d]: role=%s, blocks=%d %v, content=%q", i, messages[i].Role, len(messages[i].Blocks), blockTypes, content)
		}
		for i := 0; i < 7 && i < len(forkedMessages); i++ {
			content := ""
			blockTypes := []string{}
			for _, block := range forkedMessages[i].Blocks {
				switch b := block.(type) {
				case core.TextBlock:
					blockTypes = append(blockTypes, "TextBlock")
					if content == "" {
						content = b.Text
						content = format.Truncate(content, 50)
					}
				case core.ToolUseBlock:
					blockTypes = append(blockTypes, fmt.Sprintf("ToolUseBlock(%s)", b.Name))
				case core.ToolResultBlock:
					blockTypes = append(blockTypes, fmt.Sprintf("ToolResultBlock(%s)", b.ToolUseID))
				default:
					blockTypes = append(blockTypes, fmt.Sprintf("Unknown(%T)", b))
				}
			}
			t.Logf("Forked[%d]: role=%s, blocks=%d %v, content=%q", i, forkedMessages[i].Role, len(forkedMessages[i].Blocks), blockTypes, content)
		}

		// Forked session should have all messages except system message
		if len(forkedMessages) != len(originalMessagesNoSystem) {
			t.Errorf("Forked session: expected %d messages, got %d", len(originalMessagesNoSystem), len(forkedMessages))
		}

		// Verify each message type in the forked session
		for i, msg := range forkedMessages {
			if i >= len(originalMessagesNoSystem) {
				t.Errorf("Message %d: exists in forked but not in original", i)
				continue
			}
			originalMsg := originalMessagesNoSystem[i]
			if msg.Role != originalMsg.Role {
				t.Errorf("Message %d: role mismatch - forked=%s, original=%s", i, msg.Role, originalMsg.Role)
			}
			if len(msg.Blocks) != len(originalMsg.Blocks) {
				t.Errorf("Message %d: block count mismatch - forked=%d, original=%d",
					i, len(msg.Blocks), len(originalMsg.Blocks))
			}
		}
	})
}
