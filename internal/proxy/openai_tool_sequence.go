package proxy

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"sort"
	"strings"
)

func normalizeTypedMessagesForOpenAIChat(messages []core.TypedMessage) []core.TypedMessage {
	queue := omitReasoningOnlyMessagesForOpenAIChat(messages)
	out := make([]core.TypedMessage, 0, len(messages))
	buffered := make([]core.TypedMessage, 0)
	pending := map[string]struct{}{}
	pendingOrder := make([]string, 0)
	canMergeAssistantToolCalls := false

	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]

		if len(pending) == 0 {
			out = append(out, msg)
			pendingOrder = appendToolUseIDsFromMessage(pending, pendingOrder, msg)
			canMergeAssistantToolCalls = len(pending) > 0
			continue
		}

		if canMergeAssistantToolCalls && assistantOnlyToolUseOrReasoning(msg) {
			out = appendBlocksToLastAssistant(out, msg.Blocks)
			pendingOrder = appendToolUseIDsFromMessage(pending, pendingOrder, msg)
			continue
		}

		matchingResults, remainder := splitMatchingToolResults(msg, pending)
		if len(matchingResults) > 0 {
			out = append(out, core.TypedMessage{
				Role:   string(core.RoleUser),
				Blocks: matchingResults,
			})
			canMergeAssistantToolCalls = false
		}
		if len(remainder.Blocks) > 0 {
			buffered = append(buffered, remainder)
		}
		if len(pending) == 0 {
			queue = append(append([]core.TypedMessage(nil), buffered...), queue...)
			buffered = buffered[:0]
			pendingOrder = pendingOrder[:0]
		}
	}

	if len(buffered) > 0 {
		out = append(out, buffered...)
	}
	return out
}

func omitReasoningOnlyMessagesForOpenAIChat(messages []core.TypedMessage) []core.TypedMessage {
	if len(messages) == 0 {
		return messages
	}
	out := make([]core.TypedMessage, 0, len(messages))
	for _, msg := range messages {
		blocks := make([]core.Block, 0, len(msg.Blocks))
		removedReasoning := false
		for _, block := range msg.Blocks {
			switch block.(type) {
			case core.ReasoningBlock, *core.ReasoningBlock:
				removedReasoning = true
				continue
			default:
				blocks = append(blocks, block)
			}
		}
		if removedReasoning {
			if len(blocks) == 0 {
				continue
			}
			msg.Blocks = blocks
		}
		out = append(out, msg)
	}
	return out
}

func assistantOnlyToolUseOrReasoning(msg core.TypedMessage) bool {
	if msg.Role != string(core.RoleAssistant) || len(msg.Blocks) == 0 {
		return false
	}
	for _, block := range msg.Blocks {
		switch block.(type) {
		case core.ToolUseBlock, *core.ToolUseBlock:
		case core.ReasoningBlock, *core.ReasoningBlock:
		default:
			return false
		}
	}
	return true
}

func appendBlocksToLastAssistant(messages []core.TypedMessage, blocks []core.Block) []core.TypedMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == string(core.RoleAssistant) {
			messages[i].Blocks = append(messages[i].Blocks, blocks...)
			return messages
		}
	}
	return append(messages, core.TypedMessage{Role: string(core.RoleAssistant), Blocks: blocks})
}

func appendToolUseIDsFromMessage(pending map[string]struct{}, order []string, msg core.TypedMessage) []string {
	if msg.Role != string(core.RoleAssistant) {
		return order
	}
	for _, block := range msg.Blocks {
		toolUse, ok := asToolUseBlock(block)
		if !ok || toolUse.ID == "" {
			continue
		}
		if _, exists := pending[toolUse.ID]; exists {
			continue
		}
		pending[toolUse.ID] = struct{}{}
		order = append(order, toolUse.ID)
	}
	return order
}

func splitMatchingToolResults(msg core.TypedMessage, pending map[string]struct{}) ([]core.Block, core.TypedMessage) {
	matching := make([]core.Block, 0)
	remainder := core.TypedMessage{Role: msg.Role, Blocks: make([]core.Block, 0, len(msg.Blocks))}

	for _, block := range msg.Blocks {
		result, ok := asToolResultBlock(block)
		if !ok {
			remainder.Blocks = append(remainder.Blocks, block)
			continue
		}
		if _, exists := pending[result.ToolUseID]; !exists {
			remainder.Blocks = append(remainder.Blocks, block)
			continue
		}
		delete(pending, result.ToolUseID)
		matching = append(matching, result)
	}

	return matching, remainder
}

