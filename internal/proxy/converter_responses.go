package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"strings"
	"time"
)

const (
	defaultResponsesClaudeOpusMaxTokens    = 128000
	defaultResponsesClaudeDefaultMaxTokens = 64000
)

func OpenAIResponsesRequestToTyped(ctx context.Context, req *OpenAIResponsesRequest) (TypedRequest, error) {
	if req == nil {
		return TypedRequest{}, fmt.Errorf("request is required")
	}
	typed := TypedRequest{
		MaxTokens:       req.MaxOutputTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		Stream:          req.Stream,
		Metadata:        cloneStringInterfaceMap(req.Metadata),
		ServiceTier:     req.ServiceTier,
		ReasoningEffort: "",
	}
	if req.Reasoning != nil {
		typed.ReasoningEffort = req.Reasoning.Effort
	}
	if req.User != "" {
		if typed.Metadata == nil {
			typed.Metadata = map[string]interface{}{}
		}
		typed.Metadata["user_id"] = req.User
	} else if req.SafetyIdentifier != "" {
		if typed.Metadata == nil {
			typed.Metadata = map[string]interface{}{}
		}
		typed.Metadata["user_id"] = req.SafetyIdentifier
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		if typed.Metadata == nil {
			typed.Metadata = map[string]interface{}{}
		}
		typed.Metadata[constants.IncludeUsageKey] = true
	}
	if req.Instructions != nil {
		typed.Developer = responsesInstructionText(req.Instructions)
	}
	if req.Text != nil {
		typed.ResponseFormat = responsesTextToResponseFormat(req.Text)
	}
	if req.Prompt != nil {
		return TypedRequest{}, fmt.Errorf("prompt is only supported for direct OpenAI Responses passthrough")
	}

	messages, err := responsesInputToTypedMessages(req.Input)
	if err != nil {
		return TypedRequest{}, err
	}
	typed.Messages = messages
	tools, err := responsesToolsToCore(ctx, req.Tools)
	if err != nil {
		return TypedRequest{}, err
	}
	typed.Tools = tools
	typed.ToolChoice = responsesToolChoiceToCore(ctx, req.ToolChoice)
	return typed, nil
}

func ensureResponsesAnthropicMaxTokens(typed TypedRequest, model string) TypedRequest {
	if typed.MaxTokens != nil {
		return typed
	}
	maxTokens := defaultResponsesClaudeMaxTokens(model)
	typed.MaxTokens = &maxTokens
	return typed
}

func ensureResponsesAnthropicWireMaxTokens(typed TypedRequest, provider, model string) TypedRequest {
	if !responsesProviderRequiresClaudeMaxTokens(provider, model) {
		return typed
	}
	return ensureResponsesAnthropicMaxTokens(typed, model)
}

func responsesProviderRequiresClaudeMaxTokens(provider, model string) bool {
	switch constants.NormalizeProvider(provider) {
	case constants.ProviderAnthropic:
		return true
	case constants.ProviderArgo:
		return providers.DetermineArgoModelProvider(model) == constants.ProviderAnthropic
	default:
		return false
	}
}

func defaultResponsesClaudeMaxTokens(model string) int {
	if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "opus") {
		return defaultResponsesClaudeOpusMaxTokens
	}
	return defaultResponsesClaudeDefaultMaxTokens
}

func responsesInputToTypedMessages(input interface{}) ([]core.TypedMessage, error) {
	switch value := input.(type) {
	case nil:
		return nil, nil
	case string:
		if value == "" {
			return nil, nil
		}
		return []core.TypedMessage{core.NewTextMessage(string(core.RoleUser), value)}, nil
	case []interface{}:
		messages := make([]core.TypedMessage, 0, len(value))
		toolNamesByCallID := make(map[string]string)
		for _, rawItem := range value {
			msgs, err := responsesInputItemToTypedMessages(rawItem, toolNamesByCallID)
			if err != nil {
				return nil, err
			}
			messages = append(messages, msgs...)
		}
		return messages, nil
	default:
		return nil, fmt.Errorf("input must be a string or array")
	}
}

