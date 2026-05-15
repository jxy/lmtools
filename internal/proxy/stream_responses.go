package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
	"net/http"
	"sort"
	"strings"
	"time"
)

type responsesStreamWriter struct {
	sse              *SSEWriter
	ctx              context.Context
	responseID       string
	model            string
	conversationID   string
	createdAt        int64
	sequence         int
	output           []OpenAIResponsesOutputItem
	outputText       string
	usage            *OpenAIResponsesUsage
	serviceTier      string
	incompleteReason string
	responseError    interface{}
	started          bool
	messageIndex     *int
	messageItemID    string
	messageText      string
	messagePartOpen  bool
	toolItems        map[int]*responsesStreamToolItemState
	reasoningItems   map[int]*responsesReasoningItemState
	toolNameRegistry responseToolNameRegistry
}

type responsesStreamToolItemState struct {
	OutputIndex int
	ItemID      string
	CallID      string
	Namespace   string
	Name        string
	Payload     string
}

type responsesReasoningItemState struct {
	OutputIndex int
	ItemID      string
	BlockType   string
	Thinking    string
	Signature   string
	Data        string
}

type anthropicStreamMessageDeltaEvent struct {
	Type  string                     `json:"type"`
	Delta MessageDelta               `json:"delta"`
	Usage *anthropicStreamUsageDelta `json:"usage,omitempty"`
}

type anthropicStreamUsageDelta struct {
	InputTokens  *int `json:"input_tokens,omitempty"`
	OutputTokens *int `json:"output_tokens,omitempty"`
}

// newResponsesStreamWriter emits OpenAI Responses-compatible SSE for converted
// provider streams. It intentionally implements the subset that can be derived
// from Anthropic, OpenAI chat-completions, and Google streams. Exact native
// Responses passthrough still uses forwardOpenAIResponsesStreamDirectly.
func newResponsesStreamWriter(w http.ResponseWriter, ctx context.Context, originalModel string) (*responsesStreamWriter, error) {
	sse, err := NewSSEWriter(w, ctx)
	if err != nil {
		return nil, err
	}
	return &responsesStreamWriter{
		sse:            sse,
		ctx:            ctx,
		responseID:     generateUUID("resp_"),
		model:          originalModel,
		createdAt:      time.Now().Unix(),
		toolItems:      make(map[int]*responsesStreamToolItemState),
		reasoningItems: make(map[int]*responsesReasoningItemState),
	}, nil
}

func (w *responsesStreamWriter) SetConversationID(id string) {
	w.conversationID = id
}

func (w *responsesStreamWriter) SetToolNameRegistry(registry responseToolNameRegistry) {
	w.toolNameRegistry = registry
}

func (w *responsesStreamWriter) send(event string, payload map[string]interface{}) error {
	payload["type"] = event
	payload["sequence_number"] = w.sequence
	w.sequence++
	return w.sse.WriteJSON(event, payload)
}

func (w *responsesStreamWriter) start() error {
	w.started = true
	resp := w.currentResponse("in_progress")
	if err := w.send("response.created", map[string]interface{}{"response": resp}); err != nil {
		return err
	}
	return w.send("response.in_progress", map[string]interface{}{"response": resp})
}

func (w *responsesStreamWriter) currentResponse(status string) OpenAIResponsesResponse {
	output := append([]OpenAIResponsesOutputItem(nil), w.output...)
	if output == nil {
		output = []OpenAIResponsesOutputItem{}
	}
	return OpenAIResponsesResponse{
		ID:                w.responseID,
		Object:            "response",
		CreatedAt:         w.createdAt,
		Status:            status,
		Model:             w.model,
		Conversation:      openAIResponsesConversationRef(w.conversationID),
		Output:            output,
		OutputText:        w.outputText,
		Usage:             w.usage,
		Error:             w.responseError,
		ServiceTier:       w.serviceTier,
		IncompleteDetails: w.incompleteDetails(),
	}
}

func (w *responsesStreamWriter) incompleteDetails() interface{} {
	if w.incompleteReason == "" {
		return nil
	}
	return map[string]interface{}{"reason": w.incompleteReason}
}

