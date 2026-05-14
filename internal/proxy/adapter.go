package proxy

import (
	"fmt"
	"lmtools/internal/core"
	"strings"
	"time"
)

// ARCHITECTURAL PRINCIPLE: TypedRequest is our canonical internal representation.
// All provider-specific formats (OpenAI, Anthropic, Google, Argo) are converted
// to/from TypedRequest at API boundaries. This ensures:
// 1. Single source of truth for message handling and business logic
// 2. Provider-specific details are isolated to edge converters
// 3. Core logic remains provider-agnostic
//
// Conversion flow:
//   Incoming: ProviderFormat -> TypedRequest -> Internal Processing
//   Outgoing: Internal Processing -> TypedRequest -> ProviderFormat
//
// NEVER convert directly between provider formats. Always go through TypedRequest.
// This ensures consistency and maintainability.

// TypedRequest represents a provider-agnostic request structure
type TypedRequest struct {
	System          string
	Developer       string
	Messages        []core.TypedMessage
	Tools           []core.ToolDefinition
	ToolChoice      *core.ToolChoice
	MaxTokens       *int
	Temperature     *float64
	TopP            *float64
	Stop            []string
	Stream          bool
	ReasoningEffort string // for OpenAI o1 models
	Thinking        *AnthropicThinking
	OutputConfig    *AnthropicOutputConfig
	ResponseFormat  *ResponseFormat
	Metadata        map[string]interface{}
	ServiceTier     string
}

// OpenAIRequestToTyped converts an OpenAI request to TypedRequest
func OpenAIRequestToTyped(req *OpenAIRequest) TypedRequest {
	typed, _ := openAIRequestToTyped(req, false)
	return typed
}

func OpenAIRequestToTypedStrict(req *OpenAIRequest) (TypedRequest, error) {
	return openAIRequestToTyped(req, true)
}

func openAIRequestToTyped(req *OpenAIRequest, strict bool) (TypedRequest, error) {
	maxTokens := positiveIntPtr(req.MaxTokens)
	if maxTokens == nil {
		maxTokens = positiveIntPtr(req.MaxCompletionTokens)
	}

	typed := TypedRequest{
		MaxTokens:       maxTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		Stop:            req.Stop,
		Stream:          req.Stream,
		ReasoningEffort: req.ReasoningEffort,
		ResponseFormat:  req.ResponseFormat,
		Metadata:        cloneStringInterfaceMap(req.Metadata),
		ServiceTier:     req.ServiceTier,
	}

	openAITypedMessages, err := openAIProxyMessagesToCore(req.Messages, strict)
	if err != nil {
		return TypedRequest{}, err
	}

	// Convert messages using typed function
	typed.Messages = core.FromOpenAITyped(openAITypedMessages)

	// Convert tools
	if len(req.Tools) > 0 {
		typed.Tools = make([]core.ToolDefinition, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if tool.Type == "custom" {
				custom := openAICustomToolMap(tool.Custom)
				name, _ := custom["name"].(string)
				if name == "" {
					continue
				}
				description, _ := custom["description"].(string)
				typed.Tools = append(typed.Tools, core.ToolDefinition{
					Type:        "custom",
					Name:        name,
					Description: description,
					Format:      responsesCustomToolFormatFromChat(custom["format"]),
				})
				continue
			}
			if tool.Type != "" && tool.Type != "function" || tool.Function.Name == "" {
				continue
			}
			typed.Tools = append(typed.Tools, core.ToolDefinition{
				Type:        "function",
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: tool.Function.Parameters,
				Strict:      tool.Function.Strict,
			})
		}
	}

	// Convert tool choice
	typed.ToolChoice = openAIToolChoiceToTyped(req.ToolChoice)

	return typed, nil
}