func responsesInputItemToTypedMessages(rawItem interface{}, toolNamesByCallID map[string]string) ([]core.TypedMessage, error) {
	item, ok := rawItem.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("input item must be an object")
	}
	itemType, _ := item["type"].(string)
	if itemType == "" {
		if _, ok := item["role"].(string); ok {
			itemType = "message"
		}
	}
	switch itemType {
	case "message":
		role, _ := item["role"].(string)
		if role == "" {
			role = string(core.RoleUser)
		}
		blocks := responsesContentToBlocks(role, item["content"])
		return []core.TypedMessage{{Role: role, Blocks: blocks}}, nil
	case "function_call":
		callID, _ := item["call_id"].(string)
		if callID == "" {
			callID, _ = item["id"].(string)
		}
		name, _ := item["name"].(string)
		namespace, _ := item["namespace"].(string)
		originalName := name
		if namespace != "" {
			name = flattenNamespaceToolName(namespace, name)
		}
		if callID != "" && name != "" {
			toolNamesByCallID[callID] = name
		}
		arguments, _ := item["arguments"].(string)
		return []core.TypedMessage{{
			Role: string(core.RoleAssistant),
			Blocks: []core.Block{core.ToolUseBlock{
				ID:           callID,
				Type:         "function",
				Namespace:    namespace,
				OriginalName: originalName,
				Name:         name,
				Input:        normalizeResponsesArguments(arguments),
			}},
		}}, nil
	case "custom_tool_call":
		callID, _ := item["call_id"].(string)
		if callID == "" {
			callID, _ = item["id"].(string)
		}
		name, _ := item["name"].(string)
		namespace, _ := item["namespace"].(string)
		originalName := name
		if namespace != "" {
			name = flattenNamespaceToolName(namespace, name)
		}
		if callID != "" && name != "" {
			toolNamesByCallID[callID] = name
		}
		input := responseCustomToolInput(item["input"])
		return []core.TypedMessage{{
			Role: string(core.RoleAssistant),
			Blocks: []core.Block{core.ToolUseBlock{
				ID:           callID,
				Type:         "custom",
				Namespace:    namespace,
				OriginalName: originalName,
				Name:         name,
				Input:        mustMarshalJSON(input),
				InputString:  input,
			}},
		}}, nil
	case "function_call_output":
		callID, _ := item["call_id"].(string)
		status, _ := item["status"].(string)
		return []core.TypedMessage{{
			Role: string(core.RoleUser),
			Blocks: []core.Block{core.ToolResultBlock{
				ToolUseID: callID,
				Name:      toolNamesByCallID[callID],
				Content:   responsesFunctionCallOutputText(item["output"]),
				IsError:   status == "incomplete",
			}},
		}}, nil
	case "custom_tool_call_output":
		callID, _ := item["call_id"].(string)
		status, _ := item["status"].(string)
		return []core.TypedMessage{{
			Role: string(core.RoleUser),
			Blocks: []core.Block{core.ToolResultBlock{
				ToolUseID: callID,
				Type:      "custom",
				Name:      toolNamesByCallID[callID],
				Content:   responsesFunctionCallOutputText(item["output"]),
				IsError:   status == "incomplete",
			}},
		}}, nil
	case "reasoning":
		raw, _ := json.Marshal(item)
		id, _ := item["id"].(string)
		status, _ := item["status"].(string)
		encryptedContent, _ := item["encrypted_content"].(string)
		summary := mustMarshalJSON(item["summary"])
		return []core.TypedMessage{{
			Role: string(core.RoleAssistant),
			Blocks: []core.Block{core.ReasoningBlock{
				Provider:         "openai",
				Type:             "reasoning",
				ID:               id,
				Status:           status,
				Summary:          summary,
				EncryptedContent: encryptedContent,
				Raw:              raw,
			}},
		}}, nil
	case "compaction":
		encryptedContent, _ := item["encrypted_content"].(string)
		if encryptedContent == "" {
			return nil, nil
		}
		return []core.TypedMessage{core.NewTextMessage(string(core.RoleAssistant), "Compacted conversation state:\n"+encryptedContent)}, nil
	default:
		return nil, nil
	}
}

