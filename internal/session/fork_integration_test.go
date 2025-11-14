package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForkSessionWithDifferentSystemMessage(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create original session with system message
		originalSystemMsg := "You are a helpful assistant."
		originalSession, err := CreateSession(originalSystemMsg, core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create original session: %v", err)
		}

		// Add some messages to the original session
		messages := []Message{
			{Role: "user", Content: "What is 2+2?", Timestamp: time.Now()},
			{Role: "assistant", Content: "2+2 equals 4.", Timestamp: time.Now(), Model: "test-model"},
			{Role: "user", Content: "What about 3+3?", Timestamp: time.Now()},
			{Role: "assistant", Content: "3+3 equals 6.", Timestamp: time.Now(), Model: "test-model"},
		}

		for _, msg := range messages {
			if _, err := AppendMessageWithToolInteraction(context.Background(), originalSession, msg, nil, nil); err != nil {
				t.Fatalf("Failed to append message: %v", err)
			}
		}

		// Test 1: Fork with different system message
		newSystemMsg := "You are an expert mathematician."
		forkedSession, err := ForkSessionWithSystemMessage(context.Background(), originalSession.Path, &newSystemMsg)
		if err != nil {
			t.Fatalf("Failed to fork session: %v", err)
		}

		// Verify new session has the new system message
		savedSystemMsg, err := GetSystemMessage(forkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to get system message from forked session: %v", err)
		}
		if savedSystemMsg == nil || *savedSystemMsg != newSystemMsg {
			t.Errorf("Expected system message %q, got %v", newSystemMsg, savedSystemMsg)
		}

		// Verify all non-system messages were copied
		forkedLineage, err := GetLineage(forkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to get forked session lineage: %v", err)
		}

		// Should have system message + 4 copied messages
		if len(forkedLineage) != 5 {
			t.Errorf("Expected 5 messages in forked session, got %d", len(forkedLineage))
		}

		// Verify first message is the new system message
		if forkedLineage[0].Role != "system" || forkedLineage[0].Content != newSystemMsg {
			t.Errorf("First message should be new system message, got role=%q content=%q",
				forkedLineage[0].Role, forkedLineage[0].Content)
		}

		// Verify other messages match original (except for IDs)
		for i, msg := range messages {
			forkedMsg := forkedLineage[i+1] // +1 to skip system message
			if forkedMsg.Role != msg.Role || forkedMsg.Content != msg.Content {
				t.Errorf("Message %d mismatch: expected role=%q content=%q, got role=%q content=%q",
					i, msg.Role, msg.Content, forkedMsg.Role, forkedMsg.Content)
			}
		}

		// Test 2: Fork with empty system message (nil)
		emptyForkedSession, err := ForkSessionWithSystemMessage(context.Background(), originalSession.Path, nil)
		if err != nil {
			t.Fatalf("Failed to fork session with empty system message: %v", err)
		}

		// Verify no system message in the empty fork
		emptySystemMsg, err := GetSystemMessage(emptyForkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to get system message from empty fork: %v", err)
		}
		if emptySystemMsg != nil {
			t.Errorf("Expected no system message in empty fork, got %q", *emptySystemMsg)
		}

		// Verify lineage has no system message
		emptyLineage, err := GetLineage(emptyForkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to get empty fork lineage: %v", err)
		}

		// Should have only the 4 copied messages (no system message)
		if len(emptyLineage) != 4 {
			t.Errorf("Expected 4 messages in empty fork, got %d", len(emptyLineage))
		}

		// Verify first message is a user message (not system)
		if emptyLineage[0].Role != "user" {
			t.Errorf("First message in empty fork should be user, got %q", emptyLineage[0].Role)
		}
	})
}

