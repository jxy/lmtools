package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/mockserver"
	"lmtools/internal/providerconfig"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func toolTestDirs(t *testing.T) (string, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	sessionDir := filepath.Join(tmpDir, "sessions")
	logDir := filepath.Join(tmpDir, "logs")
	if err := os.MkdirAll(sessionDir, constants.DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, constants.DirPerm); err != nil {
		t.Fatal(err)
	}
	return tmpDir, sessionDir, logDir
}

func writeToolListFile(t *testing.T, dir, name string, commands ...string) string {
	t.Helper()
	if len(commands) == 0 {
		return ""
	}
	lines := make([]string, 0, len(commands))
	for _, cmd := range commands {
		lines = append(lines, fmt.Sprintf(`[%q]`, cmd))
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), constants.FilePerm); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestToolIntegrationFlow(t *testing.T) {
	// Create temp directories
	tmpDir, sessionDir, logDir := toolTestDirs(t)

	// Create whitelist file
	whitelistFile := writeToolListFile(t, tmpDir, "whitelist.txt", "echo", "date", "pwd")

	// Start mock server with tool support
	ms := mockserver.NewMockServer()

	// Configure the ProviderURL to include /messages endpoint
	// which will be handled by the mock server's chat handler

	// Configure mock to return tool calls on first request
	ms.SetResponseFunc(func(req *http.Request) (interface{}, int, error) {
		// Read request body to check if it's a tool result submission
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, 500, err
		}
		// Reset the body for any subsequent reads
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		bodyStr := string(bodyBytes)

		// Debug: log the request body
		t.Logf("Mock server received request body: %s", bodyStr)

		// Return Anthropic-style responses with tool calls
		if strings.Contains(bodyStr, "tool_result") {
			// This is a follow-up with tool results
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "The command executed successfully. The output shows 'Hello from tools!'",
					},
				},
			}, 200, nil
		}

		// Initial request - return tool call
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "I'll run that echo command for you.",
				},
				{
					"type":  "tool_use",
					"id":    "call-123",
					"name":  "universal_command",
					"input": map[string]interface{}{"command": []string{"echo", "Hello from tools!"}},
				},
			},
		}, 200, nil
	})

	// Start the mock server
	serverURL := ms.URL()
	defer ms.Close()

	// Create config
	cfg := config.Config{
		Options: providerconfig.Options{
			ArgoUser:    "testuser",
			Provider:    "anthropic",
			ProviderURL: serverURL,
		},
		Model:           "claude-3-opus",
		SessionsDir:     sessionDir,
		LogDir:          logDir,
		EnableTool:      true,
		ToolTimeout:     5 * time.Second,
		ToolWhitelist:   whitelistFile,
		ToolAutoApprove: true,
	}

	// Initialize logger with log directory. Initialization is first-call-wins,
	// so an earlier test that made the default logger would otherwise keep it,
	// and response logs would go to ~/.lmc/logs.
	logger.ResetForTesting()
	t.Cleanup(logger.ResetForTesting)
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(logDir),
		logger.WithLevel("info"),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	log := logger.GetLogger()

	// Create the session with the user message
	userMsg := "Please run echo 'Hello from tools!'"
	store := session.Open(cfg.SessionsDir)
	sess := newTestSession(t, store, textEntry(core.RoleUser, userMsg))

	// Create retry client
	retryClient := retry.NewClientWithRetries(5*time.Second, 3, log)

	typedMessages := historyOf(t, sess)
	rbReq, rbBody, err := core.BuildChatRequest(cfg.RequestOptions(), typedMessages, core.ChatBuildOptions{ToolDefs: core.GetBuiltinUniversalCommandTool()})
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}
	// Execute request with retry
	ctx := context.Background()
	rbReq.Body = io.NopCloser(bytes.NewReader(rbBody))
	resp, err := retryClient.Do(ctx, rbReq, cfg.Provider)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	// Handle response (should return tool calls)
	notifier := core.NewTestNotifier()
	response, err := core.HandleResponse(ctx, cfg.RequestOptions(), resp, log, notifier)
	if err != nil {
		t.Fatalf("Failed to handle response: %v", err)
	}

	// Verify we got tool calls
	if len(response.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(response.ToolCalls))
	}

	if response.ToolCalls[0].Name != "universal_command" {
		t.Errorf("Expected tool name 'universal_command', got %s", response.ToolCalls[0].Name)
	}

	// Save assistant response with tools
	withCalls := saveToolCalls(t, sess, response.Text, response.ToolCalls)
	if tools := newestTools(t, sess); tools == nil || len(tools.Calls) != 1 {
		t.Fatalf("saved calls %+v, want the one call", tools)
	}

	// Create and execute tools
	approver := core.NewTestApprover(true) // Auto-approve for tests
	executor, err := core.NewExecutor(cfg.RequestOptions(), logger.GetLogger(), approver)
	if err != nil {
		t.Fatalf("Failed to create executor: %v", err)
	}

	results := executor.ExecuteParallel(ctx, response.ToolCalls, nil)

	// Verify tool execution
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	if results[0].Error != "" {
		t.Errorf("Tool execution failed: %s", results[0].Error)
	}

	if !strings.Contains(results[0].Output, "Hello from tools!") {
		t.Errorf("Expected output to contain 'Hello from tools!', got: %s", results[0].Output)
	}

	// Save tool results
	additionalText := ""
	if results[0].Truncated {
		additionalText = fmt.Sprintf("Note: Output for tool '%s' was truncated", response.ToolCalls[0].Name)
	}

	if _, err := sess.SaveToolResults(context.Background(), results, additionalText); err != nil {
		t.Fatalf("Failed to save tool results: %v", err)
	}

	// Build follow-up request with tool results
	// Create typed messages that include the full conversation context
	followUpMessages := []core.TypedMessage{
		// System message
		core.NewTextMessage("system", "You are a helpful assistant."),
		// Initial user message
		core.NewTextMessage("user", userMsg),
		// Assistant response with tool call
		{
			Role: "assistant",
			Blocks: []core.Block{
				core.TextBlock{Text: response.Text},
				core.ToolUseBlock{
					ID:    response.ToolCalls[0].ID,
					Name:  response.ToolCalls[0].Name,
					Input: response.ToolCalls[0].Args,
				},
			},
		},
		// Tool result message
		{
			Role: "user",
			Blocks: []core.Block{
				core.ToolResultBlock{
					ToolUseID: results[0].ID,
					Content:   results[0].Output,
				},
			},
		},
	}

	// Add additional text block if present
	if additionalText != "" {
		lastMsg := &followUpMessages[len(followUpMessages)-1]
		lastMsg.Blocks = append(lastMsg.Blocks, core.TextBlock{Text: additionalText})
	}

	req2, reqBody2, err := core.BuildChatRequest(cfg.RequestOptions(), followUpMessages, core.ChatBuildOptions{
		ModelOverride: cfg.Model,
		ToolDefs:      core.GetBuiltinUniversalCommandTool(),
	})
	if err != nil {
		t.Fatalf("Failed to build tool result request: %v", err)
	}

	// Execute follow-up request
	req2.Body = io.NopCloser(bytes.NewReader(reqBody2))
	resp2, err := retryClient.Do(ctx, req2, cfg.Provider)
	if err != nil {
		t.Fatalf("Follow-up request failed: %v", err)
	}
	defer resp2.Body.Close()

	// Handle final response
	finalResponse, err := core.HandleResponse(ctx, cfg.RequestOptions(), resp2, log, notifier)
	if err != nil {
		t.Fatalf("Failed to handle final response: %v", err)
	}

	// Should have no more tool calls
	if len(finalResponse.ToolCalls) > 0 {
		t.Errorf("Expected no tool calls in final response, got %d", len(finalResponse.ToolCalls))
	}

	// Should have final text
	if !strings.Contains(finalResponse.Text, "successfully") {
		t.Errorf("Expected final text to contain 'successfully', got: %s", finalResponse.Text)
	}

	// Save final response
	if _, err := sess.SaveReply(context.Background(), core.Response{Text: finalResponse.Text}, cfg.Model); err != nil {
		t.Fatalf("Failed to save final response: %v", err)
	}

	// Verify session has all messages
	h := historyAt(t, store, sess.Ref())
	if len(h.Messages) != 4 {
		t.Fatalf("lineage length = %d, want 4 messages", len(h.Messages))
	}
	lineage := h.Messages
	if lineage[0].Role != core.RoleUser || lineage[1].Role != core.RoleAssistant || lineage[2].Role != core.RoleUser || lineage[3].Role != core.RoleAssistant {
		t.Fatalf("lineage roles = [%s %s %s %s], want [user assistant user assistant]", lineage[0].Role, lineage[1].Role, lineage[2].Role, lineage[3].Role)
	}
	if lineage[1].ID != withCalls.ID || h.Contents[1].Tools == nil || len(h.Contents[1].Tools.Calls) != 1 {
		t.Errorf("the reply with the call holds %+v", h.Contents[1].Tools)
	}
	if h.Contents[2].Tools == nil || len(h.Contents[2].Tools.Results) != 1 {
		t.Errorf("the results message holds %+v", h.Contents[2].Tools)
	}
	if h.Contents[3].Tools != nil {
		t.Errorf("the final answer holds tools %+v", h.Contents[3].Tools)
	}
}