func openAIProxyMessagesToCore(messages []OpenAIMessage, strict bool) ([]core.OpenAIMessage, error) {
	result := make([]core.OpenAIMessage, 0, len(messages))
	pendingLegacyCalls := make(map[string][]pendingLegacyFunctionCall)
	var pendingLegacyOrder []pendingLegacyFunctionCall

	for i, msg := range messages {
		content, err := openAIContentToTypedUnionForMode(msg.Content, strict)
		if err != nil {
			return nil, fmt.Errorf("messages[%d].content: %w", i, err)
		}

		if msg.FunctionCall != nil {
			if strict && msg.Role != core.RoleAssistant {
				return nil, fmt.Errorf("messages[%d].function_call: only assistant messages can contain legacy function_call", i)
			}
			if strict && len(msg.ToolCalls) > 0 {
				return nil, fmt.Errorf("messages[%d]: cannot mix function_call and tool_calls", i)
			}
			if strings.TrimSpace(msg.FunctionCall.Name) == "" {
				if strict {
					return nil, fmt.Errorf("messages[%d].function_call.name: required", i)
				}
			} else if len(msg.ToolCalls) == 0 {
				callID := syntheticLegacyFunctionCallID(i)
				pending := pendingLegacyFunctionCall{
					MessageIndex: i,
					Name:         msg.FunctionCall.Name,
					ID:           callID,
				}
				pendingLegacyCalls[msg.FunctionCall.Name] = append(pendingLegacyCalls[msg.FunctionCall.Name], pending)
				pendingLegacyOrder = append(pendingLegacyOrder, pending)
				args := msg.FunctionCall.Arguments
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				msg.ToolCalls = []ToolCall{{
					ID:   callID,
					Type: "function",
					Function: FunctionCall{
						Name:      msg.FunctionCall.Name,
						Arguments: args,
					},
				}}
			}
		}

		role := string(msg.Role)
		toolCallID := msg.ToolCallID
		if role == "function" {
			if strings.TrimSpace(msg.Name) == "" {
				if strict {
					return nil, fmt.Errorf("messages[%d].name: required for legacy function result", i)
				}
			}
			if toolCallID == "" {
				if id, ok := popPendingLegacyFunctionCallID(pendingLegacyCalls, msg.Name); ok {
					toolCallID = id
				} else if strict {
					return nil, fmt.Errorf("messages[%d]: legacy function result %q has no matching function_call", i, msg.Name)
				} else {
					toolCallID = syntheticLegacyFunctionResultID(i)
				}
			}
			role = string(core.RoleTool)
		}

		result = append(result, core.OpenAIMessage{
			Role:       role,
			Content:    content,
			ToolCalls:  openAIToolCallsToTyped(msg.ToolCalls),
			ToolCallID: toolCallID,
			Name:       msg.Name,
		})
	}
	if strict {
		if pending, ok := firstPendingLegacyFunctionCall(pendingLegacyCalls, pendingLegacyOrder); ok {
			return nil, fmt.Errorf("messages[%d].function_call: legacy function_call %q has no matching function result", pending.MessageIndex, pending.Name)
		}
	}

	return result, nil
}

type pendingLegacyFunctionCall struct {
	MessageIndex int
	Name         string
	ID           string
}

func syntheticLegacyFunctionCallID(index int) string {
	return fmt.Sprintf("call_legacy_%d", index)
}

func syntheticLegacyFunctionResultID(index int) string {
	return fmt.Sprintf("call_legacy_unmatched_%d", index)
}

func popPendingLegacyFunctionCallID(pending map[string][]pendingLegacyFunctionCall, name string) (string, bool) {
	ids := pending[name]
	if len(ids) == 0 {
		return "", false
	}
	id := ids[0]
	if len(ids) == 1 {
		delete(pending, name)
	} else {
		pending[name] = ids[1:]
	}
	return id.ID, true
}

func firstPendingLegacyFunctionCall(pending map[string][]pendingLegacyFunctionCall, order []pendingLegacyFunctionCall) (pendingLegacyFunctionCall, bool) {
	for _, candidate := range order {
		for _, item := range pending[candidate.Name] {
			if item.ID == candidate.ID {
				return item, true
			}
		}
	}
	return pendingLegacyFunctionCall{}, false
}

func positiveIntPtr(value *int) *int {
	if value == nil || *value <= 0 {
		return nil
	}
	return value
}

func omitNonPositiveOpenAITokenLimits(req *OpenAIRequest) {
	if req == nil {
		return
	}
	req.MaxTokens = positiveIntPtr(req.MaxTokens)
	req.MaxCompletionTokens = positiveIntPtr(req.MaxCompletionTokens)
}

