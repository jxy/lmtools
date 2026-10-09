package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/http"
	"strings"
)

// buildOpenAIResponsesChatRequest renders a Responses API request. The payload and
// auth always use the OpenAI wire provider, but the endpoint is resolved for the
// configured provider so Argo requests reach Argo's own /v1/responses route.
func buildOpenAIResponsesChatRequest(cfg RequestOptions, typedMessages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	payload, err := PrepareRequestPayloadWithSystemExplicit(constants.ProviderOpenAI, model, typedMessages, system, systemExplicit, toolDefs, toolChoice, stream)
	if err != nil {
		return nil, nil, err
	}
	inlineSystem, rest := splitSystem(payload.Messages)
	if !systemExplicit && inlineSystem != "" {
		payload.System = inlineSystem
	} else if payload.System == "" {
		payload.System = inlineSystem
	}
	payload.Messages = rest
	applyOutputOptionsFromConfig(&payload, cfg)

	reqMap := openAIResponsesRequestMap(payload)
	body, err := json.Marshal(reqMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal OpenAI responses request: %w", err)
	}

	url, err := providers.ResolveResponsesURL(getProviderWithDefault(cfg, constants.ProviderArgo), cfg.ProviderURL, cfg.Env)
	if err != nil {
		return nil, nil, err
	}
	return buildProviderRequest(cfg, url, body, constants.ProviderOpenAI, stream)
}

func openAIResponsesRequestMap(payload PreparedRequestPayload) map[string]interface{} {
	reqMap := map[string]interface{}{
		"model":  payload.Model,
		"input":  OpenAIResponsesInput(payload.Messages),
		"stream": payload.Stream,
	}
	if payload.MaxTokens > 0 {
		reqMap["max_output_tokens"] = payload.MaxTokens
	}
	if payload.System != "" {
		reqMap["instructions"] = payload.System
	}
	if payload.Tools != nil {
		reqMap["tools"] = openAIResponsesToolsFromOpenAITools(payload.Tools)
	}
	if payload.ToolChoice != nil {
		reqMap["tool_choice"] = openAIResponsesToolChoice(payload.ToolChoice)
	}
	if reasoning := openAIResponsesReasoningConfig(payload); reasoning != nil {
		reqMap["reasoning"] = reasoning
		if _, hasEffort := reasoning["effort"]; hasEffort && payload.Tools != nil {
			reqMap["include"] = []string{"reasoning.encrypted_content"}
		}
	}
	if text := openAIResponsesTextConfig(payload); text != nil {
		reqMap["text"] = text
	}
	return reqMap
}

// openAIResponsesReasoningConfig builds the OpenAI Responses `reasoning` object
// from effort, mode, and context. reasoning.mode and reasoning.context are
// OpenAI Responses-only controls and only apply on this path.
func openAIResponsesReasoningConfig(payload PreparedRequestPayload) map[string]interface{} {
	reasoning := map[string]interface{}{}
	if effort := openAIReasoningEffort(payload.Effort); effort != "" {
		reasoning["effort"] = effort
	}
	if mode := openAIReasoningMode(payload.ReasoningMode); mode != "" {
		reasoning["mode"] = mode
	}
	if reasoningContext := openAIReasoningContext(payload.ReasoningContext); reasoningContext != "" {
		reasoning["context"] = reasoningContext
	}
	if len(reasoning) == 0 {
		return nil
	}
	return reasoning
}

func openAIReasoningMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "standard", "pro":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return ""
	}
}

func openAIReasoningContext(reasoningContext string) string {
	switch strings.ToLower(strings.TrimSpace(reasoningContext)) {
	case "auto", "current_turn", "all_turns":
		return strings.ToLower(strings.TrimSpace(reasoningContext))
	default:
		return ""
	}
}

type OpenAIResponsesInputProjectionItem struct {
	Item         interface{}
	MessageIndex int
	BlockIndexes []int
}

