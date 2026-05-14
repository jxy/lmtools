package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/http"
	"strings"
)

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

	url, err := providers.ResolveResponsesURL(constants.ProviderOpenAI, cfg.GetProviderURL(), cfg.GetEnv())
	if err != nil {
		return nil, nil, err
	}
	return buildProviderRequest(cfg, url, body, constants.ProviderOpenAI, stream)
}

func openAIResponsesRequestMap(payload PreparedRequestPayload) map[string]interface{} {
	reqMap := map[string]interface{}{
		"model":  payload.Model,
		"input":  marshalOpenAIResponsesInput(payload.Messages),
		"stream": payload.Stream,
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
	if effort := openAIReasoningEffort(payload.Effort); effort != "" {
		reqMap["reasoning"] = map[string]interface{}{"effort": effort}
		if payload.Tools != nil {
			reqMap["include"] = []string{"reasoning.encrypted_content"}
		}
	}
	if text := openAIResponsesTextConfig(payload); text != nil {
		reqMap["text"] = text
	}
	return reqMap
}

func marshalOpenAIResponsesInput(messages []TypedMessage) []interface{} {
	input := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		var content []map[string]interface{}
		flushContent := func() {
			if len(content) == 0 {
				return
			}
			input = append(input, map[string]interface{}{
				"type":    "message",
				"role":    msg.Role,
				"content": content,
			})
			content = nil
		}
		for _, block := range msg.Blocks {
			switch value := block.(type) {
			case TextBlock:
				if value.Text != "" {
					content = append(content, openAIResponsesTextPart(msg.Role, value.Text))
				}
			case *TextBlock:
				if value != nil && value.Text != "" {
					content = append(content, openAIResponsesTextPart(msg.Role, value.Text))
				}
			case ImageBlock:
				content = append(content, openAIResponsesImagePart(value))
			case *ImageBlock:
				if value != nil {
					content = append(content, openAIResponsesImagePart(*value))
				}
			case FileBlock:
				content = append(content, openAIResponsesFilePart(value))
			case *FileBlock:
				if value != nil {
					content = append(content, openAIResponsesFilePart(*value))
				}
			case ToolUseBlock:
				flushContent()
				input = append(input, openAIResponsesToolCallItem(value))
			case *ToolUseBlock:
				if value != nil {
					flushContent()
					input = append(input, openAIResponsesToolCallItem(*value))
				}
			case ToolResultBlock:
				flushContent()
				input = append(input, openAIResponsesToolCallOutputItem(value))
			case *ToolResultBlock:
				if value != nil {
					flushContent()
					input = append(input, openAIResponsesToolCallOutputItem(*value))
				}
			case ReasoningBlock:
				flushContent()
				if item := openAIResponsesReasoningItem(value); item != nil {
					input = append(input, item)
				}
			case *ReasoningBlock:
				if value != nil {
					flushContent()
					if item := openAIResponsesReasoningItem(*value); item != nil {
						input = append(input, item)
					}
				}
			}
		}
		flushContent()
	}
	return input
}