func (w *responsesStreamWriter) ensureMessageItem() error {
	if w.messageIndex != nil {
		return nil
	}
	idx := len(w.output)
	w.messageIndex = &idx
	w.messageItemID = generateUUID("msg_")
	item := OpenAIResponsesOutputItem{
		ID:      w.messageItemID,
		Type:    "message",
		Status:  "in_progress",
		Role:    core.RoleAssistant,
		Content: []OpenAIResponsesContentPart{},
	}
	w.output = append(w.output, item)
	return w.send("response.output_item.added", map[string]interface{}{
		"output_index": idx,
		"item":         item,
	})
}

func (w *responsesStreamWriter) ensureMessageContentPart() error {
	if w.messagePartOpen {
		return nil
	}
	if err := w.ensureMessageItem(); err != nil {
		return err
	}
	w.messagePartOpen = true
	return w.send("response.content_part.added", map[string]interface{}{
		"output_index":  *w.messageIndex,
		"content_index": 0,
		"item_id":       w.messageItemID,
		"part": OpenAIResponsesContentPart{
			Type:        "output_text",
			Annotations: []interface{}{},
		},
	})
}

func (w *responsesStreamWriter) WriteTextDelta(text string) error {
	if text == "" {
		return nil
	}
	if err := w.ensureMessageContentPart(); err != nil {
		return err
	}
	w.messageText += text
	w.outputText += text
	return w.send("response.output_text.delta", map[string]interface{}{
		"output_index":  *w.messageIndex,
		"content_index": 0,
		"item_id":       w.messageItemID,
		"delta":         text,
		"logprobs":      []interface{}{},
	})
}

func (w *responsesStreamWriter) closeMessageItem(status string) error {
	if w.messageIndex == nil {
		return nil
	}
	idx := *w.messageIndex
	part := OpenAIResponsesContentPart{
		Type:        "output_text",
		Text:        w.messageText,
		Annotations: []interface{}{},
	}
	if w.messagePartOpen {
		if err := w.send("response.output_text.done", map[string]interface{}{
			"output_index":  idx,
			"content_index": 0,
			"item_id":       w.messageItemID,
			"text":          w.messageText,
			"logprobs":      []interface{}{},
		}); err != nil {
			return err
		}
		if err := w.send("response.content_part.done", map[string]interface{}{
			"output_index":  idx,
			"content_index": 0,
			"item_id":       w.messageItemID,
			"part":          part,
		}); err != nil {
			return err
		}
	}
	item := w.output[idx]
	item.Status = status
	item.Content = []OpenAIResponsesContentPart{part}
	w.output[idx] = item
	if err := w.send("response.output_item.done", map[string]interface{}{
		"output_index": idx,
		"item":         item,
	}); err != nil {
		return err
	}
	w.messageIndex = nil
	w.messageItemID = ""
	w.messageText = ""
	w.messagePartOpen = false
	return nil
}

func (w *responsesStreamWriter) ensureToolItem(index int, id, name, itemType, toolType string) (*responsesStreamToolItemState, error) {
	if state, ok := w.toolItems[index]; ok {
		if state.Name == "" && name != "" {
			state.Name, state.Namespace = w.responseToolOutputName(name, toolType)
		}
		return state, nil
	}
	if err := w.closeMessageItem("completed"); err != nil {
		return nil, err
	}
	itemID := id
	if itemID == "" {
		itemID = generateUUID(responsesToolItemIDPrefix(itemType))
	}
	callID := id
	if callID == "" {
		callID = generateUUID("call_")
	}
	outputName, namespace := w.responseToolOutputName(name, toolType)
	state := &responsesStreamToolItemState{
		OutputIndex: len(w.output),
		ItemID:      itemID,
		CallID:      callID,
		Namespace:   namespace,
		Name:        outputName,
	}
	w.toolItems[index] = state
	item := OpenAIResponsesOutputItem{
		ID:        state.ItemID,
		Type:      itemType,
		Status:    "in_progress",
		CallID:    state.CallID,
		Namespace: state.Namespace,
		Name:      state.Name,
	}
	if itemType == "function_call" {
		item.Arguments = ""
	} else {
		item.Input = ""
	}
	w.output = append(w.output, item)
	return state, w.send("response.output_item.added", map[string]interface{}{
		"output_index": state.OutputIndex,
		"item":         item,
	})
}