func OpenAIResponsesInput(messages []TypedMessage) []interface{} {
	projected := OpenAIResponsesInputProjection(messages)
	input := make([]interface{}, 0, len(projected))
	for _, item := range projected {
		input = append(input, item.Item)
	}
	return input
}

func OpenAIResponsesInputProjection(messages []TypedMessage) []OpenAIResponsesInputProjectionItem {
	projected := make([]OpenAIResponsesInputProjectionItem, 0, len(messages))
	for msgIndex, msg := range messages {
		var content []map[string]interface{}
		var contentBlockIndexes []int
		flushContent := func() {
			if len(content) == 0 {
				return
			}
			projected = append(projected, OpenAIResponsesInputProjectionItem{
				Item: map[string]interface{}{
					"type":    "message",
					"role":    msg.Role,
					"content": content,
				},
				MessageIndex: msgIndex,
				BlockIndexes: append([]int(nil), contentBlockIndexes...),
			})
			content = nil
			contentBlockIndexes = nil
		}
		appendToolUse := func(blockIndex int, value ToolUseBlock) {
			flushContent()
			projected = append(projected, OpenAIResponsesInputProjectionItem{
				Item:         openAIResponsesToolCallItem(value),
				MessageIndex: msgIndex,
				BlockIndexes: []int{blockIndex},
			})
		}
		appendToolResult := func(blockIndex int, value ToolResultBlock) {
			flushContent()
			projected = append(projected, OpenAIResponsesInputProjectionItem{
				Item:         openAIResponsesToolCallOutputItem(value),
				MessageIndex: msgIndex,
				BlockIndexes: []int{blockIndex},
			})
		}
		appendReasoning := func(blockIndex int, value ReasoningBlock) {
			flushContent()
			if item := openAIResponsesReasoningItem(value); item != nil {
				projected = append(projected, OpenAIResponsesInputProjectionItem{
					Item:         item,
					MessageIndex: msgIndex,
					BlockIndexes: []int{blockIndex},
				})
			}
		}
		for blockIndex, block := range msg.Blocks {
			switch value := block.(type) {
			case TextBlock:
				if value.Text != "" {
					content = append(content, openAIResponsesTextPart(msg.Role, value.Text))
					contentBlockIndexes = append(contentBlockIndexes, blockIndex)
				}
			case *TextBlock:
				if value != nil && value.Text != "" {
					content = append(content, openAIResponsesTextPart(msg.Role, value.Text))
					contentBlockIndexes = append(contentBlockIndexes, blockIndex)
				}
			case ImageBlock:
				content = append(content, openAIResponsesImagePart(value))
				contentBlockIndexes = append(contentBlockIndexes, blockIndex)
			case *ImageBlock:
				if value != nil {
					content = append(content, openAIResponsesImagePart(*value))
					contentBlockIndexes = append(contentBlockIndexes, blockIndex)
				}
			case FileBlock:
				content = append(content, openAIResponsesFilePart(value))
				contentBlockIndexes = append(contentBlockIndexes, blockIndex)
			case *FileBlock:
				if value != nil {
					content = append(content, openAIResponsesFilePart(*value))
					contentBlockIndexes = append(contentBlockIndexes, blockIndex)
				}
			case ToolUseBlock:
				appendToolUse(blockIndex, value)
			case *ToolUseBlock:
				if value != nil {
					appendToolUse(blockIndex, *value)
				}
			case ToolResultBlock:
				appendToolResult(blockIndex, value)
			case *ToolResultBlock:
				if value != nil {
					appendToolResult(blockIndex, *value)
				}
			case ReasoningBlock:
				appendReasoning(blockIndex, value)
			case *ReasoningBlock:
				if value != nil {
					appendReasoning(blockIndex, *value)
				}
			}
		}
		flushContent()
	}
	return projected
}