func openAIResponsesReasoningItem(block ReasoningBlock) map[string]interface{} {
	if block.Provider != "openai" {
		return nil
	}
	if len(block.Raw) > 0 {
		if raw := rawMessageToMap(block.Raw); raw != nil {
			return raw
		}
	}
	item := map[string]interface{}{
		"type": "reasoning",
	}
	if block.ID != "" {
		item["id"] = block.ID
	}
	if block.Status != "" {
		item["status"] = block.Status
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
	return map[string]interface{}{
		"type":      "function_call",
		"call_id":   block.ID,
		"name":      block.Name,
		"arguments": string(block.Input),
	}
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

func openAIResponsesFunctionCallOutputItem(block ToolResultBlock) map[string]interface{} {
	item := map[string]interface{}{
		"type":    "function_call_output",
		"call_id": block.ToolUseID,
		"output":  block.Content,
	}
	if block.IsError {
		item["status"] = "incomplete"
	}
	return item
}

func openAIResponsesToolCallOutputItem(block ToolResultBlock) map[string]interface{} {
	if block.Type == "custom" {
		item := map[string]interface{}{
			"type":    "custom_tool_call_output",
			"call_id": block.ToolUseID,
			"output":  block.Content,
		}
		if block.IsError {
			item["status"] = "incomplete"
		}
		return item
	}
	return openAIResponsesFunctionCallOutputItem(block)
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
				item["format"] = openAIResponsesCustomToolFormat(tool.Custom.Format)
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

func openAIResponsesCustomToolFormat(format interface{}) interface{} {
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
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return raw
	}
	return decoded
}

func rawMessageToMap(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}

func parseOpenAIResponses(data []byte, isEmbed bool) (Response, error) {
	if isEmbed {
		return parseOpenAIResponse(data, true)
	}
	text, toolCalls, blocks, err := parseOpenAIResponsesDetailed(data)
	return Response{Text: text, ToolCalls: toolCalls, Blocks: blocks}, err
}

func parseOpenAIResponsesWithTools(data []byte) (string, []ToolCall, error) {
	text, toolCalls, _, err := parseOpenAIResponsesDetailed(data)
	return text, toolCalls, err
}

func parseOpenAIResponsesDetailed(data []byte) (string, []ToolCall, []Block, error) {
	var resp struct {
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
		var item struct {
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
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return "", nil, nil, err
		}
		switch item.Type {
		case "reasoning":
			blocks = append(blocks, ReasoningBlock{
				Provider:         "openai",
				Type:             "reasoning",
				ID:               item.ID,
				Status:           item.Status,
				Summary:          append(json.RawMessage(nil), item.Summary...),
				EncryptedContent: item.EncryptedContent,
				Raw:              append(json.RawMessage(nil), rawItem...),
			})
		case "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text", "text":
					if content.Text != "" {
						textParts = append(textParts, content.Text)
						blocks = append(blocks, TextBlock{Text: content.Text})
					}
				case "refusal", "output_refusal":
					text := content.Refusal
					if text == "" {
						text = content.Text
					}
					if text != "" {
						textParts = append(textParts, text)
						blocks = append(blocks, TextBlock{Text: text})
					}
				}
			}
		case "function_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   id,
				Name: item.Name,
				Args: normalizeOpenAIResponsesArguments(item.Arguments),
			})
			blocks = append(blocks, ToolUseBlock{
				ID:    id,
				Name:  item.Name,
				Input: normalizeOpenAIResponsesArguments(item.Arguments),
			})
		case "custom_tool_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:    id,
				Type:  "custom",
				Name:  item.Name,
				Args:  jsonStringRawMessage(item.Input),
				Input: item.Input,
			})
			blocks = append(blocks, ToolUseBlock{
				ID:          id,
				Type:        "custom",
				Name:        item.Name,
				Input:       jsonStringRawMessage(item.Input),
				InputString: item.Input,
			})
		}
	}
	if len(textParts) == 0 && resp.OutputText != "" {
		textParts = append(textParts, resp.OutputText)
		blocks = append(blocks, TextBlock{Text: resp.OutputText})
	}
	return strings.Join(textParts, ""), toolCalls, blocks, nil
}

func normalizeOpenAIResponsesArguments(arguments string) json.RawMessage {
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
	return normalizeOpenAIToolArguments(encoded)
}

type OpenAIResponsesStreamState struct {
	partialToolCalls map[int]*ToolCall
	argumentBuffers  map[int]*bytes.Buffer
	outputItems      map[int]json.RawMessage
	outputOrder      []int
}

func NewOpenAIResponsesStreamState() *OpenAIResponsesStreamState {
	return &OpenAIResponsesStreamState{
		partialToolCalls: make(map[int]*ToolCall),
		argumentBuffers:  make(map[int]*bytes.Buffer),
		outputItems:      make(map[int]json.RawMessage),
	}
}

