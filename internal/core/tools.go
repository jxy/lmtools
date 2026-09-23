package core

import (
	"context"
	"encoding/json"
	stdErrors "errors"
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
	// MCPServer and MCPTool name the server and the server's own tool
	// behind a qualified MCP tool name. No wire renders them; response
	// parsing copies them onto the calls the model makes.
	MCPServer string `json:"-"`
	MCPTool   string `json:"-"`
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
	// MCPServer and MCPTool label a call to an MCP tool with the server and
	// the server's tool name, for the review line, the session, and -show.
	// The executor routes by Name; these are what a person reads.
	MCPServer string `json:"mcp_server,omitempty"`
	MCPTool   string `json:"mcp_tool,omitempty"`
	// InvocationID identifies this invocation across every copy of the
	// transcript that carries it: a sibling branch shares the message and a
	// fork copies it, and both keep the identity, so they resolve to the
	// same ownership and outcome records. It is assigned when the message
	// holding the call first commits, kept in .tools.json, and rendered to
	// no wire. A call without one was written before invocations were
	// recorded.
	InvocationID string `json:"invocation_id,omitempty"`
}

// ToolResult represents the result of executing a tool
type ToolResult struct {
	ID      string `json:"tool_call_id"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
	Code    string `json:"code,omitempty"` // Error code for structured error handling
	Elapsed int64  `json:"elapsed_ms"`
	// NotRun records that no process was created. The executor sets it rather
	// than making renderers infer lifecycle state from error codes; elapsed time
	// is meaningless when it is true. Omitempty preserves older session JSON.
	NotRun bool `json:"not_run,omitempty"`
	// Reason and Hints carry a denial's structure for operator display. Error
	// remains the model-facing text, and denyResult composes all three from the
	// same parts so the two audiences cannot be told different things.
	Reason    string   `json:"reason,omitempty"`
	Hints     []string `json:"hints,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	// TruncatedTo is the output cap that actually applied when Truncated is
	// set. The executor stamps it so consumers describe what the truncating
	// writer did rather than re-deriving the limit from configuration.
	TruncatedTo int `json:"truncated_to_bytes,omitempty"`
	// Images are what a view_image call loaded. They travel with the result
	// in memory and into the tool_result block the round's message carries;
	// the JSON tag keeps them out of .tools.json, so a session holds the bytes
	// once, in .blocks.json, and Output stays the text description.
	Images []ImageBlock `json:"-"`
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
	// Journal owns and records each round's calls. It is nil when nothing
	// is persisted, with -no-session, and then no call is recorded.
	Journal ToolJournal
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
	Output          ResponseOutput
	InitialResponse Response
}

// ToolExecutionResult holds the result of tool execution
type ToolExecutionResult struct {
	FinalText     string // The final response text from the model
	FinalStreamed bool   // True if the final response was already printed while streaming
	Error         error  // Any error that occurred
	// UnsavedAnswer is the error from saving a response the loop had
	// already presented. Error carries it too; this field says which
	// failure left the transcript without something the operator saw.
	UnsavedAnswer error
}

// ToolUI displays one append-only tool execution batch. It observes execution;
// approval policy and decisions remain owned by Executor.
type ToolUI interface {
	// ShowCall presents each complete call in index order before any required
	// approval. args carries the executor's already-parsed arguments so the UI
	// does not decode call.Args again; it is nil when the executor never parsed
	// the call (unsupported tool, invalid arguments, or past the per-round cap).
	ShowCall(index, total int, call ToolCall, args *UniversalCommandArgs)
	// BeforeRun is called after every approval decision and before any command starts.
	BeforeRun(total, runnable, parallel int)
	// AfterExecute is called after the batch finishes, with calls and results in
	// their original request order.
	AfterExecute(calls []ToolCall, results []ToolResult)
	// ShowRerun presents a call an earlier run may already have executed,
	// and why its outcome is unknown, immediately before the question
	// whether to run it again. It renders on the stream that question
	// uses, so the operator is never asked about a call they were not
	// shown.
	ShowRerun(call ToolCall, reason string)
}

// ResponseHasContent reports whether a response carries anything a session
// keeps: text, tool calls, a thought signature, or typed blocks.
func ResponseHasContent(response Response) bool {
	return response.Text != "" || len(response.ToolCalls) > 0 || response.ThoughtSignature != "" || len(response.Blocks) > 0
}