func TestMultiRoundToolExecutionWithLimitResets(t *testing.T) {
	// Create temp directories
	tmpDir, sessionDir, logDir := toolTestDirs(t)

	// Create whitelist file
	whitelistFile := writeToolListFile(t, tmpDir, "whitelist.txt", "echo", "date", "pwd")

	// Start mock server with multi-round tool support
	ms := mockserver.NewMockServer()

	// Track request count to simulate multiple rounds
	requestCount := 0
	// Configure mock to return tool calls for multiple rounds
	ms.SetResponseFunc(func(req *http.Request) (interface{}, int, error) {
		// Read and validate the complete accumulated tool history.
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, 500, err
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		requestCount++
		t.Logf("Mock server handling request %d", requestCount)
		var requestData map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
			return map[string]interface{}{"error": map[string]interface{}{"type": "invalid_request", "message": err.Error()}}, 400, nil
		}
		if err := validateAnthropicToolHistory(requestData, requestCount-1); err != nil {
			return map[string]interface{}{"error": map[string]interface{}{"type": "invalid_tool_history", "message": err.Error()}}, 400, nil
		}

		switch requestCount {
		case 1:
			// Initial request - return first tool call
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "I'll run the first command for you.",
					},
					{
						"type":  "tool_use",
						"id":    "call-001",
						"name":  "universal_command",
						"input": map[string]interface{}{"command": []string{"echo", "First command"}},
					},
				},
			}, 200, nil

		case 2:
			// First follow-up with tool results - return second tool call
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "First command executed. Now let me run the second command.",
					},
					{
						"type":  "tool_use",
						"id":    "call-002",
						"name":  "universal_command",
						"input": map[string]interface{}{"command": []string{"echo", "Second command"}},
					},
				},
			}, 200, nil

		case 3:
			// Second follow-up with tool results - return third tool call
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Second command done. One more command to run.",
					},
					{
						"type":  "tool_use",
						"id":    "call-003",
						"name":  "universal_command",
						"input": map[string]interface{}{"command": []string{"echo", "Third command"}},
					},
				},
			}, 200, nil

		case 4:
			// Third follow-up with tool results - return final response without tools
			return map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "All three commands have been executed successfully!",
					},
				},
			}, 200, nil
		}

		// Any additional request is a hard failure, not a successful fallback.
		return map[string]interface{}{
			"error": map[string]interface{}{
				"type":    "unexpected_request",
				"message": fmt.Sprintf("unexpected request %d", requestCount),
			},
		}, 400, nil
	})

	// Start the mock server
	serverURL := ms.URL()
	defer ms.Close()

	// Create config
	cfg := config.Config{
		Options: providerconfig.Options{
			ArgoUser:    "testuser",
			Provider:    "anthropic",
			ProviderURL: serverURL + "/messages",
		},
		Model:              "claude-3-opus",
		EnableTool:         true,
		ToolWhitelist:      whitelistFile,
		ToolAutoApprove:    true,
		ToolNonInteractive: false,
		MaxToolRounds:      1,
		SessionsDir:        sessionDir,
		LogDir:             logDir,
		Timeout:            10 * time.Second,
	}

	// Create context and logger
	ctx := context.Background()
	log := core.NewTestLogger(false)

	// Create notifier
	notifier := core.NewTestNotifier()

	// Build initial request
	userMsg := "Please run three commands in sequence"
	messages := []core.TypedMessage{
		core.NewTextMessage("user", userMsg),
	}
	toolDefs := core.GetBuiltinUniversalCommandTool()
	req, reqBody, err := core.BuildChatRequest(cfg.RequestOptions(), messages, core.ChatBuildOptions{ToolDefs: toolDefs})
	if err != nil {
		t.Fatalf("Failed to build chat request: %v", err)
	}

	// Create retry client
	retryClient := retry.NewClient(cfg.Timeout, log)

	// Execute initial request
	req.Body = io.NopCloser(bytes.NewReader(reqBody))
	resp, err := retryClient.Do(ctx, req, cfg.Provider)
	if err != nil {
		t.Fatalf("Initial request failed: %v", err)
	}
	defer resp.Body.Close()

	// Handle initial response
	response, err := core.HandleResponse(ctx, cfg.RequestOptions(), resp, log, notifier)
	if err != nil {
		t.Fatalf("Failed to handle initial response: %v", err)
	}

	// Should have tool calls
	if len(response.ToolCalls) == 0 {
		t.Fatal("Expected tool calls in initial response")
	}

	// Save initial user message
	store := session.Open(sessionDir)
	sess := newTestSession(t, store, textEntry(core.RoleUser, userMsg))
	msgBuilder := func(string) ([]core.TypedMessage, error) {
		view, err := sess.View(cfg.RequestOptions())
		return view.Messages, err
	}

	// Create tool context for execution. A one-round allowance requires two
	// renewals to complete all three requested rounds.
	approver := &core.TestApprover{DefaultApproval: true, ResetResponses: []bool{true, true}}
	toolCtx := core.ToolContext{
		Ctx:      ctx,
		Cfg:      cfg.RequestOptions(),
		Logger:   log,
		Notifier: notifier,
		Approver: approver,
		ExecCfg: core.ToolExecutionConfig{
			Store: sess.ToolStore(),
			Send: func(ctx context.Context, req *http.Request, _ []byte) (*http.Response, error) {
				return retryClient.Do(ctx, req, "lmc")
			},
			ActualModel: cfg.Model,
		},
		Model:           cfg.Model,
		ToolDefs:        toolDefs,
		MessagesFn:      msgBuilder,
		InitialResponse: response,
	}

	// Execute tool rounds
	result := core.HandleToolExecution(toolCtx)
	if result.Error != nil {
		t.Fatalf("Tool execution failed: %v", result.Error)
	}
	if len(approver.ResetCalls) != 2 || approver.ResetCalls[0] != 1 || approver.ResetCalls[1] != 1 {
		t.Fatalf("round-limit reset calls = %v, want [1 1]", approver.ResetCalls)
	}

	// Verify the tool loop reached the intended final response.
	if result.FinalText != "All three commands have been executed successfully!" {
		t.Fatalf("final text = %q", result.FinalText)
	}

	// Verify the request and stored-message sequences completed exactly once.
	if requestCount != 4 {
		t.Fatalf("request count = %d, want 4", requestCount)
	}

	lineage := historyAt(t, store, sess.Ref()).Messages
	if len(lineage) != 8 {
		t.Fatalf("lineage length = %d, want 8", len(lineage))
	}
	wantRoles := []core.Role{
		core.RoleUser, core.RoleAssistant,
		core.RoleUser, core.RoleAssistant,
		core.RoleUser, core.RoleAssistant,
		core.RoleUser, core.RoleAssistant,
	}
	for i, want := range wantRoles {
		if lineage[i].Role != want {
			t.Fatalf("lineage[%d].Role = %q, want %q", i, lineage[i].Role, want)
		}
	}
	if awaitsAnswer(t, sess) {
		t.Fatal("the session awaits an answer, want none pending")
	}
}