func (s *OpenAIResponsesStreamState) ParseLine(line string) (string, []ToolCall, bool, error) {
	if !strings.HasPrefix(line, "data: ") {
		return "", nil, false, nil
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		calls, done, err := s.finalizeToolCalls()
		return "", calls, done, err
	}

	var event struct {
		Type        string          `json:"type"`
		Delta       string          `json:"delta"`
		OutputIndex int             `json:"output_index"`
		Item        json.RawMessage `json:"item"`
		Response    struct {
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		} `json:"response"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", nil, false, err
	}

	if len(event.Error) > 0 && !bytes.Equal(event.Error, []byte("null")) {
		return "", nil, false, newFatalStreamError(fmt.Errorf("upstream stream error: %s", formatOpenAIStreamError(event.Error)))
	}

	switch event.Type {
	case "error":
		return "", nil, false, newFatalStreamError(fmt.Errorf("OpenAI responses stream error"))
	case "response.output_text.delta":
		return event.Delta, nil, false, nil
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if _, ok := s.argumentBuffers[event.OutputIndex]; !ok {
			s.argumentBuffers[event.OutputIndex] = &bytes.Buffer{}
		}
		s.argumentBuffers[event.OutputIndex].WriteString(event.Delta)
	case "response.output_item.added", "response.output_item.done":
		if err := s.captureOutputItem(event.OutputIndex, event.Item); err != nil {
			return "", nil, false, err
		}
		if event.Type == "response.output_item.done" {
			if tc, ok := s.partialToolCalls[event.OutputIndex]; ok {
				delete(s.partialToolCalls, event.OutputIndex)
				delete(s.argumentBuffers, event.OutputIndex)
				return "", []ToolCall{*tc}, false, nil
			}
		}
	case "response.completed", "response.incomplete":
		calls, done, err := s.finalizeToolCalls()
		return "", calls, done, err
	case "response.failed":
		if event.Response.Error != nil {
			raw, err := json.Marshal(event.Response.Error)
			if err == nil {
				return "", nil, false, newFatalStreamError(fmt.Errorf("API error: %s", formatOpenAIStreamError(raw)))
			}
			return "", nil, false, newFatalStreamError(fmt.Errorf("API error: %s (type: %s, code: %s)", event.Response.Error.Message, event.Response.Error.Type, event.Response.Error.Code))
		}
		return "", nil, false, newFatalStreamError(fmt.Errorf("OpenAI responses stream failed"))
	}

	return "", nil, false, nil
}

func (s *OpenAIResponsesStreamState) captureOutputItem(index int, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var item struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Input     string `json:"input"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	switch item.Type {
	case "reasoning", "message", "function_call", "custom_tool_call":
		if _, exists := s.outputItems[index]; !exists {
			s.outputOrder = append(s.outputOrder, index)
		}
		s.outputItems[index] = append(json.RawMessage(nil), raw...)
	}
	if item.Type != "function_call" && item.Type != "custom_tool_call" {
		return nil
	}
	if _, ok := s.partialToolCalls[index]; !ok {
		s.partialToolCalls[index] = &ToolCall{}
	}
	tc := s.partialToolCalls[index]
	if item.Type == "custom_tool_call" {
		tc.Type = "custom"
	}
	if item.CallID != "" {
		tc.ID = item.CallID
	} else if item.ID != "" {
		tc.ID = item.ID
	}
	if item.Name != "" {
		tc.Name = item.Name
	}
	args := item.Arguments
	if buf := s.argumentBuffers[index]; buf != nil && buf.Len() > 0 {
		args = buf.String()
	}
	if args != "" {
		if item.Type == "custom_tool_call" {
			tc.Input = args
			tc.Args = jsonStringRawMessage(args)
		} else {
			tc.Args = normalizeOpenAIResponsesArguments(args)
		}
	} else if item.Input != "" {
		tc.Input = item.Input
		tc.Args = jsonStringRawMessage(item.Input)
	}
	return nil
}

func (s *OpenAIResponsesStreamState) finalizeToolCalls() ([]ToolCall, bool, error) {
	var calls []ToolCall
	for index, tc := range s.partialToolCalls {
		if len(tc.Args) == 0 {
			if buf := s.argumentBuffers[index]; buf != nil && buf.Len() > 0 {
				if tc.Type == "custom" {
					tc.Input = buf.String()
					tc.Args = jsonStringRawMessage(buf.String())
				} else {
					tc.Args = normalizeOpenAIResponsesArguments(buf.String())
				}
			}
		}
		if len(tc.Args) == 0 {
			if tc.Type == "custom" {
				tc.Args = jsonStringRawMessage(tc.Input)
			} else {
				tc.Args = json.RawMessage("{}")
			}
		}
		calls = append(calls, *tc)
	}
	s.partialToolCalls = map[int]*ToolCall{}
	s.argumentBuffers = map[int]*bytes.Buffer{}
	return calls, true, nil
}

func (s *OpenAIResponsesStreamState) Blocks() []Block {
	if len(s.outputOrder) == 0 {
		return nil
	}
	blocks := make([]Block, 0, len(s.outputOrder))
	for _, index := range s.outputOrder {
		raw := s.outputItems[index]
		if len(raw) == 0 {
			continue
		}
		blocks = append(blocks, openAIResponsesOutputItemBlocks(raw)...)
	}
	return blocks
}

func openAIResponsesOutputItemBlocks(rawItem json.RawMessage) []Block {
	var item struct {
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
			Raw:              append(json.RawMessage(nil), rawItem...),
		}}
	case "message":
		blocks := make([]Block, 0, len(item.Content))
		for _, content := range item.Content {
			text := content.Text
			if text == "" {
				text = content.Refusal
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
			Input: normalizeOpenAIResponsesArguments(item.Arguments),
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