// handleToolExecutionLoop implements the tool execution loop.
func handleToolExecutionLoop(tc ToolContext) ToolExecutionResult {
	maxRounds := tc.Cfg.GetMaxToolRounds()

	response := tc.InitialResponse
	finalText := response.Text
	finalStreamed := response.Streamed
	// presentedUnsaved is true while the response the loop holds has been
	// presented and not saved yet. Whatever ends the loop in that state, a
	// failed save or anything before the save, leaves the transcript without
	// a response the operator saw, and the result says so.
	presentedUnsaved := ResponseHasContent(response)
	finish := func(err error) ToolExecutionResult {
		result := ToolExecutionResult{FinalText: finalText, FinalStreamed: finalStreamed, Error: err}
		if err != nil && presentedUnsaved {
			result.UnsavedAnswer = err
		}
		return result
	}

	executor, err := NewExecutor(tc.Cfg, tc.Logger, tc.Approver)
	if err != nil {
		return finish(fmt.Errorf("failed to create executor: %w", err))
	}

	// claim owns the calls of the round about to run. It is taken before the
	// message holding them commits and released once their results commit,
	// so no other run finds these calls pending while this one owns them.
	var claim ToolClaim
	defer func() {
		if claim != nil {
			claim.Release()
		}
	}()

	for round := 0; len(response.ToolCalls) > 0; round++ {
		if round > 0 && round%maxRounds == 0 {
			approved, err := executor.requestToolRoundLimitReset(tc.Ctx, maxRounds)
			if err != nil {
				return finish(err)
			}
			if !approved {
				return finish(fmt.Errorf("reached maximum tool execution rounds (%d)", maxRounds))
			}
		}

		// Save assistant's response with tool calls only on first round
		// Subsequent rounds already saved their tool calls at the end of the previous iteration
		if round == 0 {
			if claim, err = claimRoundCalls(tc.ExecCfg.Journal, response.ToolCalls); err != nil {
				return finish(err)
			}
			if err := persistAssistantRound(tc.Ctx, tc.ExecCfg.Store, response, tc.ExecCfg.ActualModel, tc.Logger); err != nil {
				return finish(err)
			}
			presentedUnsaved = false
		}

		// Review, approve, execute, and display the batch. Approval preflight is
		// completed before any parallel worker starts.
		executor.SetRecorder(claim)
		results := executor.ExecuteParallel(tc.Ctx, response.ToolCalls, tc.UI)
		recordErr := executor.TakeRecordingError()

		// Save tool results. A cancelled turn still commits them: every call
		// has a result by now, including the ones cancellation kept from
		// starting, and leaving them uncommitted would leave the calls
		// pending for a later run to find. A journal that failed to record
		// an outcome does not stop the save either; the failure is reported
		// once the save has been attempted.
		additionalText := BuildTruncationNotes(results, response.ToolCalls)
		saveErr := persistToolResultsRound(tc.Ctx, tc.ExecCfg.Store, results, additionalText)
		if claim != nil {
			claim.Release()
			claim = nil
		}
		if recordErr != nil {
			recordErr = fmt.Errorf("record tool call outcomes: %w", recordErr)
		}
		if err := stdErrors.Join(saveErr, recordErr); err != nil {
			return finish(err)
		}
		if err := tc.Ctx.Err(); err != nil {
			return finish(err)
		}

		// Build and send follow-up request
		resp, err := BuildAndSendFollowupRequest(tc.Ctx, tc.Cfg, tc.ExecCfg, tc.Model, tc.ToolDefs, tc.MessagesFn, tc.Logger)
		if err != nil {
			return finish(err)
		}
		// HandleResponse closes the response body - no need to close it here

		// Handle the response
		response, err = HandleResponseWithOptions(tc.Ctx, tc.Cfg, resp, tc.Logger, tc.Notifier, ResponseParseOptions{
			ArgoLegacy: tc.Cfg.ArgoLegacy,
			ToolDefs:   tc.ToolDefs,
			Output:     tc.Output,
		})
		if err != nil {
			return finish(fmt.Errorf("failed to handle tool result response: %w", err))
		}
		presentedUnsaved = ResponseHasContent(response)

		// Update final text if we got a response
		if response.Text != "" {
			finalText = response.Text
			finalStreamed = response.Streamed
		}

		// Save response if we have content, tool calls, or provider metadata to preserve.
		if response.Text != "" || len(response.ToolCalls) > 0 || response.ThoughtSignature != "" || len(response.Blocks) > 0 {
			if len(response.ToolCalls) > 0 {
				if claim, err = claimRoundCalls(tc.ExecCfg.Journal, response.ToolCalls); err != nil {
					return finish(err)
				}
			}
			if err := persistAssistantRound(tc.Ctx, tc.ExecCfg.Store, response, tc.ExecCfg.ActualModel, tc.Logger); err != nil {
				return finish(err)
			}
			presentedUnsaved = false
		}
	}

	return finish(nil)
}