func TestForkSessionWithToolInteractions(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create original session
		originalSession, err := CreateSession("You are a helpful assistant.", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create original session: %v", err)
		}

		// Add user message
		userMsg := Message{
			Role:      "user",
			Content:   "List the files in the current directory.",
			Timestamp: time.Now(),
		}
		if _, err := AppendMessageWithToolInteraction(context.Background(), originalSession, userMsg, nil, nil); err != nil {
			t.Fatalf("Failed to append user message: %v", err)
		}

		// Add assistant response with tool call
		toolCallArgs, _ := json.Marshal(map[string]interface{}{
			"command": []string{"ls", "-la"},
		})
		toolCalls := []core.ToolCall{
			{
				ID:   "call_123",
				Name: "universal_command",
				Args: toolCallArgs,
			},
		}
		result, err := SaveAssistantResponseWithTools(
			context.Background(),
			originalSession,
			"I'll list the files for you.",
			toolCalls,
			"test-model",
		)
		if err != nil {
			t.Fatalf("Failed to save assistant response with tools: %v", err)
		}

		// Verify tool file was created
		toolPath := filepath.Join(result.Path, result.MessageID+".tools.json")
		if _, err := os.Stat(toolPath); os.IsNotExist(err) {
			t.Errorf("Tool file not created: %s", toolPath)
		}

		// Add tool results
		toolResults := []core.ToolResult{
			{
				ID:     "call_123",
				Output: "file1.txt\nfile2.go\ndir1/",
			},
		}
		if _, err := SaveToolResults(context.Background(), originalSession, toolResults, ""); err != nil {
			t.Fatalf("Failed to save tool results: %v", err)
		}

		// Fork the session with a different system message
		newSystemMsg := "You are a file system expert."
		forkedSession, err := ForkSessionWithSystemMessage(context.Background(), originalSession.Path, &newSystemMsg)
		if err != nil {
			t.Fatalf("Failed to fork session with tools: %v", err)
		}

		// Get messages with tool interactions from forked session
		typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), forkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to build messages with tool interactions: %v", err)
		}

		// Debug: Check what typed messages we got
		t.Logf("Got %d typed messages", len(typedMessages))
		for i, msg := range typedMessages {
			t.Logf("TypedMessage %d: role=%s, blocks=%d", i, msg.Role, len(msg.Blocks))
			for j, block := range msg.Blocks {
				switch b := block.(type) {
				case core.TextBlock:
					t.Logf("  Block %d: TextBlock, text=%s", j, b.Text)
				case core.ToolUseBlock:
					t.Logf("  Block %d: ToolUseBlock, id=%s, name=%s", j, b.ID, b.Name)
				case core.ToolResultBlock:
					t.Logf("  Block %d: ToolResultBlock, tool_use_id=%s", j, b.ToolUseID)
				}
			}
		}

		// Should have: system, user, assistant (with tool), user (with tool results)
		if len(typedMessages) < 4 {
			t.Errorf("Expected at least 4 messages with tool interactions, got %d", len(typedMessages))
		}

		// Convert to untyped format for testing
		typedAnthMessages := core.ToAnthropicTyped(typedMessages)
		messages := core.MarshalAnthropicMessagesForRequest(typedAnthMessages)

		// Debug: Log the converted messages
		for i, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				t.Logf("Converted message %d: %+v", i, m)
			}
		}

		// Verify tool calls are preserved
		foundToolCall := false
		foundToolResult := false
		for i, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				t.Logf("Message %d: role=%v", i, m["role"])
				if content, ok := m["content"].([]interface{}); ok {
					t.Logf("  Content array with %d blocks", len(content))
					for j, block := range content {
						if b, ok := block.(map[string]interface{}); ok {
							t.Logf("    Block %d: type=%v", j, b["type"])
							switch b["type"] {
							case "tool_use":
								foundToolCall = true
								// Verify tool call details
								if b["name"] != "universal_command" {
									t.Errorf("Expected tool name 'universal_command', got %v", b["name"])
								}
							case "tool_result":
								foundToolResult = true
								// Verify tool result details
								if b["tool_use_id"] != "call_123" {
									t.Errorf("Expected tool_use_id 'call_123', got %v", b["tool_use_id"])
								}
							}
						}
					}
				} else if content, ok := m["content"].(string); ok {
					t.Logf("  Content string: %s", content)
				} else if contentSlice, ok := m["content"].([]core.AnthropicContent); ok {
					// Handle case where content is []core.AnthropicContent (typed)
					t.Logf("  Content array (AnthropicContent) with %d blocks", len(contentSlice))
					for j, b := range contentSlice {
						t.Logf("    Block %d: type=%v", j, b.Type)
						switch b.Type {
						case "tool_use":
							foundToolCall = true
							// Verify tool call details
							if b.Name != "universal_command" {
								t.Errorf("Expected tool name 'universal_command', got %v", b.Name)
							}
						case "tool_result":
							foundToolResult = true
							// Verify tool result details
							if b.ToolUseID != "call_123" {
								t.Errorf("Expected tool_use_id 'call_123', got %v", b.ToolUseID)
							}
						}
					}
				} else if contentSlice, ok := m["content"].([]map[string]interface{}); ok {
					// Handle case where content is []map[string]interface{} instead of []interface{}
					t.Logf("  Content array (typed) with %d blocks", len(contentSlice))
					for j, b := range contentSlice {
						t.Logf("    Block %d: type=%v", j, b["type"])
						switch b["type"] {
						case "tool_use":
							foundToolCall = true
							// Verify tool call details
							if b["name"] != "universal_command" {
								t.Errorf("Expected tool name 'universal_command', got %v", b["name"])
							}
						case "tool_result":
							foundToolResult = true
							// Verify tool result details
							if b["tool_use_id"] != "call_123" {
								t.Errorf("Expected tool_use_id 'call_123', got %v", b["tool_use_id"])
							}
						}
					}
				}
			}
		}

		if !foundToolCall {
			t.Error("Tool call not found in forked session messages")
		}
		if !foundToolResult {
			t.Error("Tool result not found in forked session messages")
		}
	})
}