func responsesToolItemIDPrefix(itemType string) string {
	if itemType == "custom_tool_call" {
		return "ctc_"
	}
	return "fc_"
}

func (w *responsesStreamWriter) responseToolOutputName(name, toolType string) (string, string) {
	if mapping, ok := w.toolNameRegistry.resolve(name, toolType); ok {
		return mapping.Name, mapping.Namespace
	}
	return name, ""
}

func (w *responsesStreamWriter) WriteFunctionCallDelta(index int, id, name, arguments string) error {
	state, err := w.ensureToolItem(index, id, name, "function_call", "function")
	if err != nil {
		return err
	}
	if arguments == "" {
		return nil
	}
	state.Payload += arguments
	return w.send("response.function_call_arguments.delta", map[string]interface{}{
		"output_index": state.OutputIndex,
		"item_id":      state.ItemID,
		"delta":        arguments,
	})
}

func (w *responsesStreamWriter) WriteCustomToolCallDelta(index int, id, name, input string) error {
	state, err := w.ensureToolItem(index, id, name, "custom_tool_call", "custom")
	if err != nil {
		return err
	}
	if input == "" {
		return nil
	}
	state.Payload += input
	return w.send("response.custom_tool_call_input.delta", map[string]interface{}{
		"output_index": state.OutputIndex,
		"item_id":      state.ItemID,
		"delta":        input,
	})
}