func openAIResponsesReasoningItem(block ReasoningBlock) map[string]interface{} {
	if block.Provider != "openai" || block.Type == "reasoning_content" {
		return nil
	}
	if len(block.Raw) > 0 {
		if raw := rawMessageToMap(block.Raw); raw != nil {
			// Responses may include status on output reasoning items, but status is
			// not accepted when the item is replayed as request input.
			delete(raw, "status")
			return raw
		}
	}
	item := map[string]interface{}{
		"type": "reasoning",
	}
	if block.ID != "" {
		item["id"] = block.ID
	}
	if len(block.Summary) > 0 {
		item["summary"] = rawMessageToInterface(block.Summary)
	}
	if block.EncryptedContent != "" {
		item["encrypted_content"] = block.EncryptedContent
	}
	return item
}

func openAIResponsesTextPart(role, text string) map[string]interface{} {
	partType := "input_text"
	if role == string(RoleAssistant) {
		partType = "output_text"
	}
	return map[string]interface{}{"type": partType, "text": text}
}

func openAIResponsesImagePart(block ImageBlock) map[string]interface{} {
	part := map[string]interface{}{
		"type":      "input_image",
		"image_url": block.URL,
	}
	if block.Detail != "" {
		part["detail"] = block.Detail
	}
	return part
}

func openAIResponsesFilePart(block FileBlock) map[string]interface{} {
	return map[string]interface{}{
		"type":    "input_file",
		"file_id": block.FileID,
	}
}

func openAIResponsesFunctionCallItem(block ToolUseBlock) map[string]interface{} {
	item := map[string]interface{}{
		"type":      "function_call",
		"call_id":   block.ID,
		"name":      block.Name,
		"arguments": string(block.Input),
	}
	if block.Namespace != "" {
		item["namespace"] = block.Namespace
		if block.OriginalName != "" {
			item["name"] = block.OriginalName
		}
	}
	return item
}

func openAIResponsesToolCallItem(block ToolUseBlock) map[string]interface{} {
	if block.Type == "custom" {
		item := map[string]interface{}{
			"type":    "custom_tool_call",
			"call_id": block.ID,
			"name":    block.Name,
			"input":   CustomToolRawInput(block.InputString, block.Input),
		}
		if block.Namespace != "" {
			item["namespace"] = block.Namespace
			if block.OriginalName != "" {
				item["name"] = block.OriginalName
			}
		}
		return item
	}
	return openAIResponsesFunctionCallItem(block)
}

func openAIResponsesToolCallOutputItem(block ToolResultBlock) map[string]interface{} {
	itemType := "function_call_output"
	if block.Type == "custom" {
		itemType = "custom_tool_call_output"
	}
	item := map[string]interface{}{
		"type":    itemType,
		"call_id": block.ToolUseID,
		"output":  block.Content,
	}
	// A function output may be a content list, which is how an image travels
	// back with the text. A custom tool output is documented as a string
	// alone, and no path in this repository puts an image on one.
	if len(block.Images) > 0 && itemType == "function_call_output" {
		item["output"] = openAIResponsesToolOutputParts(block)
	}
	return item
}

// openAIResponsesToolOutputParts is the content-list form of a function
// output: the text as an input_text part, then one input_image per image.
func openAIResponsesToolOutputParts(block ToolResultBlock) []map[string]interface{} {
	parts := make([]map[string]interface{}, 0, len(block.Images)+1)
	if block.Content != "" {
		parts = append(parts, map[string]interface{}{"type": "input_text", "text": block.Content})
	}
	for _, image := range block.Images {
		parts = append(parts, openAIResponsesImagePart(image))
	}
	return parts
}