func TestSystemMessagePriority(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		testCases := []struct {
			name                 string
			sessionSystemMsg     string
			commandLineSystemMsg string
			explicitlySet        bool
			expectedSystemMsg    string
			expectedFork         bool
		}{
			{
				name:                 "No command line flag, use session's system message",
				sessionSystemMsg:     "Original system message",
				commandLineSystemMsg: "",
				explicitlySet:        false,
				expectedSystemMsg:    "Original system message",
				expectedFork:         false,
			},
			{
				name:                 "Command line flag differs, should fork",
				sessionSystemMsg:     "Original system message",
				commandLineSystemMsg: "New system message",
				explicitlySet:        true,
				expectedSystemMsg:    "New system message",
				expectedFork:         true,
			},
			{
				name:                 "Command line flag same as session, no fork",
				sessionSystemMsg:     "Same system message",
				commandLineSystemMsg: "Same system message",
				explicitlySet:        true,
				expectedSystemMsg:    "Same system message",
				expectedFork:         false,
			},
			{
				name:                 "Explicitly empty command line, should fork with no system",
				sessionSystemMsg:     "Original system message",
				commandLineSystemMsg: "",
				explicitlySet:        true,
				expectedSystemMsg:    "",
				expectedFork:         true,
			},
			{
				name:                 "No session system message, command line sets one",
				sessionSystemMsg:     "",
				commandLineSystemMsg: "New system message",
				explicitlySet:        true,
				expectedSystemMsg:    "New system message",
				expectedFork:         true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Create original session
				originalSession, err := CreateSession(tc.sessionSystemMsg, core.NewTestLogger(false))
				if err != nil {
					t.Fatalf("Failed to create session: %v", err)
				}

				// Add a message
				msg := Message{
					Role:      "user",
					Content:   "Test message",
					Timestamp: time.Now(),
				}
				if _, err := AppendMessageWithToolInteraction(context.Background(), originalSession, msg, nil, nil); err != nil {
					t.Fatalf("Failed to append message: %v", err)
				}

				// Simulate the forking logic
				var resultSession *Session
				if tc.explicitlySet {
					originalSystemMsg, _ := GetSystemMessage(originalSession.Path)
					needFork := false

					if originalSystemMsg == nil && tc.commandLineSystemMsg != "" {
						needFork = true
					} else if originalSystemMsg != nil && *originalSystemMsg != tc.commandLineSystemMsg {
						needFork = true
					}

					if needFork {
						var newSystemPrompt *string
						if tc.commandLineSystemMsg != "" {
							newSystemPrompt = &tc.commandLineSystemMsg
						}
						resultSession, err = ForkSessionWithSystemMessage(context.Background(), originalSession.Path, newSystemPrompt)
						if err != nil {
							t.Fatalf("Failed to fork session: %v", err)
						}
					} else {
						resultSession = originalSession
					}
				} else {
					resultSession = originalSession
				}

				// Verify the result
				actualSystemMsg, err := GetSystemMessage(resultSession.Path)
				if err != nil {
					t.Fatalf("Failed to get system message: %v", err)
				}

				// Check system message
				if tc.expectedSystemMsg == "" {
					if actualSystemMsg != nil {
						t.Errorf("Expected no system message, got %q", *actualSystemMsg)
					}
				} else {
					if actualSystemMsg == nil || *actualSystemMsg != tc.expectedSystemMsg {
						var got string
						if actualSystemMsg != nil {
							got = *actualSystemMsg
						}
						t.Errorf("Expected system message %q, got %q", tc.expectedSystemMsg, got)
					}
				}

				// Check if fork happened
				didFork := resultSession.Path != originalSession.Path
				if didFork != tc.expectedFork {
					t.Errorf("Expected fork=%v, got fork=%v", tc.expectedFork, didFork)
				}
			})
		}
	})
}