func (w *responsesStreamWriter) closeToolItems(status string) error {
	for _, state := range w.orderedToolStates() {
		i := state.OutputIndex
		if i < 0 || i >= len(w.output) {
			continue
		}
		item := w.output[i]
		if (item.Type != "function_call" && item.Type != "custom_tool_call") || item.Status == "completed" || item.Status == "incomplete" {
			continue
		}
		item.Status = status
		item.Namespace = state.Namespace
		item.Name = state.Name
		item.CallID = state.CallID
		if item.Type == "function_call" {
			arguments := state.Payload
			if strings.TrimSpace(arguments) == "" {
				arguments = "{}"
			}
			if err := w.send("response.function_call_arguments.done", map[string]interface{}{
				"output_index": state.OutputIndex,
				"item_id":      state.ItemID,
				"arguments":    arguments,
			}); err != nil {
				return err
			}
			item.Arguments = arguments
		} else {
			if err := w.send("response.custom_tool_call_input.done", map[string]interface{}{
				"output_index": state.OutputIndex,
				"item_id":      state.ItemID,
				"input":        state.Payload,
			}); err != nil {
				return err
			}
			item.Input = state.Payload
		}
		w.output[i] = item
		if err := w.send("response.output_item.done", map[string]interface{}{
			"output_index": i,
			"item":         item,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (w *responsesStreamWriter) StartReasoningBlock(index int, block AnthropicContentBlock) error {
	if _, ok := w.reasoningItems[index]; ok {
		return nil
	}
	if err := w.closeMessageItem("completed"); err != nil {
		return err
	}
	itemID := generateUUID("rs_")
	state := &responsesReasoningItemState{
		OutputIndex: len(w.output),
		ItemID:      itemID,
		BlockType:   block.Type,
		Thinking:    block.Thinking,
		Signature:   block.Signature,
		Data:        block.Data,
	}
	w.reasoningItems[index] = state
	item := OpenAIResponsesOutputItem{
		ID:      itemID,
		Type:    "reasoning",
		Status:  "in_progress",
		Summary: anthropicThinkingToResponsesSummary(AnthropicContentBlock{Type: block.Type, Thinking: block.Thinking}),
	}
	w.output = append(w.output, item)
	return w.send("response.output_item.added", map[string]interface{}{
		"output_index": state.OutputIndex,
		"item":         item,
	})
}

func (w *responsesStreamWriter) WriteReasoningDelta(index int, delta DeltaContent) error {
	state, ok := w.reasoningItems[index]
	if !ok {
		return nil
	}
	switch delta.Type {
	case "thinking_delta":
		state.Thinking += delta.Thinking
	case "signature_delta":
		state.Signature += delta.Signature
	}
	return nil
}

func (w *responsesStreamWriter) CloseReasoningBlock(index int, status string) error {
	state, ok := w.reasoningItems[index]
	if !ok {
		return nil
	}
	item := w.output[state.OutputIndex]
	item.Status = status
	item.Summary = anthropicThinkingToResponsesSummary(AnthropicContentBlock{
		Type:     state.BlockType,
		Thinking: state.Thinking,
	})
	w.output[state.OutputIndex] = item
	return w.send("response.output_item.done", map[string]interface{}{
		"output_index": state.OutputIndex,
		"item":         item,
	})
}

func (w *responsesStreamWriter) closeReasoningItems(status string) error {
	for _, index := range w.orderedReasoningIndexes() {
		state := w.reasoningItems[index]
		if state == nil || state.OutputIndex < 0 || state.OutputIndex >= len(w.output) {
			continue
		}
		item := w.output[state.OutputIndex]
		if item.Status == "completed" || item.Status == "incomplete" {
			continue
		}
		if err := w.CloseReasoningBlock(index, status); err != nil {
			return err
		}
	}
	return nil
}

func (w *responsesStreamWriter) SetUsage(usage *OpenAIUsage) {
	w.usage = openAIUsageToResponsesUsage(usage)
}

func (w *responsesStreamWriter) SetUsageCounts(inputTokens, outputTokens *int) {
	if inputTokens == nil && outputTokens == nil {
		return
	}
	if w.usage == nil {
		w.usage = &OpenAIResponsesUsage{}
	}
	if inputTokens != nil {
		w.usage.InputTokens = *inputTokens
	}
	if outputTokens != nil {
		w.usage.OutputTokens = *outputTokens
	}
	w.usage.TotalTokens = w.usage.InputTokens + w.usage.OutputTokens
}

func (w *responsesStreamWriter) Finish(finishReason string) (*OpenAIResponsesResponse, error) {
	status := "completed"
	itemStatus := "completed"
	if finishReason == "length" || finishReason == "max_tokens" {
		status = "incomplete"
		itemStatus = "incomplete"
		w.incompleteReason = "max_output_tokens"
	}
	if err := w.closeMessageItem(itemStatus); err != nil {
		return nil, err
	}
	if err := w.closeToolItems(itemStatus); err != nil {
		return nil, err
	}
	if err := w.closeReasoningItems(itemStatus); err != nil {
		return nil, err
	}
	resp := w.currentResponse(status)
	finalEvent := "response.completed"
	if status == "incomplete" {
		finalEvent = "response.incomplete"
	}
	if err := w.send(finalEvent, map[string]interface{}{"response": resp}); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (w *responsesStreamWriter) Fail(streamErr error) (*OpenAIResponsesResponse, error) {
	w.responseError = responsesStreamFailurePayload(streamErr)

	var firstErr error
	if err := w.closeMessageItem("incomplete"); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := w.closeToolItems("incomplete"); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := w.closeReasoningItems("incomplete"); err != nil && firstErr == nil {
		firstErr = err
	}

	resp := w.currentResponse("failed")
	if err := w.send("response.failed", map[string]interface{}{"response": resp}); err != nil {
		return &resp, err
	}
	return &resp, firstErr
}

func responsesStreamFailurePayload(streamErr error) map[string]interface{} {
	message := "Responses stream failed"
	if streamErr != nil && streamErr.Error() != "" {
		message = streamErr.Error()
	}
	return map[string]interface{}{
		"type":    "server_error",
		"code":    "upstream_stream_error",
		"message": message,
	}
}

func (w *responsesStreamWriter) Blocks() []core.Block {
	reasoningByOutput := make(map[int]*responsesReasoningItemState, len(w.reasoningItems))
	for _, state := range w.reasoningItems {
		if state != nil {
			reasoningByOutput[state.OutputIndex] = state
		}
	}
	blocks := make([]core.Block, 0, len(w.output))
	for i, item := range w.output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					blocks = append(blocks, core.TextBlock{Text: part.Text})
				}
			}
		case "reasoning":
			if state := reasoningByOutput[i]; state != nil {
				if block := state.toCoreBlock(); block != nil {
					blocks = append(blocks, block)
				}
			}
		case "function_call":
			blocks = append(blocks, core.ToolUseBlock{
				ID:           firstNonEmpty(item.CallID, item.ID),
				Type:         "function",
				Namespace:    item.Namespace,
				OriginalName: item.Name,
				Name:         responseOutputToolName(item),
				Input:        normalizeResponsesArguments(item.Arguments),
			})
		case "custom_tool_call":
			blocks = append(blocks, core.ToolUseBlock{
				ID:           firstNonEmpty(item.CallID, item.ID),
				Type:         "custom",
				Namespace:    item.Namespace,
				OriginalName: item.Name,
				Name:         responseOutputToolName(item),
				Input:        mustMarshalJSON(item.Input),
				InputString:  item.Input,
			})
		}
	}
	return blocks
}

func (w *responsesStreamWriter) orderedToolStates() []*responsesStreamToolItemState {
	states := make([]*responsesStreamToolItemState, 0, len(w.toolItems))
	for _, state := range w.toolItems {
		if state != nil {
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].OutputIndex < states[j].OutputIndex
	})
	return states
}

