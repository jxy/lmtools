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
	Type                 string      `json:"type,omitempty"`
	Namespace            string      `json:"namespace,omitempty"`
	NamespaceDescription string      `json:"namespace_description,omitempty"`
	OriginalName         string      `json:"original_name,omitempty"`
	OriginalDescription  string      `json:"original_description,omitempty"`
	Name                 string      `json:"name"`
	Description          string      `json:"description"`
	InputSchema          interface{} `json:"input_schema"`
	Format               interface{} `json:"format,omitempty"`
	Strict               *bool       `json:"strict,omitempty"`
}

const CustomToolInputField = "input"

func CustomToolInputSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			CustomToolInputField: map[string]interface{}{
				"type":        "string",
				"description": "Exact raw input text for the adapted custom tool.",
			},
		},
		"required":             []string{CustomToolInputField},
		"additionalProperties": false,
	}
}

func CustomToolRawInput(inputString string, input json.RawMessage) string {
	if inputString != "" {
		return inputString
	}
	if rawInput, ok := UnwrapCustomToolInput(input); ok {
		return rawInput
	}
	return rawJSONStringValue(input)
}

func WrapCustomToolInput(rawInput string) json.RawMessage {
	data, err := json.Marshal(map[string]string{CustomToolInputField: rawInput})
	if err != nil {
		return json.RawMessage(`{"input":""}`)
	}
	return data
}

func UnwrapCustomToolInput(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var wrapped map[string]interface{}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return "", false
	}
	return UnwrapCustomToolInputValue(wrapped)
}

func UnwrapCustomToolInputValue(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		input, ok := typed[CustomToolInputField].(string)
		return input, ok
	case map[string]string:
		input, ok := typed[CustomToolInputField]
		return input, ok
	case json.RawMessage:
		return UnwrapCustomToolInput(typed)
	case []byte:
		return UnwrapCustomToolInput(json.RawMessage(typed))
	default:
		return "", false
	}
}

// ToolCall represents a tool invocation request from the LLM
type ToolCall struct {
	ID               string          `json:"id"`
	Type             string          `json:"type,omitempty"`
	Namespace        string          `json:"namespace,omitempty"`
	OriginalName     string          `json:"original_name,omitempty"`
	Name             string          `json:"name"`
	Args             json.RawMessage `json:"args"`
	Input            string          `json:"input,omitempty"`
	AssistantContent string          `json:"assistant_content,omitempty"` // The text content from assistant alongside tool calls
	ThoughtSignature string          `json:"thought_signature,omitempty"`
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
	Ctx             context.Context
	Cfg             RequestOptions
	Logger          Logger
	Notifier        Notifier
	Approver        Approver
	ExecCfg         ToolExecutionConfig
	Model           string
	ToolDefs        []ToolDefinition
	MessagesFn      func(string) ([]TypedMessage, error)
	UI              ToolUI
	InitialResponse Response
	InitialText     string
	InitialCalls    []ToolCall
	InitialStreamed bool
}

// ToolExecutionResult holds the result of tool execution
type ToolExecutionResult struct {
	FinalText     string // The final response text from the model
	FinalStreamed bool   // True if the final response was already printed while streaming
	Error         error  // Any error that occurred
}

// ToolUI provides hooks for displaying tool execution progress
type ToolUI interface {
	// BeforeExecute is called before executing a batch of tool calls
	BeforeExecute(calls []ToolCall)
	// AfterExecute is called after executing a batch of tool calls
	AfterExecute(results []ToolResult)
}