func openAIResponsesToolsFromOpenAITools(raw interface{}) []map[string]interface{} {
	tools, ok := raw.([]OpenAITool)
	if !ok || len(tools) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		if tool.Type == "custom" {
			if tool.Custom == nil {
				continue
			}
			item := map[string]interface{}{
				"type": "custom",
				"name": tool.Custom.Name,
			}
			if tool.Custom.Description != "" {
				item["description"] = tool.Custom.Description
			}
			if tool.Custom.Format != nil {
				item["format"] = OpenAIResponsesCustomToolFormat(tool.Custom.Format)
			}
			result = append(result, item)
			continue
		}
		if tool.Type != "" && tool.Type != "function" {
			continue
		}
		item := map[string]interface{}{
			"type":        "function",
			"name":        tool.Function.Name,
			"description": tool.Function.Description,
			"parameters":  rawMessageToInterface(tool.Function.Parameters),
		}
		if tool.Function.Strict != nil {
			item["strict"] = *tool.Function.Strict
		}
		result = append(result, item)
	}
	return result
}

// OpenAIResponsesCustomToolFormat renders a custom tool format in Responses shape.
func OpenAIResponsesCustomToolFormat(format interface{}) interface{} {
	formatMap, ok := customToolFormatMap(format)
	if !ok {
		return format
	}
	out := make(map[string]interface{}, len(formatMap))
	for key, value := range formatMap {
		out[key] = value
	}
	if out["type"] != "grammar" {
		return out
	}
	grammar, ok := customToolFormatMap(out["grammar"])
	if !ok {
		return out
	}
	delete(out, "grammar")
	if syntax, ok := grammar["syntax"]; ok {
		out["syntax"] = syntax
	}
	if definition, ok := grammar["definition"]; ok {
		out["definition"] = definition
	}
	return out
}

func openAIResponsesToolChoice(raw interface{}) interface{} {
	switch choice := raw.(type) {
	case string:
		return choice
	case OpenAIToolChoice:
		if choice.Custom != nil && choice.Custom.Name != "" {
			return map[string]interface{}{"type": "custom", "name": choice.Custom.Name}
		}
		if choice.Function != nil && choice.Function.Name != "" {
			return map[string]interface{}{"type": "function", "name": choice.Function.Name}
		}
		if choice.Type != "" {
			return choice.Type
		}
	case *OpenAIToolChoice:
		if choice != nil {
			return openAIResponsesToolChoice(*choice)
		}
	}
	return raw
}

func openAIResponsesTextConfig(payload PreparedRequestPayload) map[string]interface{} {
	if len(payload.JSONSchema) > 0 {
		return map[string]interface{}{
			"format": map[string]interface{}{
				"type":   "json_schema",
				"name":   "response",
				"schema": rawMessageToInterface(payload.JSONSchema),
			},
		}
	}
	if payload.JSONMode {
		return map[string]interface{}{
			"format": map[string]interface{}{"type": "json_object"},
		}
	}
	return nil
}

func rawMessageToInterface(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var decoded interface{}
	if err := decodeJSONNumbers(raw, &decoded); err != nil {
		return raw
	}
	return decoded
}

func rawMessageToMap(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]interface{}
	if err := decodeJSONNumbers(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

// decodeJSONNumbers preserves schema constants and provider metadata without
// passing numbers through float64. Like json.Unmarshal, it rejects extra values.
func decodeJSONNumbers(raw []byte, value interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("unexpected data after JSON value")
	}
	return nil
}

func parseOpenAIResponses(data []byte, isEmbed bool) (Response, error) {
	if isEmbed {
		return parseOpenAIResponse(data, true)
	}
	text, toolCalls, blocks, err := parseOpenAIResponsesDetailed(data)
	response := Response{Text: text, ToolCalls: toolCalls, Blocks: blocks}
	if err == nil {
		var metadata struct {
			openAIResponsesCompletion
			Output []struct {
				Status string `json:"status"`
			} `json:"output"`
		}
		if err = json.Unmarshal(data, &metadata); err == nil {
			response.IncompleteReason = metadata.incompleteReason()
			for _, item := range metadata.Output {
				if item.Status != "" && item.Status != "completed" && response.IncompleteReason == "" {
					response.IncompleteReason = "incomplete"
				}
			}
		}
	}
	return response, err
}

func parseOpenAIResponsesWithTools(data []byte) (string, []ToolCall, error) {
	text, toolCalls, _, err := parseOpenAIResponsesDetailed(data)
	return text, toolCalls, err
}