func responsesFunctionCallOutputText(output interface{}) string {
	switch value := output.(type) {
	case nil:
		return ""
	case string:
		return value
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, rawPart := range value {
			part, ok := rawPart.(map[string]interface{})
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "input_text", "output_text", "text":
				if text, _ := part["text"].(string); text != "" {
					parts = append(parts, text)
				}
			default:
				if encoded, err := json.Marshal(part); err == nil {
					parts = append(parts, string(encoded))
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func responsesContentToBlocks(role string, content interface{}) []core.Block {
	switch value := content.(type) {
	case string:
		if value == "" {
			return nil
		}
		return []core.Block{core.TextBlock{Text: value}}
	case []interface{}:
		blocks := make([]core.Block, 0, len(value))
		for _, rawPart := range value {
			part, ok := rawPart.(map[string]interface{})
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "input_text", "output_text", "text":
				if text, _ := part["text"].(string); text != "" {
					blocks = append(blocks, core.TextBlock{Text: text})
				}
			case "refusal", "output_refusal":
				if text, _ := part["refusal"].(string); text != "" {
					blocks = append(blocks, core.TextBlock{Text: text})
				} else if text, _ := part["text"].(string); text != "" {
					blocks = append(blocks, core.TextBlock{Text: text})
				}
			case "input_image":
				url, _ := part["image_url"].(string)
				detail, _ := part["detail"].(string)
				if url != "" {
					blocks = append(blocks, core.ImageBlock{URL: url, Detail: detail})
				}
			case "input_file":
				fileID, _ := part["file_id"].(string)
				if fileID != "" {
					blocks = append(blocks, core.FileBlock{FileID: fileID})
				}
			}
		}
		return blocks
	default:
		return nil
	}
}

func responsesInstructionText(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, raw := range typed {
			partMap, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			for _, block := range responsesContentToBlocks(string(core.RoleDeveloper), partMap["content"]) {
				if text, ok := block.(core.TextBlock); ok && text.Text != "" {
					parts = append(parts, text.Text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func responsesToolsToCore(ctx context.Context, tools []map[string]interface{}) ([]core.ToolDefinition, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	result := make([]core.ToolDefinition, 0, len(tools))
	usedNames := make(map[string]struct{})
	for i, tool := range tools {
		toolType, _ := tool["type"].(string)
		switch toolType {
		case "function":
			def, err := responsesFunctionToolToCore(tool, i)
			if err != nil {
				return nil, err
			}
			if _, exists := usedNames[def.Name]; exists {
				return nil, duplicateFlattenedToolNameError(def.Name)
			}
			usedNames[def.Name] = struct{}{}
			result = append(result, def)
		case "custom":
			def, err := responsesCustomToolToCore(tool, i)
			if err != nil {
				return nil, err
			}
			if _, exists := usedNames[def.Name]; exists {
				return nil, duplicateFlattenedToolNameError(def.Name)
			}
			usedNames[def.Name] = struct{}{}
			result = append(result, def)
		case "namespace":
			namespaceTools, err := responsesNamespaceToolsToCore(tool, i, usedNames)
			if err != nil {
				return nil, err
			}
			result = append(result, namespaceTools...)
		default:
			warnDroppedResponsesTool(ctx, i, toolType)
		}
	}
	return result, nil
}

func responsesFunctionToolToCore(tool map[string]interface{}, index int) (core.ToolDefinition, error) {
	name, _ := tool["name"].(string)
	if name == "" {
		return core.ToolDefinition{}, fmt.Errorf("responses function tool at index %d is missing name", index)
	}
	description, _ := tool["description"].(string)
	return core.ToolDefinition{
		Type:        "function",
		Name:        name,
		Description: description,
		InputSchema: tool["parameters"],
		Strict:      boolPointerFromInterface(tool["strict"]),
	}, nil
}

func responsesCustomToolToCore(tool map[string]interface{}, index int) (core.ToolDefinition, error) {
	name, _ := tool["name"].(string)
	if name == "" {
		return core.ToolDefinition{}, fmt.Errorf("responses custom tool at index %d is missing name", index)
	}
	description, _ := tool["description"].(string)
	return core.ToolDefinition{
		Type:        "custom",
		Name:        name,
		Description: description,
		Format:      responsesCustomToolFormatFromChat(tool["format"]),
	}, nil
}

func responsesNamespaceToolsToCore(namespaceTool map[string]interface{}, index int, usedNames map[string]struct{}) ([]core.ToolDefinition, error) {
	namespace, _ := namespaceTool["name"].(string)
	if namespace == "" {
		return nil, fmt.Errorf("responses namespace tool at index %d is missing name", index)
	}
	namespaceDescription, _ := namespaceTool["description"].(string)
	rawTools, ok := namespaceTool["tools"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("responses namespace tool at index %d is missing tools array", index)
	}
	result := make([]core.ToolDefinition, 0, len(rawTools))
	for j, rawNested := range rawTools {
		nested, ok := rawNested.(map[string]interface{})
		if !ok {
			continue
		}
		nestedType, _ := nested["type"].(string)
		var def core.ToolDefinition
		var err error
		switch nestedType {
		case "function":
			def, err = responsesFunctionToolToCore(nested, j)
		case "custom":
			def, err = responsesCustomToolToCore(nested, j)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		originalName := def.Name
		def.Name = flattenNamespaceToolName(namespace, originalName)
		def.Namespace = namespace
		def.NamespaceDescription = namespaceDescription
		def.OriginalName = originalName
		def.OriginalDescription = def.Description
		def.Description = namespaceToolDescription(namespace, namespaceDescription, originalName, def.Description)
		if _, exists := usedNames[def.Name]; exists {
			return nil, duplicateFlattenedToolNameError(def.Name)
		}
		usedNames[def.Name] = struct{}{}
		result = append(result, def)
	}
	return result, nil
}

func responsesToolChoiceToCore(ctx context.Context, toolChoice interface{}) *core.ToolChoice {
	switch value := toolChoice.(type) {
	case nil:
		return nil
	case string:
		if value == "auto" || value == "none" || value == "required" {
			return &core.ToolChoice{Type: value}
		}
		warnDroppedResponsesToolChoice(ctx, value)
	case map[string]interface{}:
		choiceType, _ := value["type"].(string)
		if choiceType != "function" && choiceType != "custom" {
			warnDroppedResponsesToolChoice(ctx, choiceType)
			return nil
		}
		name, _ := value["name"].(string)
		if name == "" {
			return nil
		}
		if namespace, _ := value["namespace"].(string); namespace != "" {
			name = flattenNamespaceToolName(namespace, name)
		}
		return &core.ToolChoice{Type: "tool", Name: name}
	}
	return nil
}

func responsesTextToResponseFormat(text *OpenAIResponsesText) *ResponseFormat {
	if text == nil || len(text.Format) == 0 {
		return nil
	}
	formatType, _ := text.Format["type"].(string)
	switch formatType {
	case "json_object":
		return &ResponseFormat{Type: "json_object"}
	case "json_schema":
		return &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &OpenAIJSONSchema{
				Name:        stringFromInterface(text.Format["name"]),
				Description: stringFromInterface(text.Format["description"]),
				Schema:      text.Format["schema"],
				Strict:      boolPointerFromInterface(text.Format["strict"]),
			},
		}
	default:
		return nil
	}
}

func normalizeResponsesArguments(arguments string) json.RawMessage {
	if strings.TrimSpace(arguments) == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(arguments)) {
		return json.RawMessage(arguments)
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return encoded
}

func TypedToOpenAIResponsesRequest(typed TypedRequest, model string) (*OpenAIResponsesRequest, error) {
	prepared, err := prepareTypedRequestPayload(constants.ProviderOpenAI, typed, typedRenderContext{Model: model})
	if err != nil {
		return nil, err
	}
	req := &OpenAIResponsesRequest{
		Model:           model,
		Input:           coreResponsesInput(prepared.Messages),
		Instructions:    combineInstructionText(typed.System, typed.Developer),
		Tools:           proxyOpenAIResponsesToolsFromDefinitions(typed.Tools),
		ToolChoice:      proxyOpenAIResponsesToolChoiceFromDefinition(typed.ToolChoice, typed.Tools, prepared.ToolChoice),
		Text:            responseFormatToResponsesText(typed.ResponseFormat),
		Temperature:     typed.Temperature,
		TopP:            typed.TopP,
		MaxOutputTokens: typed.MaxTokens,
		Stream:          typed.Stream,
		Metadata:        cloneStringInterfaceMap(typed.Metadata),
		ServiceTier:     serviceTierForOpenAI(typed.ServiceTier),
	}
	if typed.ReasoningEffort != "" {
		req.Reasoning = &OpenAIResponsesReasoning{Effort: typed.ReasoningEffort}
	}
	if req.Instructions == "" {
		req.Instructions = nil
	}
	return req, nil
}

type coreResponsesInputProjectionItem struct {
	Item         interface{}
	MessageIndex int
	BlockIndexes []int
}

func coreResponsesInput(messages []core.TypedMessage) []interface{} {
	projected := coreResponsesInputProjection(messages)
	input := make([]interface{}, 0, len(projected))
	for _, item := range projected {
		input = append(input, item.Item)
	}
	return input
}

func coreResponsesInputProjection(messages []core.TypedMessage) []coreResponsesInputProjectionItem {
	input := make([]coreResponsesInputProjectionItem, 0, len(messages))
	for msgIndex, msg := range messages {
		var content []map[string]interface{}
		var contentBlockIndexes []int
		flushContent := func() {
			if len(content) == 0 {
				return
			}
			input = append(input, coreResponsesInputProjectionItem{
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
		for blockIndex, block := range msg.Blocks {
			switch value := block.(type) {
			case core.TextBlock:
				if value.Text != "" {
					content = append(content, responsesTextPart(msg.Role, value.Text))
					contentBlockIndexes = append(contentBlockIndexes, blockIndex)
				}
			case core.ImageBlock:
				part := map[string]interface{}{"type": "input_image", "image_url": value.URL}
				if value.Detail != "" {
					part["detail"] = value.Detail
				}
				content = append(content, part)
				contentBlockIndexes = append(contentBlockIndexes, blockIndex)
			case core.FileBlock:
				content = append(content, map[string]interface{}{"type": "input_file", "file_id": value.FileID})
				contentBlockIndexes = append(contentBlockIndexes, blockIndex)
			case core.ToolUseBlock:
				flushContent()
				if value.Type == "custom" {
					name := value.Name
					item := map[string]interface{}{
						"type":    "custom_tool_call",
						"call_id": value.ID,
						"name":    name,
						"input":   firstNonEmpty(value.InputString, rawJSONStringValue(value.Input)),
					}
					if value.Namespace != "" {
						item["namespace"] = value.Namespace
						if value.OriginalName != "" {
							item["name"] = value.OriginalName
						}
					}
					input = append(input, coreResponsesInputProjectionItem{
						Item:         item,
						MessageIndex: msgIndex,
						BlockIndexes: []int{blockIndex},
					})
					continue
				}
				name := value.Name
				item := map[string]interface{}{
					"type":      "function_call",
					"call_id":   value.ID,
					"name":      name,
					"arguments": string(value.Input),
				}
				if value.Namespace != "" {
					item["namespace"] = value.Namespace
					if value.OriginalName != "" {
						item["name"] = value.OriginalName
					}
				}
				input = append(input, coreResponsesInputProjectionItem{
					Item:         item,
					MessageIndex: msgIndex,
					BlockIndexes: []int{blockIndex},
				})
			case core.ToolResultBlock:
				flushContent()
				itemType := "function_call_output"
				if value.Type == "custom" {
					itemType = "custom_tool_call_output"
				}
				item := map[string]interface{}{
					"type":    itemType,
					"call_id": value.ToolUseID,
					"output":  value.Content,
				}
				if value.IsError {
					item["status"] = "incomplete"
				}
				input = append(input, coreResponsesInputProjectionItem{
					Item:         item,
					MessageIndex: msgIndex,
					BlockIndexes: []int{blockIndex},
				})
			case core.ReasoningBlock:
				flushContent()
				if value.Provider != "openai" || value.Type != "reasoning" {
					continue
				}
				if item := openAIReasoningBlockInputItem(value); item != nil {
					input = append(input, coreResponsesInputProjectionItem{
						Item:         item,
						MessageIndex: msgIndex,
						BlockIndexes: []int{blockIndex},
					})
				}
			}
		}
		flushContent()
	}
	return input
}

func responsesTextPart(role, text string) map[string]interface{} {
	partType := "input_text"
	if role == string(core.RoleAssistant) {
		partType = "output_text"
	}
	return map[string]interface{}{"type": partType, "text": text}
}

func openAIReasoningBlockInputItem(block core.ReasoningBlock) map[string]interface{} {
	if len(block.Raw) > 0 {
		var item map[string]interface{}
		if err := json.Unmarshal(block.Raw, &item); err == nil {
			return item
		}
	}
	item := map[string]interface{}{"type": "reasoning"}
	if block.ID != "" {
		item["id"] = block.ID
	}
	if block.Status != "" {
		item["status"] = block.Status
	}
	if len(block.Summary) > 0 {
		var summary interface{}
		if err := json.Unmarshal(block.Summary, &summary); err == nil {
			item["summary"] = summary
		}
	}
	if block.EncryptedContent != "" {
		item["encrypted_content"] = block.EncryptedContent
	}
	return item
}

func proxyOpenAIResponsesToolsFromDefinitions(tools []core.ToolDefinition) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(tools))
	namespaceGroups := make(map[string][]core.ToolDefinition)
	namespaceOrder := make([]string, 0)
	for _, tool := range tools {
		if tool.Namespace == "" {
			result = append(result, openAIResponsesToolFromDefinition(tool))
			continue
		}
		if _, exists := namespaceGroups[tool.Namespace]; !exists {
			namespaceOrder = append(namespaceOrder, tool.Namespace)
		}
		namespaceGroups[tool.Namespace] = append(namespaceGroups[tool.Namespace], tool)
	}
	for _, namespace := range namespaceOrder {
		group := namespaceGroups[namespace]
		if len(group) == 0 {
			continue
		}
		nested := make([]map[string]interface{}, 0, len(group))
		for _, tool := range group {
			nestedTool := tool
			if nestedTool.OriginalName != "" {
				nestedTool.Name = nestedTool.OriginalName
			}
			if nestedTool.OriginalDescription != "" {
				nestedTool.Description = nestedTool.OriginalDescription
			}
			nestedTool.Namespace = ""
			nested = append(nested, openAIResponsesToolFromDefinition(nestedTool))
		}
		item := map[string]interface{}{
			"type":  "namespace",
			"name":  namespace,
			"tools": nested,
		}
		if group[0].NamespaceDescription != "" {
			item["description"] = group[0].NamespaceDescription
		}
		result = append(result, item)
	}
	return result
}

func openAIResponsesToolFromDefinition(tool core.ToolDefinition) map[string]interface{} {
	toolType := tool.Type
	if toolType == "" {
		toolType = "function"
	}
	if toolType == "custom" {
		item := map[string]interface{}{
			"type": "custom",
			"name": tool.Name,
		}
		if tool.Description != "" {
			item["description"] = tool.Description
		}
		if tool.Format != nil {
			item["format"] = responsesCustomToolFormatFromChat(tool.Format)
		}
		return item
	}
	item := map[string]interface{}{
		"type":        "function",
		"name":        tool.Name,
		"description": tool.Description,
		"parameters":  filterSchemaMetadata(toolSchemaToInterface(tool.InputSchema)),
	}
	if tool.Strict != nil {
		item["strict"] = *tool.Strict
	}
	return item
}

func proxyOpenAIResponsesToolChoiceFromCore(raw interface{}) interface{} {
	switch choice := raw.(type) {
	case nil:
		return nil
	case string:
		return choice
	case core.OpenAIToolChoice:
		if choice.Custom != nil && choice.Custom.Name != "" {
			return map[string]string{"type": "custom", "name": choice.Custom.Name}
		}
		if choice.Function != nil && choice.Function.Name != "" {
			return map[string]string{"type": "function", "name": choice.Function.Name}
		}
		return choice.Type
	case *core.OpenAIToolChoice:
		if choice == nil {
			return nil
		}
		return proxyOpenAIResponsesToolChoiceFromCore(*choice)
	default:
		return nil
	}
}

func proxyOpenAIResponsesToolChoiceFromDefinition(choice *core.ToolChoice, tools []core.ToolDefinition, converted interface{}) interface{} {
	if choice == nil {
		return nil
	}
	if choice.Type == "tool" && choice.Name != "" {
		for _, tool := range tools {
			if tool.Name != choice.Name {
				continue
			}
			toolType := tool.Type
			if toolType == "" {
				toolType = "function"
			}
			name := tool.Name
			if tool.OriginalName != "" {
				name = tool.OriginalName
			}
			item := map[string]string{
				"type": toolType,
				"name": name,
			}
			if tool.Namespace != "" {
				item["namespace"] = tool.Namespace
			}
			return item
		}
	}
	return proxyOpenAIResponsesToolChoiceFromCore(converted)
}

func responseFormatToResponsesText(format *ResponseFormat) *OpenAIResponsesText {
	if format == nil {
		return nil
	}
	switch format.Type {
	case "json_object":
		return &OpenAIResponsesText{Format: map[string]interface{}{"type": "json_object"}}
	case "json_schema":
		if format.JSONSchema == nil {
			return nil
		}
		responseFormat := map[string]interface{}{
			"type":   "json_schema",
			"name":   format.JSONSchema.Name,
			"schema": format.JSONSchema.Schema,
		}
		if format.JSONSchema.Description != "" {
			responseFormat["description"] = format.JSONSchema.Description
		}
		if format.JSONSchema.Strict != nil {
			responseFormat["strict"] = *format.JSONSchema.Strict
		}
		return &OpenAIResponsesText{Format: responseFormat}
	default:
		return nil
	}
}

func (c *Converter) ConvertOpenAIResponsesToAnthropic(resp *OpenAIResponsesResponse, originalModel string) *AnthropicResponse {
	if resp == nil {
		return &AnthropicResponse{Type: "message", Model: originalModel}
	}
	anthResp := &AnthropicResponse{
		ID:          resp.ID,
		Type:        "message",
		Model:       originalModel,
		Role:        core.RoleAssistant,
		ServiceTier: resp.ServiceTier,
	}
	if resp.Usage != nil {
		anthResp.Usage = &AnthropicUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		}
	}

	var content []AnthropicContentBlock
	hasTool := false
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					content = append(content, AnthropicContentBlock{Type: "text", Text: part.Text})
				}
			}
		case "function_call":
			hasTool = true
			content = append(content, AnthropicContentBlock{
				Type:      "tool_use",
				ID:        firstNonEmpty(item.CallID, item.ID),
				Namespace: item.Namespace,
				Name:      item.Name,
				Input:     rawJSONToMap(normalizeResponsesArguments(item.Arguments)),
			})
		case "custom_tool_call":
			hasTool = true
			content = append(content, AnthropicContentBlock{
				Type:        "tool_use",
				ToolType:    "custom",
				ID:          firstNonEmpty(item.CallID, item.ID),
				Namespace:   item.Namespace,
				Name:        item.Name,
				Input:       map[string]interface{}{core.CustomToolInputField: item.Input},
				InputString: item.Input,
			})
		}
	}
	anthResp.Content = content
	if hasTool {
		anthResp.StopReason = "tool_use"
	} else if resp.Status == "incomplete" {
		anthResp.StopReason = "max_tokens"
	} else {
		anthResp.StopReason = "end_turn"
	}
	return anthResp
}

func (c *Converter) ConvertOpenAIResponsesToOpenAI(resp *OpenAIResponsesResponse, originalModel string) *OpenAIResponse {
	if resp == nil {
		return &OpenAIResponse{Object: "chat.completion", Model: originalModel}
	}
	message := OpenAIMessage{Role: core.RoleAssistant}
	var textParts []string
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					textParts = append(textParts, part.Text)
				}
			}
		case "function_call":
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:   firstNonEmpty(item.CallID, item.ID),
				Type: "function",
				Function: FunctionCall{
					Name:      responseOutputToolName(item),
					Arguments: item.Arguments,
				},
			})
		case "custom_tool_call":
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:   firstNonEmpty(item.CallID, item.ID),
				Type: "custom",
				Custom: &CustomToolCall{
					Name:  responseOutputToolName(item),
					Input: item.Input,
				},
			})
		}
	}
	if len(textParts) > 0 {
		message.Content = strings.Join(textParts, "")
	}
	finishReason := "stop"
	if len(message.ToolCalls) > 0 {
		finishReason = "tool_calls"
		if len(textParts) == 0 {
			message.Content = nil
		}
	}
	return &OpenAIResponse{
		ID:          firstNonEmpty(resp.ID, generateUUID("chatcmpl-")),
		Object:      "chat.completion",
		Created:     time.Now().Unix(),
		Model:       originalModel,
		Usage:       resp.Usage.toOpenAIUsage(),
		ServiceTier: resp.ServiceTier,
		Choices: []OpenAIChoice{{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		}},
	}
}