// handleToolExecutionLoop implements the tool execution loop.
func handleToolExecutionLoop(tc ToolContext) (string, bool, error) {
	maxRounds := tc.Cfg.GetMaxToolRounds()
	if maxRounds <= 0 {
		maxRounds = DefaultMaxToolRounds
	}

	executor, err := NewExecutor(tc.Cfg, tc.Logger, tc.Notifier, tc.Approver)
	if err != nil {
		return "", false, fmt.Errorf("failed to create executor: %w", err)
	}

	rounds := 0
	response := initialToolResponse(tc)
	finalText := tc.InitialText
	if response.Text != "" {
		finalText = response.Text
	}
	finalStreamed := response.Streamed

	for rounds < maxRounds && len(response.ToolCalls) > 0 {
		rounds++

		// Save assistant's response with tool calls only on first round
		// Subsequent rounds already saved their tool calls at the end of the previous iteration
		if rounds == 1 {
			if err := persistAssistantRound(tc.Ctx, tc.ExecCfg.Store, response, tc.ExecCfg.ActualModel, tc.Logger); err != nil {
				return finalText, finalStreamed, err
			}
		}

		// Notify UI before executing tools
		if tc.UI != nil {
			tc.UI.BeforeExecute(response.ToolCalls)
		}

		// Execute tools in parallel
		results := executor.ExecuteParallel(tc.Ctx, response.ToolCalls)

		// Notify UI after executing tools
		if tc.UI != nil {
			tc.UI.AfterExecute(results)
		}

		// Save tool results
		additionalText := BuildTruncationNotes(results, response.ToolCalls)
		if err := persistToolResultsRound(tc.Ctx, tc.ExecCfg.Store, results, additionalText); err != nil {
			return finalText, finalStreamed, err
		}

		// Build and send follow-up request
		resp, err := BuildAndSendFollowupRequest(tc.Ctx, tc.Cfg, tc.ExecCfg, tc.Model, tc.ToolDefs, tc.MessagesFn, tc.Logger)
		if err != nil {
			return finalText, finalStreamed, err
		}
		// HandleResponse closes the response body - no need to close it here

		// Handle the response
		response, err = HandleResponseWithOptions(tc.Ctx, tc.Cfg, resp, tc.Logger, tc.Notifier, ResponseParseOptions{
			ArgoLegacy: isArgoLegacyMode(tc.Cfg),
			ToolDefs:   tc.ToolDefs,
		})
		if err != nil {
			return finalText, finalStreamed, fmt.Errorf("failed to handle tool result response: %w", err)
		}

		// Update final text if we got a response
		if response.Text != "" {
			finalText = response.Text
			finalStreamed = response.Streamed
		}

		// Save response if we have content, tool calls, or provider metadata to preserve.
		if response.Text != "" || len(response.ToolCalls) > 0 || response.ThoughtSignature != "" || len(response.Blocks) > 0 {
			if err := persistAssistantRound(tc.Ctx, tc.ExecCfg.Store, response, tc.ExecCfg.ActualModel, tc.Logger); err != nil {
				return finalText, finalStreamed, err
			}
		}
	}

	// Check if we hit the max rounds limit
	if rounds >= maxRounds && len(response.ToolCalls) > 0 {
		return finalText, finalStreamed, fmt.Errorf("reached maximum tool execution rounds (%d)", maxRounds)
	}

	return finalText, finalStreamed, nil
}

func initialToolResponse(tc ToolContext) Response {
	response := tc.InitialResponse
	if response.Text != "" || len(response.ToolCalls) > 0 || len(response.Blocks) > 0 || response.ThoughtSignature != "" {
		return response
	}
	return Response{
		Text:      tc.InitialText,
		ToolCalls: tc.InitialCalls,
		Streamed:  tc.InitialStreamed,
	}
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
	finalText, finalStreamed, err := handleToolExecutionLoop(tc)

	return ToolExecutionResult{
		FinalText:     finalText,
		FinalStreamed: finalStreamed,
		Error:         err,
	}
}

// persistAssistantRound saves an assistant message with optional tool calls
func persistAssistantRound(ctx context.Context, store SessionStore, response Response, model string, logger Logger) error {
	if response.Text == "" && len(response.ToolCalls) == 0 && response.ThoughtSignature == "" && len(response.Blocks) == 0 {
		return nil
	}

	var err error
	if responseStore, ok := store.(AssistantResponseStore); ok {
		_, _, err = responseStore.SaveAssistantResponse(ctx, response, model)
	} else if thoughtStore, ok := store.(AssistantThoughtSignatureStore); ok {
		_, _, err = thoughtStore.SaveAssistantWithThoughtSignature(ctx, response.Text, response.ToolCalls, model, response.ThoughtSignature)
	} else {
		_, _, err = store.SaveAssistant(ctx, response.Text, response.ToolCalls, model)
	}
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
func BuildAndSendFollowupRequest(ctx context.Context, cfg RequestOptions, execCfg ToolExecutionConfig,
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