func (w *responsesStreamWriter) orderedReasoningIndexes() []int {
	indexes := make([]int, 0, len(w.reasoningItems))
	for index := range w.reasoningItems {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return w.reasoningItems[indexes[i]].OutputIndex < w.reasoningItems[indexes[j]].OutputIndex
	})
	return indexes
}

func (s *responsesReasoningItemState) toCoreBlock() core.Block {
	switch s.BlockType {
	case "thinking":
		raw := mustMarshalJSON(map[string]interface{}{
			"type":      "thinking",
			"thinking":  s.Thinking,
			"signature": s.Signature,
		})
		return core.ReasoningBlock{
			Provider:  "anthropic",
			Type:      "thinking",
			Text:      s.Thinking,
			Signature: s.Signature,
			Raw:       raw,
		}
	case "redacted_thinking":
		raw := mustMarshalJSON(map[string]interface{}{
			"type": "redacted_thinking",
			"data": s.Data,
		})
		return core.ReasoningBlock{
			Provider: "anthropic",
			Type:     "redacted_thinking",
			Raw:      raw,
		}
	default:
		return nil
	}
}

func (s *Server) streamResponsesFromProvider(ctx context.Context, anthReq *AnthropicRequest, provider, originalModel string, writer *responsesStreamWriter) (*OpenAIResponsesResponse, []core.Block, error) {
	finishReason := "stop"
	var err error
	switch provider {
	case constants.ProviderAnthropic:
		finishReason, err = s.streamResponsesFromAnthropic(ctx, anthReq, writer)
	case constants.ProviderGoogle:
		finishReason, err = s.streamResponsesFromGoogle(ctx, anthReq, writer)
	case constants.ProviderOpenAI:
		finishReason, err = s.streamResponsesFromOpenAI(ctx, anthReq, writer)
	case constants.ProviderArgo:
		finishReason, err = s.streamResponsesFromArgo(ctx, anthReq, writer)
	default:
		err = fmt.Errorf("unknown provider: %s", provider)
	}
	if err != nil {
		return nil, nil, err
	}
	resp, err := writer.Finish(finishReason)
	if resp != nil {
		resp.Model = originalModel
	}
	return resp, writer.Blocks(), err
}

func (s *Server) streamResponsesFromOpenAI(ctx context.Context, anthReq *AnthropicRequest, writer *responsesStreamWriter) (string, error) {
	resp, err := s.openAIStreamingRequest(ctx, anthReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderOpenAI, resp); err != nil {
		return "", err
	}
	if err := writer.start(); err != nil {
		return "", err
	}
	return s.convertOpenAIChatStreamToResponses(ctx, resp.Body, writer)
}

func (s *Server) streamResponsesFromAnthropic(ctx context.Context, anthReq *AnthropicRequest, writer *responsesStreamWriter) (string, error) {
	resp, err := s.anthropicStreamingRequest(ctx, anthReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderAnthropic, resp); err != nil {
		return "", err
	}
	if err := writer.start(); err != nil {
		return "", err
	}
	return s.convertAnthropicStreamToResponses(ctx, resp.Body, writer)
}

func (s *Server) streamResponsesFromGoogle(ctx context.Context, anthReq *AnthropicRequest, writer *responsesStreamWriter) (string, error) {
	resp, err := s.googleStreamingRequest(ctx, anthReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderGoogle, resp); err != nil {
		return "", err
	}
	if err := writer.start(); err != nil {
		return "", err
	}
	return s.convertGoogleStreamToResponses(ctx, resp.Body, writer)
}