func (c *Converter) ConvertAnthropicResponseToOpenAIResponses(resp *AnthropicResponse, originalModel string) *OpenAIResponsesResponse {
	return c.ConvertAnthropicResponseToOpenAIResponsesWithToolNameRegistry(resp, originalModel, nil)
}

func (c *Converter) ConvertAnthropicResponseToOpenAIResponsesWithToolNameRegistry(resp *AnthropicResponse, originalModel string, registry responseToolNameRegistry) *OpenAIResponsesResponse {
	if resp == nil {
		return &OpenAIResponsesResponse{Object: "response", Model: originalModel, Status: "completed"}
	}
	status := "completed"
	itemStatus := "completed"
	var incompleteDetails interface{}
	if resp.StopReason == "max_tokens" {
		status = "incomplete"
		itemStatus = "incomplete"
		incompleteDetails = map[string]interface{}{"reason": "max_output_tokens"}
	}
	output := make([]OpenAIResponsesOutputItem, 0, len(resp.Content))
	var outputText []string
	for _, block := range resp.Content {
		switch block.Type {
		case "thinking", "redacted_thinking":
			output = append(output, OpenAIResponsesOutputItem{
				ID:      generateUUID("rs_"),
				Type:    "reasoning",
				Status:  "completed",
				Summary: anthropicThinkingToResponsesSummary(block),
			})
		case "text":
			if block.Text == "" {
				continue
			}
			outputText = append(outputText, block.Text)
			output = append(output, OpenAIResponsesOutputItem{
				ID:     generateUUID("msg_"),
				Type:   "message",
				Status: itemStatus,
				Role:   core.RoleAssistant,
				Content: []OpenAIResponsesContentPart{{
					Type: "output_text",
					Text: block.Text,
				}},
			})
		case "tool_use":
			mapping, mapped := registry.resolve(block.Name, "")
			outputName := block.Name
			namespace := block.Namespace
			toolType := block.ToolType
			if mapped {
				outputName = mapping.Name
				namespace = mapping.Namespace
				toolType = mapping.Type
			}
			if toolType == "custom" {
				output = append(output, OpenAIResponsesOutputItem{
					ID:        block.ID,
					Type:      "custom_tool_call",
					Status:    "completed",
					CallID:    block.ID,
					Namespace: namespace,
					Name:      outputName,
					Input:     anthropicCustomToolInput(block.Input, block.InputString),
				})
				continue
			}
			args := "{}"
			if len(block.Input) > 0 {
				if encoded, err := json.Marshal(block.Input); err == nil {
					args = string(encoded)
				}
			}
			output = append(output, OpenAIResponsesOutputItem{
				ID:        block.ID,
				Type:      "function_call",
				Status:    "completed",
				CallID:    block.ID,
				Namespace: namespace,
				Name:      outputName,
				Arguments: args,
			})
		}
	}
	return &OpenAIResponsesResponse{
		ID:                responsesResponseID(resp.ID),
		Object:            "response",
		CreatedAt:         time.Now().Unix(),
		Status:            status,
		Model:             originalModel,
		Output:            output,
		OutputText:        strings.Join(outputText, ""),
		Usage:             openAIUsageToResponsesUsage(AnthropicUsageToOpenAI(resp.Usage)),
		ServiceTier:       resp.ServiceTier,
		IncompleteDetails: incompleteDetails,
	}
}

