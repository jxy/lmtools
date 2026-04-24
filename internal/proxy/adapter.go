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
	maxTokens := req.MaxTokens
	if maxTokens == nil && req.MaxCompletionTokens != nil {
		maxTokens = req.MaxCompletionTokens
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

	// Convert OpenAI messages to typed OpenAI messages first
	openAITypedMessages := make([]core.OpenAIMessage, len(req.Messages))
	for i, msg := range req.Messages {
		content, err := openAIContentToTypedUnionForMode(msg.Content, strict)
		if err != nil {
			return TypedRequest{}, fmt.Errorf("messages[%d].content: %w", i, err)
		}
		openAITypedMessages[i] = core.OpenAIMessage{
			Role:       string(msg.Role),
			Content:    content,
			ToolCalls:  openAIToolCallsToTyped(msg.ToolCalls),
			ToolCallID: msg.ToolCallID,
		}
	}

	// Convert messages using typed function
	typed.Messages = core.FromOpenAITyped(openAITypedMessages)

	// Convert tools
	if len(req.Tools) > 0 {
		typed.Tools = make([]core.ToolDefinition, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if tool.Type != "" && tool.Type != "function" {
				continue
			}
			if tool.Function.Name == "" {
				continue
			}
			typed.Tools = append(typed.Tools, core.ToolDefinition{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: tool.Function.Parameters,
			})
		}
	}

	// Convert tool choice
	typed.ToolChoice = openAIToolChoiceToTyped(req.ToolChoice)

	return typed, nil
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
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
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
