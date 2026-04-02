package core

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/retry"
	"net/http"
	"strings"
)

// ToolDefinition is the standard tool definition format (Anthropic format)
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

// ToolCall represents a tool invocation request from the LLM
type ToolCall struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Args             json.RawMessage `json:"args"`
	AssistantContent string          `json:"assistant_content,omitempty"` // The text content from assistant alongside tool calls
}

// ToolResult represents the result of executing a tool
type ToolResult struct {
	ID        string `json:"tool_call_id"`
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
	Code      string `json:"code,omitempty"` // Error code for structured error handling
	Elapsed   int64  `json:"elapsed_ms"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ToolInteraction represents tool calls and results for session storage
type ToolInteraction struct {
	Calls   []ToolCall   `json:"calls,omitempty"`
	Results []ToolResult `json:"results,omitempty"`
}

// ToolChoice represents the tool selection strategy
type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// ToolExecutionConfig contains configuration for tool execution
type ToolExecutionConfig struct {
	Store        SessionStore            // Session storage abstraction
	RetryClient  *retry.Client           // The retry client to use for requests
	LogRequestFn func(body []byte) error // Optional function to log requests
	ActualModel  string                  // The actual model being used
}

// ToolContext encapsulates all dependencies needed for tool execution
// This reduces parameter sprawl and makes the API cleaner
type ToolContext struct {
	Ctx          context.Context
	Cfg          ChatRequestConfig
	Logger       Logger
	Notifier     Notifier
	Approver     Approver
	ExecCfg      ToolExecutionConfig
	Model        string
	ToolDefs     []ToolDefinition
	MessagesFn   func(string) ([]TypedMessage, error)
	UI           ToolUI
	InitialText  string
	InitialCalls []ToolCall
}

// ToolExecutionResult holds the result of tool execution
type ToolExecutionResult struct {
	FinalText string // The final response text from the model
	Error     error  // Any error that occurred
}

// ToolUI provides hooks for displaying tool execution progress
type ToolUI interface {
	// BeforeExecute is called before executing a batch of tool calls
	BeforeExecute(calls []ToolCall)
	// AfterExecute is called after executing a batch of tool calls
	AfterExecute(results []ToolResult)
}

// toolRunner encapsulates all dependencies needed for tool execution
type toolRunner struct {
	Ctx        context.Context
	Cfg        ChatRequestConfig
	Logger     Logger
	Notifier   Notifier
	Approver   Approver
	ExecCfg    ToolExecutionConfig
	Model      string
	ToolDefs   []ToolDefinition
	MsgBuilder func(string) ([]TypedMessage, error)
	UI         ToolUI
}

// InitialToolState represents the initial state for tool execution
type InitialToolState struct {
	Text      string
	ToolCalls []ToolCall
}

// run executes the tool execution loop
func (r *toolRunner) run(initial InitialToolState) (string, error) {
	return r.handleToolExecution(initial.Text, initial.ToolCalls)
}

// handleToolExecution implements the tool execution loop
func (r *toolRunner) handleToolExecution(initialResponse string, initialToolCalls []ToolCall) (string, error) {
	maxRounds := r.Cfg.GetMaxToolRounds()
	if maxRounds <= 0 {
		maxRounds = DefaultMaxToolRounds
	}

	executor, err := NewExecutor(r.Cfg, r.Logger, r.Notifier, r.Approver)
	if err != nil {
		return "", fmt.Errorf("failed to create executor: %w", err)
	}

	rounds := 0
	text := initialResponse
	toolCalls := initialToolCalls
	finalText := initialResponse // Track the final text to return

	for rounds < maxRounds && len(toolCalls) > 0 {
		rounds++

		// Save assistant's response with tool calls only on first round
		// Subsequent rounds already saved their tool calls at the end of the previous iteration
		if rounds == 1 {
			if err := persistAssistantRound(r.Ctx, r.ExecCfg.Store, text, toolCalls, r.ExecCfg.ActualModel, r.Logger); err != nil {
				return finalText, err
			}
		}

		// Notify UI before executing tools
		if r.UI != nil {
			r.UI.BeforeExecute(toolCalls)
		}

		// Execute tools in parallel
		results := executor.ExecuteParallel(r.Ctx, toolCalls)

		// Notify UI after executing tools
		if r.UI != nil {
			r.UI.AfterExecute(results)
		}

		// Save tool results
		additionalText := BuildTruncationNotes(results, toolCalls)
		if err := persistToolResultsRound(r.Ctx, r.ExecCfg.Store, results, additionalText); err != nil {
			return finalText, err
		}

		// Build and send follow-up request
		resp, err := BuildAndSendFollowupRequest(r.Ctx, r.Cfg, r.ExecCfg, r.Model, r.ToolDefs, r.MsgBuilder, r.Logger)
		if err != nil {
			return finalText, err
		}
		// HandleResponse closes the response body - no need to close it here

		// Handle the response
		response, err := HandleResponse(r.Ctx, r.Cfg, resp, r.Logger, r.Notifier)
		if err != nil {
			return finalText, fmt.Errorf("failed to handle tool result response: %w", err)
		}
		text = response.Text
		toolCalls = response.ToolCalls

		// Update final text if we got a response
		if text != "" {
			finalText = text
		}

		// Save response if we have content or tool calls
		if text != "" || len(toolCalls) > 0 {
			if err := persistAssistantRound(r.Ctx, r.ExecCfg.Store, text, toolCalls, r.ExecCfg.ActualModel, r.Logger); err != nil {
				return finalText, err
			}
		}
	}

	// Check if we hit the max rounds limit
	if rounds >= maxRounds && len(toolCalls) > 0 {
		return finalText, fmt.Errorf("reached maximum tool execution rounds (%d)", maxRounds)
	}

	return finalText, nil
}

// HandleToolExecution manages the tool execution loop for a conversation.
// It handles multiple rounds of tool calls, respecting the maxRounds limit.
//
// Important notes about response handling:
// - HandleResponse is responsible for closing HTTP response bodies - callers should NOT close them
// - For streaming responses: HandleResponse prints output in real-time as it arrives
// - For non-streaming responses: The final text is returned in ToolExecutionResult
// - This division ensures output is displayed correctly regardless of streaming mode
//
// The function executes tools in a loop until either:
// - No more tool calls are returned by the model
// - The maximum number of rounds (maxRounds) is reached
//
// Performance optimization: The function builds a message index once at the start to avoid
// O(n^2) behavior when rebuilding messages across multiple tool execution rounds.
func HandleToolExecution(tc ToolContext) ToolExecutionResult {
	// Create a toolRunner with all the dependencies
	runner := &toolRunner{
		Ctx:        tc.Ctx,
		Cfg:        tc.Cfg,
		Logger:     tc.Logger,
		Notifier:   tc.Notifier,
		Approver:   tc.Approver,
		ExecCfg:    tc.ExecCfg,
		Model:      tc.Model,
		ToolDefs:   tc.ToolDefs,
		MsgBuilder: tc.MessagesFn,
		UI:         tc.UI,
	}

	// Run the tool execution loop
	finalText, err := runner.run(InitialToolState{
		Text:      tc.InitialText,
		ToolCalls: tc.InitialCalls,
	})

	return ToolExecutionResult{
		FinalText: finalText,
		Error:     err,
	}
}

// persistAssistantRound saves an assistant message with optional tool calls
func persistAssistantRound(ctx context.Context, store SessionStore, text string, toolCalls []ToolCall, model string, logger Logger) error {
	if text == "" && len(toolCalls) == 0 {
		return nil
	}

	_, _, err := store.SaveAssistant(ctx, text, toolCalls, model)
	if err != nil {
		if logger.IsDebugEnabled() {
			logger.Debugf("Failed to save assistant response: %v", err)
		}
		return fmt.Errorf("persist assistant response: %w", err)
	}
	return nil
}

// persistToolResultsRound saves tool execution results
func persistToolResultsRound(ctx context.Context, store SessionStore, results []ToolResult, additionalText string) error {
	_, _, err := store.SaveToolResults(ctx, results, additionalText)
	if err != nil {
		return fmt.Errorf("failed to save tool results: %w", err)
	}
	return nil
}

// BuildTruncationNotes creates additional text for truncated outputs
func BuildTruncationNotes(results []ToolResult, toolCalls []ToolCall) string {
	var notes []string
	for i, result := range results {
		if result.Truncated {
			note := fmt.Sprintf("Note: Output for tool '%s' was truncated to %dMB",
				toolCalls[i].Name, DefaultMaxOutputSize/(1024*1024))
			notes = append(notes, note)
		}
	}
	if len(notes) > 0 {
		return strings.Join(notes, "\n") + "\n"
	}
	return ""
}

// BuildAndSendFollowupRequest builds and sends a follow-up request after tool execution
func BuildAndSendFollowupRequest(ctx context.Context, cfg ChatRequestConfig, execCfg ToolExecutionConfig,
	model string, toolDefs []ToolDefinition,
	getMessagesWithTools func(string) ([]TypedMessage, error),
	logger Logger,
) (*http.Response, error) {
	// Get current session path from store and rebuild messages
	sessionPath := execCfg.Store.GetPath()
	typedMessages, err := getMessagesWithTools(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages with tools: %w", err)
	}

	// Extract system message if present
	system := extractSystemMessage(typedMessages)

	// Build request
	req, body, err := BuildToolResultRequest(cfg, model, system, toolDefs, typedMessages)
	if err != nil {
		return nil, fmt.Errorf("failed to build tool result request: %w", err)
	}

	// Log request if function provided
	if execCfg.LogRequestFn != nil {
		_ = execCfg.LogRequestFn(body) // Ignore error
	}

	// Send request using retry client
	if execCfg.RetryClient != nil {
		// Use "lmc-tool" as component name for tool follow-up requests
		return execCfg.RetryClient.Do(ctx, req, "lmc-tool")
	}

	// Fallback to direct HTTP client if no retry client provided
	client := &http.Client{}
	return client.Do(req)
}

// extractSystemMessage extracts the system message from typed messages if present
func extractSystemMessage(typedMessages []TypedMessage) string {
	system, _ := splitSystem(typedMessages)
	return system
}

// ValidateToolSupport validates if tools are supported for the given provider and model combination.
// This centralizes the tool support policy to avoid duplication and drift.
//
// Current policy:
// - Direct Google provider: SUPPORTS tools
// - Google models via Argo: SUPPORT tools
// - All other providers: SUPPORT tools
func ValidateToolSupport(provider, model string) error {
	// All current provider/model combinations support tools.
	_ = provider
	_ = model
	return nil
}