func validateAnthropicToolHistory(body map[string]interface{}, completedRounds int) error {
	tools, ok := body["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return fmt.Errorf("tools missing from request")
	}
	messages, ok := body["messages"].([]interface{})
	if !ok {
		return fmt.Errorf("messages has type %T", body["messages"])
	}

	foundUserPrompt := false
	sequence := make([]string, 0, completedRounds*2)
	resultContent := make(map[string]string, completedRounds)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			return fmt.Errorf("message has type %T", rawMessage)
		}
		role, _ := message["role"].(string)
		if role == "user" && strings.Contains(fmt.Sprint(message["content"]), "Please run three commands in sequence") {
			foundUserPrompt = true
		}
		content, ok := message["content"].([]interface{})
		if !ok {
			continue
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]interface{})
			if !ok {
				return fmt.Errorf("content block has type %T", rawBlock)
			}
			switch block["type"] {
			case "tool_use":
				if role != "assistant" {
					return fmt.Errorf("tool_use appears under role %q", role)
				}
				id, _ := block["id"].(string)
				sequence = append(sequence, "call:"+id)
			case "tool_result":
				if role != "user" {
					return fmt.Errorf("tool_result appears under role %q", role)
				}
				id, _ := block["tool_use_id"].(string)
				sequence = append(sequence, "result:"+id)
				resultContent[id] = fmt.Sprint(block["content"])
			}
		}
	}
	expectedOutputs := []string{"First command", "Second command", "Third command"}
	return verifyToolCallSequence(sequence, foundUserPrompt, completedRounds, resultContent,
		func(round int) string { return fmt.Sprintf("call-%03d", round) },
		func(round int) string { return expectedOutputs[round-1] })
}