func (s *Server) streamResponsesFromArgo(ctx context.Context, anthReq *AnthropicRequest, writer *responsesStreamWriter) (string, error) {
	if s.useLegacyArgo() {
		return s.simulateLegacyArgoResponsesStream(ctx, anthReq, writer)
	}
	if providers.DetermineArgoModelProvider(anthReq.Model) == constants.ProviderAnthropic {
		resp, err := s.argoAnthropicStreamingRequest(ctx, anthReq)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if err := s.ensureStreamingResponseOK(ctx, constants.ProviderArgo, resp); err != nil {
			return "", err
		}
		if err := writer.start(); err != nil {
			return "", err
		}
		return s.convertAnthropicStreamToResponses(ctx, resp.Body, writer)
	}
	openAIReq, err := s.converter.ConvertAnthropicToOpenAI(ctx, anthReq)
	if err != nil {
		return "", fmt.Errorf("convert to Argo OpenAI format: %w", err)
	}
	resp, err := s.argoOpenAIStreamingRequest(ctx, openAIReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderArgo, resp); err != nil {
		return "", err
	}
	if err := writer.start(); err != nil {
		return "", err
	}
	return s.convertOpenAIChatStreamToResponses(ctx, resp.Body, writer)
}

func (s *Server) streamResponsesFromArgoOpenAIRequest(ctx context.Context, openAIReq *OpenAIRequest, writer *responsesStreamWriter) (*OpenAIResponsesResponse, []core.Block, error) {
	resp, err := s.argoOpenAIStreamingRequest(ctx, openAIReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if err := s.ensureStreamingResponseOK(ctx, constants.ProviderArgo, resp); err != nil {
		return nil, nil, err
	}
	if err := writer.start(); err != nil {
		return nil, nil, err
	}
	finishReason, err := s.convertOpenAIChatStreamToResponses(ctx, resp.Body, writer)
	if err != nil {
		return nil, nil, err
	}
	response, err := writer.Finish(finishReason)
	if err != nil {
		return nil, nil, err
	}
	return response, writer.Blocks(), nil
}

func (s *Server) simulateLegacyArgoResponsesStream(ctx context.Context, anthReq *AnthropicRequest, writer *responsesStreamWriter) (string, error) {
	argoResp, err := s.forwardToArgo(ctx, anthReq)
	if err != nil {
		return "", err
	}
	anthResp := s.converter.ConvertArgoToAnthropicWithRequest(argoResp, anthReq.Model, anthReq)
	if err := writer.start(); err != nil {
		return "", err
	}
	for i, block := range anthResp.Content {
		switch block.Type {
		case "text":
			if err := emitSimulatedTextChunks(ctx, block.Text, writer.WriteTextDelta); err != nil {
				return "", err
			}
		case "tool_use":
			if block.ToolType == "custom" {
				if err := writer.WriteCustomToolCallDelta(i, block.ID, block.Name, block.InputString); err != nil {
					return "", err
				}
				continue
			}
			args := "{}"
			if len(block.Input) > 0 {
				if encoded, err := json.Marshal(block.Input); err == nil {
					args = string(encoded)
				}
			}
			if err := writer.WriteFunctionCallDelta(i, block.ID, block.Name, args); err != nil {
				return "", err
			}
		}
	}
	writer.SetUsage(AnthropicUsageToOpenAI(anthResp.Usage))
	return MapStopReasonToOpenAIFinishReason(anthResp.StopReason), nil
}

func (s *Server) convertAnthropicStreamToResponses(ctx context.Context, body io.Reader, writer *responsesStreamWriter) (string, error) {
	finishReason := "stop"
	type pendingCustomToolBlock struct {
		callID string
		name   string
		input  string
	}
	customToolBlocks := make(map[int]*pendingCustomToolBlock)
	if err := consumeSSEStream(body, func(currentEvent string, data json.RawMessage) error {
		warnAnthropicStreamEventFields(ctx, currentEvent, data)
		switch currentEvent {
		case EventMessageStart:
			var evt MessageStartEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				return err
			}
			writer.serviceTier = evt.Message.ServiceTier
			writer.SetUsage(AnthropicUsageToOpenAI(evt.Message.Usage))
		case EventContentBlockStart:
			var evt ContentBlockStartEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				return err
			}
			switch evt.ContentBlock.Type {
			case "tool_use":
				if _, ok := writer.toolNameRegistry.resolve(evt.ContentBlock.Name, "custom"); ok {
					customToolBlocks[evt.Index] = &pendingCustomToolBlock{
						callID: evt.ContentBlock.ID,
						name:   evt.ContentBlock.Name,
					}
					return nil
				}
				return writer.WriteFunctionCallDelta(evt.Index, evt.ContentBlock.ID, evt.ContentBlock.Name, "")
			case "thinking", "redacted_thinking":
				return writer.StartReasoningBlock(evt.Index, evt.ContentBlock)
			}
		case EventContentBlockDelta:
			var evt ContentBlockDeltaEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				return err
			}
			switch evt.Delta.Type {
			case "text_delta":
				return writer.WriteTextDelta(evt.Delta.Text)
			case "input_json_delta":
				partial := ""
				if evt.Delta.PartialJSON != nil {
					partial = *evt.Delta.PartialJSON
				}
				if state, ok := customToolBlocks[evt.Index]; ok {
					state.input += partial
					return nil
				}
				return writer.WriteFunctionCallDelta(evt.Index, "", "", partial)
			case "thinking_delta", "signature_delta":
				return writer.WriteReasoningDelta(evt.Index, evt.Delta)
			}
		case EventContentBlockStop:
			var evt ContentBlockStopEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				return err
			}
			if state, ok := customToolBlocks[evt.Index]; ok {
				delete(customToolBlocks, evt.Index)
				return writer.WriteCustomToolCallDelta(evt.Index, state.callID, state.name, anthropicCustomToolInputFromJSON(state.input))
			}
			return writer.CloseReasoningBlock(evt.Index, "completed")
		case EventMessageDelta:
			var evt anthropicStreamMessageDeltaEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				return err
			}
			if evt.Delta.StopReason != "" {
				finishReason = MapStopReasonToOpenAIFinishReason(evt.Delta.StopReason)
			}
			if evt.Usage != nil {
				writer.SetUsageCounts(evt.Usage.InputTokens, evt.Usage.OutputTokens)
			}
		case EventError:
			var evt ErrorEvent
			if err := json.Unmarshal(data, &evt); err != nil {
				return err
			}
			return fmt.Errorf("upstream stream error: %s", evt.Error.Message)
		}
		return nil
	}); err != nil {
		return "", err
	}
	return finishReason, nil
}