// AnthropicRequestToTyped converts an Anthropic request to TypedRequest
func AnthropicRequestToTyped(req *AnthropicRequest) TypedRequest {
	typed := TypedRequest{
		MaxTokens:      &req.MaxTokens,
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		Stop:           req.StopSequences,
		Stream:         req.Stream,
		Thinking:       req.Thinking,
		OutputConfig:   req.OutputConfig,
		ResponseFormat: anthropicOutputConfigToOpenAIResponseFormat(req.OutputConfig),
		Metadata:       cloneStringInterfaceMap(req.Metadata),
		ServiceTier:    req.ServiceTier,
	}
	if req.OutputConfig != nil {
		typed.ReasoningEffort = anthropicEffortToOpenAIReasoningEffort(req.OutputConfig.Effort)
	}

	// Handle system message
	if req.System != nil {
		systemContent, err := extractSystemContent(req.System)
		if err == nil {
			typed.System = systemContent
		} else {
			// Preserve the raw payload as a fallback for malformed inputs.
			typed.System = string(req.System)
		}
	}

	// Convert Anthropic messages to typed Anthropic messages first
	typed.Messages = make([]core.TypedMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		typedMsg := core.TypedMessage{
			Role: string(msg.Role),
		}

		if len(msg.Content) > 0 {
			text, blocks, err := parseAnthropicMessageContent(msg.Content)
			if err == nil {
				if text != nil && *text != "" {
					typedMsg.Blocks = []core.Block{core.TextBlock{Text: *text}}
				} else if len(blocks) > 0 {
					typedMsg.Blocks = AnthropicBlocksToCore(blocks)
				}
			} else {
				typedMsg.Blocks = []core.Block{core.TextBlock{Text: string(msg.Content)}}
			}
		}
		typed.Messages = append(typed.Messages, typedMsg)
	}

	// Convert tools
	if len(req.Tools) > 0 {
		typed.Tools = make([]core.ToolDefinition, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if tool.Name == "" {
				continue
			}
			if tool.Type != "" && tool.Type != "custom" && tool.Type != "function" && tool.InputSchema == nil {
				continue
			}
			typed.Tools = append(typed.Tools, core.ToolDefinition{
				Type:        tool.Type,
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
				Strict:      tool.Strict,
			})
		}
	}

	// Convert tool choice
	if req.ToolChoice != nil {
		typed.ToolChoice = &core.ToolChoice{
			Type: req.ToolChoice.Type,
			Name: req.ToolChoice.Name,
		}
	}

	return typed
}

func typedMessageText(msg core.TypedMessage) string {
	return typedMessageTextBlocks(msg.Blocks)
}

func typedMessageTextBlocks(blocks []core.Block) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch value := block.(type) {
		case core.TextBlock:
			if value.Text != "" {
				parts = append(parts, value.Text)
			}
		case *core.TextBlock:
			if value != nil && value.Text != "" {
				parts = append(parts, value.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func cloneStringInterfaceMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// TypedToOpenAIResponse converts a typed response to OpenAI format
func TypedToOpenAIResponse(typed TypedRequest, content string, toolCalls []core.ToolCall, usage *OpenAIUsage, model string, finishReason string) *OpenAIResponse {
	response := &OpenAIResponse{
		ID:      generateUUID("chatcmpl-"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Usage:   usage,
	}

	// Build the message
	message := OpenAIMessage{
		Role:    core.RoleAssistant,
		Content: content,
	}

	// Convert tool calls if present
	if len(toolCalls) > 0 {
		message.ToolCalls = make([]ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			message.ToolCalls[i] = ToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      tc.Name,
					Arguments: string(tc.Args),
				},
			}
		}
	}

	// Add single choice
	response.Choices = []OpenAIChoice{
		{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		},
	}

	return response
}

// TypedToAnthropicResponse converts a typed response to Anthropic format
func TypedToAnthropicResponse(typed TypedRequest, content []AnthropicContentBlock, usage *AnthropicUsage, model string, stopReason string) *AnthropicResponse {
	return &AnthropicResponse{
		ID:         generateUUID("msg_"),
		Type:       "message",
		Role:       core.RoleAssistant,
		Content:    content,
		Model:      model,
		StopReason: stopReason,
		Usage:      usage,
	}
}