// verifyToolCallSequence is the wire-format-independent tail shared by the
// tool history validators: the original user prompt must be present, each
// completed round must contribute its call/result pair in execution order,
// and each result must carry that round's expected output. callID and
// expectedOutput supply the per-wire call ID scheme and per-round output
// marker; results maps call IDs to their recorded output.
func verifyToolCallSequence(sequence []string, foundPrompt bool, completedRounds int, results map[string]string, callID, expectedOutput func(round int) string) error {
	if !foundPrompt {
		return fmt.Errorf("original user prompt missing")
	}
	if len(sequence) != completedRounds*2 {
		return fmt.Errorf("tool sequence = %v, want %d entries", sequence, completedRounds*2)
	}
	for round := 1; round <= completedRounds; round++ {
		id := callID(round)
		callIndex := (round - 1) * 2
		if sequence[callIndex] != "call:"+id || sequence[callIndex+1] != "result:"+id {
			return fmt.Errorf("tool sequence = %v, want call/result pair for %s at round %d", sequence, id, round)
		}
		if !strings.Contains(results[id], expectedOutput(round)) {
			return fmt.Errorf("result %s content = %q", id, results[id])
		}
	}
	return nil
}

func TestParallelToolExecution(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create whitelist
	whitelistFile := writeToolListFile(t, tmpDir, "whitelist.txt", "echo", "sleep")

	cfg := config.Config{
		ToolTimeout:     5 * time.Second,
		ToolWhitelist:   whitelistFile,
		ToolAutoApprove: true,
	}

	approver := core.NewTestApprover(true) // Auto-approve for tests
	executor, err := core.NewExecutor(cfg.RequestOptions(), logger.GetLogger(), approver)
	if err != nil {
		t.Fatal(err)
	}

	// Create multiple tool calls
	calls := []core.ToolCall{
		{
			ID:   "call-1",
			Name: "universal_command",
			Args: json.RawMessage(`{"command":["echo","first"]}`),
		},
		{
			ID:   "call-2",
			Name: "universal_command",
			Args: json.RawMessage(`{"command":["echo","second"]}`),
		},
		{
			ID:   "call-3",
			Name: "universal_command",
			Args: json.RawMessage(`{"command":["echo","third"]}`),
		},
	}

	ctx := context.Background()
	start := time.Now()
	results := executor.ExecuteParallel(ctx, calls, nil)
	elapsed := time.Since(start)

	// Verify all completed
	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	// Check they executed in parallel (should be fast)
	if elapsed > 1*time.Second {
		t.Logf("Warning: Parallel execution took %v, might not be optimal", elapsed)
	}

	// Verify results
	expectedOutputs := []string{"first", "second", "third"}
	for i, result := range results {
		if result.Error != "" {
			t.Errorf("Result %d had error: %s", i, result.Error)
		}
		if !strings.Contains(result.Output, expectedOutputs[i]) {
			t.Errorf("Result %d: expected output to contain '%s', got: %s",
				i, expectedOutputs[i], result.Output)
		}
		if result.ID != calls[i].ID {
			t.Errorf("Result %d: ID mismatch, expected %s, got %s",
				i, calls[i].ID, result.ID)
		}
	}
}