// claimRoundCalls gives a round's calls their invocation identities and, when
// a journal is configured, takes ownership of them. Both happen before the
// message holding the calls commits.
func claimRoundCalls(journal ToolJournal, calls []ToolCall) (ToolClaim, error) {
	AssignInvocationIDs(calls)
	if journal == nil {
		return nil, nil
	}
	claim, err := journal.Claim(calls)
	if err != nil {
		return nil, fmt.Errorf("claim tool calls: %w", err)
	}
	return claim, nil
}

// requestToolRoundLimitReset asks whether the round counter may start another
// block. Whether anyone can be asked at all is approvalPolicy.canPrompt's
// question, and it is asked here rather than re-derived: a second copy of
// "no flag and an approver" drifts the moment a new reason prompting becomes
// impossible is added to the policy.
func (e *Executor) requestToolRoundLimitReset(ctx context.Context, maxRounds int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !e.policy.canPrompt {
		return false, nil
	}

	approved, err := e.approver.ApproveToolRoundLimitReset(ctx, maxRounds)
	if err != nil {
		return false, fmt.Errorf("prompt for more tool-call rounds: %w", err)
	}
	return approved, nil
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
// - The maximum number of rounds is reached and no interactive reset is approved
//
// Performance optimization: The function builds a message index once at the start to avoid
// O(n^2) behavior when rebuilding messages across multiple tool execution rounds.
func HandleToolExecution(tc ToolContext) ToolExecutionResult {
	return handleToolExecutionLoop(tc)
}

// persistAssistantRound saves an assistant message with optional tool calls.
// The response has already arrived, so the save runs on a persistence
// context: cancelling the turn does not lose what the provider sent.
func persistAssistantRound(ctx context.Context, store SessionStore, response Response, model string, logger Logger) error {
	if response.Text == "" && len(response.ToolCalls) == 0 && response.ThoughtSignature == "" && len(response.Blocks) == 0 {
		return nil
	}
	ctx, cancel := PersistenceContext(ctx)
	defer cancel()

	var err error
	if responseStore, ok := store.(AssistantResponseStore); ok {
		_, _, err = responseStore.SaveAssistantResponse(ctx, response, model)
	} else if thoughtStore, ok := store.(AssistantThoughtSignatureStore); ok {
		_, _, err = thoughtStore.SaveAssistantWithThoughtSignature(ctx, response.Text, response.ToolCalls, model, response.ThoughtSignature)
	} else {
		_, _, err = store.SaveAssistant(ctx, response.Text, response.ToolCalls, model)
	}
	if err != nil {
		if logger != nil && logger.IsDebugEnabled() {
			logger.Debugf("Failed to save assistant response: %v", err)
		}
		return fmt.Errorf("persist assistant response: %w", err)
	}
	return nil
}

// persistToolResultsRound saves tool execution results on a persistence
// context, so a cancellation that cut the round short still records it.
func persistToolResultsRound(ctx context.Context, store SessionStore, results []ToolResult, additionalText string) error {
	ctx, cancel := PersistenceContext(ctx)
	defer cancel()
	_, _, err := store.SaveToolResults(ctx, results, additionalText)
	if err != nil {
		return fmt.Errorf("failed to save tool results: %w", err)
	}
	return nil
}

// BuildTruncationNotes creates additional text for truncated outputs.
//
// TruncatedTo is the cap that actually applied, stamped by the executor. The
// model plans its next command against this number — narrower filters, fewer
// lines — so quoting the package default while the run used
// -tool-max-output-bytes sends it back with a budget off by whatever the
// operator configured.
func BuildTruncationNotes(results []ToolResult, toolCalls []ToolCall) string {
	var notes []string
	for i, result := range results {
		if result.Truncated {
			note := fmt.Sprintf("Note: Output for tool '%s' was truncated to %s",
				toolCalls[i].Name, FormatByteCount(result.TruncatedTo))
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

	if len(toolDefs) == 0 && cfg.ToolEnabled {
		toolDefs = AdvertisedTools(cfg)
	}

	// Build request
	buildMessages := typedMessages
	buildOptions := ChatBuildOptions{
		ModelOverride: model,
		ToolDefs:      toolDefs,
		Stream:        cfg.StreamChat,
	}
	if system, rest, found := splitSystemWithPresence(typedMessages); found {
		buildMessages = rest
		buildOptions.SystemOverride = system
		buildOptions.SystemOverrideSet = true
	}
	req, body, err := BuildChatRequest(cfg, buildMessages, buildOptions)
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