func TestEmptySystemMessageHandling(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Test creating session with explicitly empty system message
		emptySession, err := CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session with empty system message: %v", err)
		}

		// Verify no system message files were created
		systemTxtPath := filepath.Join(emptySession.Path, "0000.txt")
		systemJsonPath := filepath.Join(emptySession.Path, "0000.json")

		if _, err := os.Stat(systemTxtPath); !os.IsNotExist(err) {
			t.Errorf("System text file should not exist for empty system message")
		}
		if _, err := os.Stat(systemJsonPath); !os.IsNotExist(err) {
			t.Errorf("System JSON file should not exist for empty system message")
		}

		// Add a user message
		userMsg := Message{
			Role:      "user",
			Content:   "Hello",
			Timestamp: time.Now(),
		}
		if _, err := AppendMessageWithToolInteraction(context.Background(), emptySession, userMsg, nil, nil); err != nil {
			t.Fatalf("Failed to append user message: %v", err)
		}

		// Get lineage and verify no system message
		lineage, err := GetLineage(emptySession.Path)
		if err != nil {
			t.Fatalf("Failed to get lineage: %v", err)
		}

		if len(lineage) != 1 {
			t.Errorf("Expected 1 message (user), got %d", len(lineage))
		}

		if lineage[0].Role != "user" {
			t.Errorf("Expected first message to be user, got %q", lineage[0].Role)
		}
	})
}