func (s *Server) convertOpenAIChatStreamToResponses(ctx context.Context, body io.Reader, writer *responsesStreamWriter) (string, error) {
	finishReason := "stop"
	toolDeltas := newOpenAIChatToolDeltaBuffer(writer)
	scanner := NewSSEScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == OpenAIDoneMarker {
			break
		}
		if err := core.ParseOpenAIStreamErrorChunk([]byte(data)); err != nil {
			return "", err
		}
		warnUnknownFields(ctx, []byte(data), OpenAIStreamChunk{}, "OpenAI chat stream chunk")
		parsed, err := core.ParseOpenAIStreamChunk([]byte(data))
		if err != nil {
			return "", err
		}
		writer.SetUsageCounts(parsed.Usage.InputTokens, parsed.Usage.OutputTokens)
		if err := writer.WriteTextDelta(parsed.Content); err != nil {
			return "", err
		}
		for _, tc := range parsed.ToolCalls {
			if err := toolDeltas.Apply(tc); err != nil {
				return "", err
			}
		}
		if parsed.FinishReason != "" {
			finishReason = parsed.FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if err := toolDeltas.Flush(); err != nil {
		return "", err
	}
	return finishReason, nil
}

type openAIChatToolDeltaBuffer struct {
	writer *responsesStreamWriter
	states map[int]*openAIChatToolDeltaState
}

type openAIChatToolDeltaState struct {
	id        string
	name      string
	arguments string
	custom    bool
	started   bool
}

func newOpenAIChatToolDeltaBuffer(writer *responsesStreamWriter) *openAIChatToolDeltaBuffer {
	return &openAIChatToolDeltaBuffer{
		writer: writer,
		states: make(map[int]*openAIChatToolDeltaState),
	}
}

func (b *openAIChatToolDeltaBuffer) Apply(tc core.ParsedOpenAIStreamToolCall) error {
	if tc.Type == "custom" {
		return b.writer.WriteCustomToolCallDelta(tc.Index, tc.ID, tc.Name, tc.Input)
	}

	state := b.state(tc.Index)
	if tc.ID != "" {
		state.id = tc.ID
	}
	if state.custom {
		if tc.Name != "" {
			state.name = tc.Name
		}
		state.arguments += tc.Arguments
		return nil
	}
	if state.started {
		return b.writer.WriteFunctionCallDelta(tc.Index, tc.ID, tc.Name, tc.Arguments)
	}
	if tc.Name == "" {
		state.arguments += tc.Arguments
		return nil
	}

	state.name = tc.Name
	if isResponsesCustomToolName(b.writer.toolNameRegistry, tc.Name) {
		state.custom = true
		state.arguments += tc.Arguments
		return nil
	}

	id := tc.ID
	if id == "" {
		id = state.id
	}
	if state.arguments != "" {
		if err := b.writer.WriteFunctionCallDelta(tc.Index, id, tc.Name, state.arguments); err != nil {
			return err
		}
		state.arguments = ""
	}
	state.started = true
	return b.writer.WriteFunctionCallDelta(tc.Index, id, tc.Name, tc.Arguments)
}

func (b *openAIChatToolDeltaBuffer) Flush() error {
	indexes := make([]int, 0, len(b.states))
	for index := range b.states {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	for _, index := range indexes {
		state := b.states[index]
		if state == nil || state.started {
			continue
		}
		if state.custom {
			if err := b.writer.WriteCustomToolCallDelta(index, state.id, state.name, anthropicCustomToolInputFromJSON(state.arguments)); err != nil {
				return err
			}
			continue
		}
		if err := b.writer.WriteFunctionCallDelta(index, state.id, state.name, state.arguments); err != nil {
			return err
		}
	}
	return nil
}

func (b *openAIChatToolDeltaBuffer) state(index int) *openAIChatToolDeltaState {
	state := b.states[index]
	if state == nil {
		state = &openAIChatToolDeltaState{}
		b.states[index] = state
	}
	return state
}

func (s *Server) convertGoogleStreamToResponses(ctx context.Context, body io.Reader, writer *responsesStreamWriter) (string, error) {
	finishReason := "stop"
	toolIndex := 0
	if err := consumeSSEStream(body, func(_ string, raw json.RawMessage) error {
		warnUnknownFields(ctx, raw, GoogleResponse{}, "Google stream chunk")
		parsed, err := core.ParseGoogleStreamChunk(raw)
		if err != nil {
			return err
		}
		writer.SetUsageCounts(parsed.Usage.InputTokens, parsed.Usage.OutputTokens)
		for _, text := range parsed.TextParts {
			if err := writer.WriteTextDelta(text); err != nil {
				return err
			}
		}
		for _, call := range parsed.FunctionCalls {
			args := string(call.Args)
			if args == "" {
				args = "{}"
			}
			id := call.ID
			if id == "" {
				id = generateToolUseID()
			}
			if _, ok := writer.toolNameRegistry.resolve(call.Name, "custom"); ok {
				if err := writer.WriteCustomToolCallDelta(toolIndex, id, call.Name, anthropicCustomToolInputFromJSON(args)); err != nil {
					return err
				}
				toolIndex++
				continue
			}
			if err := writer.WriteFunctionCallDelta(toolIndex, id, call.Name, args); err != nil {
				return err
			}
			toolIndex++
		}
		if parsed.FinishReason != "" {
			finishReason = mapGoogleFinishReason(parsed.FinishReason)
		}
		return nil
	}); err != nil {
		return "", err
	}
	return finishReason, nil
}

func logResponsesStreamError(ctx context.Context, err error) {
	if err != nil {
		logger.From(ctx).Errorf("Responses stream failed: %v", err)
	}
}

func isResponsesCustomToolName(registry responseToolNameRegistry, name string) bool {
	_, ok := registry.resolve(name, "custom")
	return ok
}