func asToolUseBlock(block core.Block) (core.ToolUseBlock, bool) {
	switch value := block.(type) {
	case core.ToolUseBlock:
		return value, true
	case *core.ToolUseBlock:
		if value != nil {
			return *value, true
		}
	}
	return core.ToolUseBlock{}, false
}

func asToolResultBlock(block core.Block) (core.ToolResultBlock, bool) {
	switch value := block.(type) {
	case core.ToolResultBlock:
		return value, true
	case *core.ToolResultBlock:
		if value != nil {
			return *value, true
		}
	}
	return core.ToolResultBlock{}, false
}

func validateOpenAIChatToolSequence(messages []OpenAIMessage) error {
	pending := map[string]struct{}{}
	pendingOrder := make([]string, 0)
	pendingAssistantIndex := -1

	for i, msg := range messages {
		role := string(msg.Role)
		if len(pending) > 0 {
			if role != "tool" {
				return newRequestValidationErrorf(
					"messages[%d].role: assistant message with tool_calls at messages[%d] must be followed by tool messages responding to each tool_call_id; missing: %s",
					i,
					pendingAssistantIndex,
					formatPendingToolCallIDs(pending, pendingOrder),
				)
			}
			if msg.ToolCallID == "" {
				return newRequestValidationErrorf("messages[%d].tool_call_id is required", i)
			}
			if _, ok := pending[msg.ToolCallID]; !ok {
				return newRequestValidationErrorf(
					"messages[%d].tool_call_id: %q does not match pending tool calls (%s)",
					i,
					msg.ToolCallID,
					formatPendingToolCallIDs(pending, pendingOrder),
				)
			}
			delete(pending, msg.ToolCallID)
			if len(pending) == 0 {
				pendingOrder = pendingOrder[:0]
				pendingAssistantIndex = -1
			}
			continue
		}

		if role == "tool" {
			return newRequestValidationErrorf("messages[%d].role: tool message has no preceding assistant tool_calls", i)
		}
		if role != string(core.RoleAssistant) || len(msg.ToolCalls) == 0 {
			continue
		}

		for j, toolCall := range msg.ToolCalls {
			if toolCall.ID == "" {
				return newRequestValidationErrorf("messages[%d].tool_calls[%d].id is required", i, j)
			}
			if _, exists := pending[toolCall.ID]; exists {
				return newRequestValidationErrorf("messages[%d].tool_calls[%d].id duplicates %q", i, j, toolCall.ID)
			}
			pending[toolCall.ID] = struct{}{}
			pendingOrder = append(pendingOrder, toolCall.ID)
		}
		if len(pending) > 0 {
			pendingAssistantIndex = i
		}
	}

	if len(pending) > 0 {
		return newRequestValidationErrorf(
			"messages: assistant message with tool_calls at messages[%d] must be followed by tool messages responding to each tool_call_id; missing: %s",
			pendingAssistantIndex,
			formatPendingToolCallIDs(pending, pendingOrder),
		)
	}
	return nil
}

func validateArgoOpenAIChatToolSequence(messages []ArgoMessage) error {
	openAIMessages := make([]OpenAIMessage, 0, len(messages))
	for _, msg := range messages {
		openAIMessages = append(openAIMessages, OpenAIMessage{
			Role:       core.Role(msg.Role),
			Content:    msg.Content,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
		})
	}
	return validateOpenAIChatToolSequence(openAIMessages)
}

func (s *Server) validateConvertedOpenAIChatToolSequence(typed TypedRequest, provider, model string) error {
	if s.useNativeArgoOpenAIChatRoute(provider, model) {
		_, err := renderTypedToOpenAIRequest(typed, typedRenderContext{Model: model, OpenAIChatCompatibilityTools: true})
		return err
	}
	return nil
}

func (s *Server) useNativeArgoOpenAIChatRoute(provider, model string) bool {
	return !s.useLegacyArgo() && isArgoOpenAIChatRoute(provider, model)
}

func isArgoOpenAIChatRoute(provider, model string) bool {
	return constants.NormalizeProvider(provider) == constants.ProviderArgo &&
		providers.DetermineArgoModelProvider(model) == constants.ProviderOpenAI
}

func formatPendingToolCallIDs(pending map[string]struct{}, order []string) string {
	ids := make([]string, 0, len(pending))
	for _, id := range order {
		if _, ok := pending[id]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		for id := range pending {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	return strings.Join(ids, ", ")
}

type requestValidationError struct {
	message string
}

func newRequestValidationErrorf(format string, args ...interface{}) error {
	return &requestValidationError{message: fmt.Sprintf(format, args...)}
}

func (e *requestValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}