func TestToolOutputTruncation(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create whitelist
	whitelistFile := writeToolListFile(t, tmpDir, "whitelist.txt", "sh")

	cfg := config.Config{
		ToolTimeout:     5 * time.Second,
		ToolWhitelist:   whitelistFile,
		ToolAutoApprove: true,
	}

	approver := core.NewTestApprover(true) // Auto-approve for tests
	executor, err := core.NewExecutor(cfg.RequestOptions(), logger.GetLogger(), approver)
	if err != nil {
		t.Fatal(err)
	}

	// Generate command that produces large output
	// Use a loop to generate >1MB of output
	call := core.ToolCall{
		ID:   "call-large",
		Name: "universal_command",
		Args: json.RawMessage(`{"command":["sh","-c","for i in $(seq 1 20000); do echo 'This is a long line of output that will be repeated many times'; done"]}`),
	}

	ctx := context.Background()
	results := executor.ExecuteParallel(ctx, []core.ToolCall{call}, nil)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	result := results[0]

	// Check truncation
	if !result.Truncated {
		t.Error("Expected output to be truncated")
	}

	// Check output size is around 1MB
	outputSize := len(result.Output)
	maxSize := 1024 * 1024
	if outputSize > maxSize {
		t.Errorf("Output should be truncated to %d bytes, got %d bytes", maxSize, outputSize)
	}

	// Should still have some output
	if outputSize < 100000 {
		t.Errorf("Output seems too small: %d bytes", outputSize)
	}
}