func parseOpenAIResponsesDetailed(data []byte) (string, []ToolCall, []Block, error) {
	var resp struct {
		openAIResponsesCompletion
		OutputText string            `json:"output_text"`
		Output     []json.RawMessage `json:"output"`
		Error      *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", nil, nil, fmt.Errorf("failed to decode OpenAI responses payload: %w", err)
	}
	if resp.Error != nil {
		return "", nil, nil, fmt.Errorf("API error: %s (type: %s, code: %s)", resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	var textParts []string
	var toolCalls []ToolCall
	var blocks []Block
	for _, rawItem := range resp.Output {
		var item openAIResponsesOutputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return "", nil, nil, err
		}
		for _, block := range openAIResponsesOutputItemBlocks(rawItem) {
			blocks = append(blocks, block)
			switch value := block.(type) {
			case TextBlock:
				textParts = append(textParts, value.Text)
			case ToolUseBlock:
				if resp.incompleteReason() == "" && (item.Status == "" || item.Status == "completed") {
					toolCalls = append(toolCalls, ToolCall{
						ID: value.ID, Type: value.Type, Name: value.Name,
						Args: value.Input, Input: value.InputString,
					})
				}
			}
		}
	}
	if len(textParts) == 0 && resp.OutputText != "" {
		textParts = append(textParts, resp.OutputText)
		blocks = append(blocks, TextBlock{Text: resp.OutputText})
	}
	return strings.Join(textParts, ""), toolCalls, blocks, nil
}

// NormalizeOpenAIResponsesArguments returns valid JSON for Responses tool-call arguments.
func NormalizeOpenAIResponsesArguments(arguments string) json.RawMessage {
	if strings.TrimSpace(arguments) == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(arguments)) {
		return normalizeOpenAIToolArguments(json.RawMessage(arguments))
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return normalizeOpenAIToolArguments(encoded)
}

type openAIResponsesOutputItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Status           string          `json:"status"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	Input            string          `json:"input"`
	Summary          json.RawMessage `json:"summary"`
	EncryptedContent string          `json:"encrypted_content"`
	Content          []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

func openAIResponsesOutputItemBlocks(rawItem json.RawMessage) []Block {
	var item openAIResponsesOutputItem
	if err := json.Unmarshal(rawItem, &item); err != nil {
		return nil
	}
	switch item.Type {
	case "reasoning":
		return []Block{ReasoningBlock{
			Provider:         "openai",
			Type:             "reasoning",
			ID:               item.ID,
			Status:           item.Status,
			Summary:          append(json.RawMessage(nil), item.Summary...),
			EncryptedContent: item.EncryptedContent,
			Content:          openAIResponsesRawContent(rawItem),
			Raw:              append(json.RawMessage(nil), rawItem...),
		}}
	case "message":
		blocks := make([]Block, 0, len(item.Content))
		for _, content := range item.Content {
			var text string
			switch content.Type {
			case "output_text", "text":
				text = content.Text
			case "refusal", "output_refusal":
				text = content.Refusal
				if text == "" {
					text = content.Text
				}
			}
			if text != "" {
				blocks = append(blocks, TextBlock{Text: text})
			}
		}
		return blocks
	case "function_call":
		id := item.CallID
		if id == "" {
			id = item.ID
		}
		return []Block{ToolUseBlock{
			ID:    id,
			Name:  item.Name,
			Input: NormalizeOpenAIResponsesArguments(item.Arguments),
		}}
	case "custom_tool_call":
		id := item.CallID
		if id == "" {
			id = item.ID
		}
		return []Block{ToolUseBlock{
			ID:          id,
			Type:        "custom",
			Name:        item.Name,
			Input:       jsonStringRawMessage(item.Input),
			InputString: item.Input,
		}}
	default:
		return nil
	}
}

func openAIResponsesRawContent(raw json.RawMessage) json.RawMessage {
	var item struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return nil
	}
	return item.Content
}