func anthropicThinkingToResponsesSummary(block AnthropicContentBlock) []interface{} {
	if block.Thinking == "" {
		return nil
	}
	return []interface{}{map[string]interface{}{
		"type": "summary_text",
		"text": block.Thinking,
	}}
}

func responsesResponseID(upstreamID string) string {
	if strings.HasPrefix(upstreamID, "resp_") {
		return upstreamID
	}
	return generateUUID("resp_")
}

func TypedToOpenAIResponsesResponse(typed TypedRequest, content string, toolCalls []core.ToolCall, usage *OpenAIUsage, model string, status string) *OpenAIResponsesResponse {
	if status == "" {
		status = "completed"
	}
	output := make([]OpenAIResponsesOutputItem, 0, 1+len(toolCalls))
	if content != "" {
		output = append(output, OpenAIResponsesOutputItem{
			ID:     generateUUID("msg_"),
			Type:   "message",
			Status: "completed",
			Role:   core.RoleAssistant,
			Content: []OpenAIResponsesContentPart{{
				Type: "output_text",
				Text: content,
			}},
		})
	}
	for _, call := range toolCalls {
		if call.Type == "custom" {
			callID := firstNonEmpty(call.ID, generateUUID("call_"))
			output = append(output, OpenAIResponsesOutputItem{
				ID:        firstNonEmpty(call.ID, generateUUID("ctc_")),
				Type:      "custom_tool_call",
				Status:    "completed",
				CallID:    callID,
				Namespace: call.Namespace,
				Name:      firstNonEmpty(call.OriginalName, call.Name),
				Input:     firstNonEmpty(call.Input, rawJSONStringValue(call.Args)),
			})
			continue
		}
		output = append(output, OpenAIResponsesOutputItem{
			ID:        firstNonEmpty(call.ID, generateUUID("fc_")),
			Type:      "function_call",
			Status:    "completed",
			CallID:    firstNonEmpty(call.ID, generateUUID("call_")),
			Namespace: call.Namespace,
			Name:      firstNonEmpty(call.OriginalName, call.Name),
			Arguments: string(call.Args),
		})
	}
	return &OpenAIResponsesResponse{
		ID:          generateUUID("resp_"),
		Object:      "response",
		CreatedAt:   time.Now().Unix(),
		Status:      status,
		Model:       model,
		Output:      output,
		OutputText:  content,
		Usage:       openAIUsageToResponsesUsage(usage),
		ServiceTier: typed.ServiceTier,
	}
}

func stringFromInterface(value interface{}) string {
	text, _ := value.(string)
	return text
}

func boolPointerFromInterface(value interface{}) *bool {
	if value == nil {
		return nil
	}
	if b, ok := value.(bool); ok {
		return &b
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