func TestToolApprovalMechanisms(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name          string
		whitelist     []string
		blacklist     []string
		command       []string
		autoApprove   bool
		expectError   bool
		errorContains string
	}{
		{
			name:        "Whitelisted command auto-approved",
			whitelist:   []string{"echo", "date"},
			command:     []string{"echo", "test"},
			autoApprove: true,
			expectError: false,
		},
		{
			name:          "Non-whitelisted command rejected",
			whitelist:     []string{"echo"},
			command:       []string{"ls", "dangerous-test-command"},
			autoApprove:   true,
			expectError:   true,
			errorContains: "denied: not in whitelist", // Expecting whitelist rejection message
		},
		{
			name:          "Blacklisted command rejected",
			blacklist:     []string{"rm", "dd"},
			command:       []string{"rm", "file.txt"},
			autoApprove:   true,
			expectError:   true,
			errorContains: "denied: blacklisted", // Now expecting simplified message
		},
		{
			name:        "Command not in blacklist approved",
			blacklist:   []string{"rm", "dd"},
			command:     []string{"echo", "safe"},
			autoApprove: true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			whitelistFile := writeToolListFile(t, tmpDir, "whitelist.txt", tt.whitelist...)
			blacklistFile := writeToolListFile(t, tmpDir, "blacklist.txt", tt.blacklist...)

			cfg := config.Config{
				ToolTimeout:        5 * time.Second,
				ToolWhitelist:      whitelistFile,
				ToolBlacklist:      blacklistFile,
				ToolAutoApprove:    tt.autoApprove,
				ToolNonInteractive: true, // Prevent prompts in tests
			}

			approver := core.NewTestApprover(true) // Auto-approve for tests
			executor, err := core.NewExecutor(cfg.RequestOptions(), logger.GetLogger(), approver)
			if err != nil {
				t.Fatal(err)
			}

			args, _ := json.Marshal(map[string]interface{}{
				"command": tt.command,
			})

			call := core.ToolCall{
				ID:   "test-call",
				Name: "universal_command",
				Args: args,
			}

			ctx := context.Background()
			results := executor.ExecuteParallel(ctx, []core.ToolCall{call}, nil)

			if len(results) != 1 {
				t.Fatalf("Expected 1 result, got %d", len(results))
			}

			result := results[0]

			if tt.expectError {
				if result.Error == "" {
					t.Error("Expected error but got none")
				}
				if tt.errorContains != "" && !strings.Contains(result.Error, tt.errorContains) {
					t.Errorf("Expected error to contain '%s', got: %s",
						tt.errorContains, result.Error)
				}
			} else {
				if result.Error != "" {
					t.Errorf("Unexpected error: %s", result.Error)
				}
			}
		})
	}
}