func TestToolMessageReconstructionInSession(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create session
		session, err := CreateSession("You are a helpful assistant.", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create session: %v", err)
		}

		// Simulate multiple rounds of tool interactions
		// Round 1: User asks, assistant calls tool
		userMsg1 := Message{
			Role:      "user",
			Content:   "What's the current directory?",
			Timestamp: time.Now(),
		}
		if _, err := AppendMessageWithToolInteraction(context.Background(), session, userMsg1, nil, nil); err != nil {
			t.Fatalf("Failed to append user message 1: %v", err)
		}

		// Assistant responds with tool call
		toolCallArgs1, _ := json.Marshal(map[string]interface{}{
			"command": []string{"pwd"},
		})
		toolCalls1 := []core.ToolCall{
			{
				ID:   "call_001",
				Name: "universal_command",
				Args: toolCallArgs1,
			},
		}
		if _, err := SaveAssistantResponseWithTools(context.Background(), session, "Let me check the current directory.", toolCalls1, "test-model"); err != nil {
			t.Fatalf("Failed to save assistant response 1: %v", err)
		}

		// Tool result
		toolResults1 := []core.ToolResult{
			{
				ID:     "call_001",
				Output: "/home/user/project",
			},
		}
		if _, err := SaveToolResults(context.Background(), session, toolResults1, ""); err != nil {
			t.Fatalf("Failed to save tool results 1: %v", err)
		}

		// Assistant responds after tool
		assistantMsg1 := Message{
			Role:      "assistant",
			Content:   "You're in /home/user/project",
			Timestamp: time.Now(),
			Model:     "test-model",
		}
		if _, err := AppendMessageWithToolInteraction(context.Background(), session, assistantMsg1, nil, nil); err != nil {
			t.Fatalf("Failed to append assistant message 1: %v", err)
		}

		// Round 2: Another tool interaction
		userMsg2 := Message{
			Role:      "user",
			Content:   "List the files",
			Timestamp: time.Now(),
		}
		if _, err := AppendMessageWithToolInteraction(context.Background(), session, userMsg2, nil, nil); err != nil {
			t.Fatalf("Failed to append user message 2: %v", err)
		}

		toolCallArgs2, _ := json.Marshal(map[string]interface{}{
			"command": []string{"ls"},
		})
		toolCalls2 := []core.ToolCall{
			{
				ID:   "call_002",
				Name: "universal_command",
				Args: toolCallArgs2,
			},
		}
		if _, err := SaveAssistantResponseWithTools(context.Background(), session, "", toolCalls2, "test-model"); err != nil {
			t.Fatalf("Failed to save assistant response 2: %v", err)
		}

		toolResults2 := []core.ToolResult{
			{
				ID:     "call_002",
				Output: "file1.txt\nfile2.go\nREADME.md",
			},
		}
		if _, err := SaveToolResults(context.Background(), session, toolResults2, ""); err != nil {
			t.Fatalf("Failed to save tool results 2: %v", err)
		}

		// Build messages with tool interactions
		typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), session.Path)
		if err != nil {
			t.Fatalf("Failed to build messages with tool interactions: %v", err)
		}

		// Convert to untyped format for testing
		typedAnthMessages := core.ToAnthropicTyped(typedMessages)
		messages := core.MarshalAnthropicMessagesForRequest(typedAnthMessages)

		// Verify all tool calls and results are present
		toolCallCount := 0
		toolResultCount := 0
		emptyContentCount := 0

		for _, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				// Check for empty content (the issue we're fixing)
				if content, ok := m["content"].(string); ok && content == "" {
					emptyContentCount++
				}

				// Count tool interactions
				if blocks, ok := m["content"].([]interface{}); ok {
					for _, block := range blocks {
						if b, ok := block.(map[string]interface{}); ok {
							switch b["type"] {
							case "tool_use":
								toolCallCount++
							case "tool_result":
								toolResultCount++
							}
						}
					}
				} else if blocks, ok := m["content"].([]core.AnthropicContent); ok {
					// Handle case where content is []core.AnthropicContent (typed)
					for _, b := range blocks {
						switch b.Type {
						case "tool_use":
							toolCallCount++
						case "tool_result":
							toolResultCount++
						}
					}
				} else if blocks, ok := m["content"].([]map[string]interface{}); ok {
					// Handle case where content is []map[string]interface{} instead of []interface{}
					for _, b := range blocks {
						switch b["type"] {
						case "tool_use":
							toolCallCount++
						case "tool_result":
							toolResultCount++
						}
					}
				}
			}
		}

		// Verify no empty content (the main issue)
		if emptyContentCount > 0 {
			t.Errorf("Found %d messages with empty content - tool interactions not properly reconstructed", emptyContentCount)
		}

		// Verify tool calls are present
		if toolCallCount != 2 {
			t.Errorf("Expected 2 tool calls, got %d", toolCallCount)
		}

		// Verify tool results are present
		if toolResultCount != 2 {
			t.Errorf("Expected 2 tool results, got %d", toolResultCount)
		}
	})
}

func TestForkSessionWithIDCollisions(t *testing.T) {
	WithTestSessionDir(t, func(sessionsDir string) {
		// Create original session with system message
		originalSession, err := CreateSession("You are a helpful assistant.", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("Failed to create original session: %v", err)
		}

		// Add a user message with ID 0001
		userMsg1 := Message{
			Role:      "user",
			Content:   "First question",
			Timestamp: time.Now(),
		}
		result1, err := AppendMessageWithToolInteraction(context.Background(), originalSession, userMsg1, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append first user message: %v", err)
		}

		// Add assistant response with tool call
		toolCallArgs, _ := json.Marshal(map[string]interface{}{
			"command": []string{"echo", "hello"},
		})
		toolCalls := []core.ToolCall{
			{
				ID:   "call_001",
				Name: "universal_command",
				Args: toolCallArgs,
			},
		}
		_, err = SaveAssistantResponseWithTools(
			context.Background(),
			originalSession,
			"I'll run echo for you.",
			toolCalls,
			"test-model",
		)
		if err != nil {
			t.Fatalf("Failed to save assistant response with tools: %v", err)
		}

		// Save tool results
		toolResults := []core.ToolResult{
			{
				ID:     "call_001",
				Output: "hello",
			},
		}
		if _, err := SaveToolResults(context.Background(), originalSession, toolResults, ""); err != nil {
			t.Fatalf("Failed to save tool results: %v", err)
		}

		// Create a branch from the user message (this will create a sibling directory)
		siblingPath, err := CreateSibling(context.Background(), originalSession.Path, result1.MessageID)
		if err != nil {
			t.Fatalf("Failed to create sibling: %v", err)
		}
		siblingSession := &Session{Path: siblingPath}

		// Add a different user message in the sibling branch
		// This will reuse ID 0001 in the sibling directory
		userMsg2 := Message{
			Role:      "user",
			Content:   "Different question",
			Timestamp: time.Now(),
		}
		_, err = AppendMessageWithToolInteraction(context.Background(), siblingSession, userMsg2, nil, nil)
		if err != nil {
			t.Fatalf("Failed to append user message to sibling: %v", err)
		}

		// Add assistant response with different tool call in sibling
		toolCallArgs2, _ := json.Marshal(map[string]interface{}{
			"command": []string{"ls", "-la"},
		})
		toolCalls2 := []core.ToolCall{
			{
				ID:   "call_002",
				Name: "universal_command",
				Args: toolCallArgs2,
			},
		}
		_, err = SaveAssistantResponseWithTools(
			context.Background(),
			siblingSession,
			"I'll list files for you.",
			toolCalls2,
			"test-model",
		)
		if err != nil {
			t.Fatalf("Failed to save assistant response in sibling: %v", err)
		}

		// Save different tool results in sibling
		toolResults2 := []core.ToolResult{
			{
				ID:     "call_002",
				Output: "file1.txt\nfile2.txt",
			},
		}
		if _, err := SaveToolResults(context.Background(), siblingSession, toolResults2, ""); err != nil {
			t.Fatalf("Failed to save tool results in sibling: %v", err)
		}

		// Now fork from the sibling session
		// This tests that tool interactions are copied from the correct directories
		// even when message IDs collide across branches
		newSystemMsg := "You are a file system expert."
		forkedSession, err := ForkSessionWithSystemMessage(context.Background(), siblingSession.Path, &newSystemMsg)
		if err != nil {
			t.Fatalf("Failed to fork session with ID collisions: %v", err)
		}

		// Get messages with tool interactions from forked session
		typedMessages, err := BuildMessagesWithToolInteractions(context.Background(), forkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to build messages with tool interactions: %v", err)
		}

		// Convert to check tool interactions
		typedAnthMessages := core.ToAnthropicTyped(typedMessages)
		messages := core.MarshalAnthropicMessagesForRequest(typedAnthMessages)

		// Verify we have the right tool calls from the sibling branch
		foundEchoTool := false
		foundLsTool := false

		for i, msg := range messages {
			if m, ok := msg.(map[string]interface{}); ok {
				t.Logf("Message %d: role=%v, content type=%T", i, m["role"], m["content"])
				if content, ok := m["content"].([]interface{}); ok {
					t.Logf("  Content array has %d items", len(content))
					for j, block := range content {
						if b, ok := block.(map[string]interface{}); ok {
							t.Logf("    Block %d: type=%v, name=%v", j, b["type"], b["name"])
							if b["type"] == "tool_use" {
								t.Logf("      Tool use block found: name=%v", b["name"])
								if b["name"] == "universal_command" {
									t.Logf("      Input type: %T", b["input"])
									// Check the input to determine which tool call it is
									var inputData map[string]interface{}
									switch v := b["input"].(type) {
									case json.RawMessage:
										if err := json.Unmarshal(v, &inputData); err == nil {
											if cmd, ok := inputData["command"].([]interface{}); ok && len(cmd) > 0 {
												t.Logf("        Command found: %v", cmd[0])
												switch cmd[0] {
												case "echo":
													foundEchoTool = true
												case "ls":
													foundLsTool = true
												}
											}
										} else {
											t.Logf("        Failed to unmarshal json.RawMessage: %v", err)
										}
									case map[string]interface{}:
										if cmd, ok := v["command"].([]interface{}); ok && len(cmd) > 0 {
											t.Logf("        Command found: %v", cmd[0])
											switch cmd[0] {
											case "echo":
												foundEchoTool = true
											case "ls":
												foundLsTool = true
											}
										}
									default:
										t.Logf("        Unexpected input type: %T", v)
									}
								}
							}
						}
					}
				} else if contentSlice, ok := m["content"].([]core.AnthropicContent); ok {
					// Handle case where content is []core.AnthropicContent (typed)
					for _, b := range contentSlice {
						if b.Type == "tool_use" && b.Name == "universal_command" {
							// Check the input to determine which tool call it is
							var input map[string]interface{}
							if err := json.Unmarshal(b.Input, &input); err == nil {
								if cmd, ok := input["command"].([]interface{}); ok && len(cmd) > 0 {
									switch cmd[0] {
									case "echo":
										foundEchoTool = true
									case "ls":
										foundLsTool = true
									}
								}
							}
						}
					}
				} else if contentSlice, ok := m["content"].([]map[string]interface{}); ok {
					// Handle typed array case
					t.Logf("Found content slice with %d items", len(contentSlice))
					for _, b := range contentSlice {
						t.Logf("  Block type=%v, name=%v", b["type"], b["name"])
						if b["type"] == "tool_use" && b["name"] == "universal_command" {
							// input might be json.RawMessage or []byte
							t.Logf("    Input type: %T", b["input"])
							var inputData map[string]interface{}
							switch v := b["input"].(type) {
							case json.RawMessage:
								t.Logf("    Input is json.RawMessage")
								if err := json.Unmarshal(v, &inputData); err == nil {
									if cmd, ok := inputData["command"].([]interface{}); ok && len(cmd) > 0 {
										t.Logf("    Command: %v", cmd[0])
										switch cmd[0] {
										case "echo":
											foundEchoTool = true
										case "ls":
											foundLsTool = true
										}
									}
								} else {
									t.Logf("    Failed to unmarshal: %v", err)
								}
							case []byte:
								t.Logf("    Input is []byte")
								if err := json.Unmarshal(v, &inputData); err == nil {
									if cmd, ok := inputData["command"].([]interface{}); ok && len(cmd) > 0 {
										t.Logf("    Command: %v", cmd[0])
										switch cmd[0] {
										case "echo":
											foundEchoTool = true
										case "ls":
											foundLsTool = true
										}
									}
								} else {
									t.Logf("    Failed to unmarshal: %v", err)
								}
							case map[string]interface{}:
								t.Logf("    Input is map[string]interface{}")
								if cmd, ok := v["command"].([]interface{}); ok && len(cmd) > 0 {
									t.Logf("    Command: %v", cmd[0])
									switch cmd[0] {
									case "echo":
										foundEchoTool = true
									case "ls":
										foundLsTool = true
									}
								}
							default:
								t.Logf("    Unknown input type: %T", v)
							}
						}
					}
				}
			}
		}

		// Should NOT find echo tool (from root) but SHOULD find ls tool (from sibling)
		if foundEchoTool {
			t.Error("Found echo tool from root directory - ID collision caused wrong tool to be copied")
		}
		if !foundLsTool {
			t.Error("Did not find ls tool from sibling directory - correct tool was not copied")
		}

		// Additional verification: check the lineage content
		lineage, err := GetLineage(forkedSession.Path)
		if err != nil {
			t.Fatalf("Failed to get lineage: %v", err)
		}

		// Should have system message + messages from sibling branch only
		hasSystemMsg := false
		hasDifferentQuestion := false
		hasFirstQuestion := false

		for _, msg := range lineage {
			if msg.Role == core.RoleSystem {
				hasSystemMsg = true
			}
			if msg.Content == "Different question" {
				hasDifferentQuestion = true
			}
			if msg.Content == "First question" {
				hasFirstQuestion = true
			}
		}

		if !hasSystemMsg {
			t.Error("Forked session should have new system message")
		}
		if !hasDifferentQuestion {
			t.Error("Forked session should have 'Different question' from sibling branch")
		}
		if hasFirstQuestion {
			t.Error("Forked session should NOT have 'First question' from root")
		}
	})
}
