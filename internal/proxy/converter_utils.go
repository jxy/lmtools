package proxy

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
	"time"
)

// extractSystemContent extracts system content from various formats
func extractSystemContent(system json.RawMessage) (string, error) {
	// Try as string
	var str string
	if err := json.Unmarshal(system, &str); err == nil {
		return str, nil
	}

	// Try as array of content blocks
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(system, &blocks); err == nil {
		var texts []string
		for _, block := range blocks {
			if block.Type == "text" {
				texts = append(texts, block.Text)
			}
		}
		return strings.Join(texts, "\n"), nil
	}

	// Try as single content block
	var block AnthropicContentBlock
	if err := json.Unmarshal(system, &block); err == nil {
		if block.Type == "text" {
			return block.Text, nil
		}
		// Non-text block - return empty string (no text content)
		return "", nil
	}

	return "", fmt.Errorf("unsupported system content format")
}

// generateUUID generates a UUID v4 with configurable prefix
// This is the single source of truth for ID generation across the proxy
func generateUUID(prefix string) string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to timestamp-based ID only if crypto/rand fails
		// This should rarely happen in practice
		return fmt.Sprintf("%s%x", prefix, time.Now().UnixNano())
	}

	// Set version (4) and variant bits for UUID v4
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%s%x%x%x%x%x",
		prefix, b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// generateToolUseID generates a unique ID for tool use
func generateToolUseID() string {
	return generateUUID("toolu_")
}

// generateResponseID generates a unique ID for API responses
func generateResponseID() string {
	return generateUUID("msg_")
}

func unmarshalAnthropicBlocks(raw json.RawMessage) ([]AnthropicContentBlock, error) {
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks, nil
	}

	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(raw, &rawBlocks); err != nil {
		return nil, err
	}

	blocks = make([]AnthropicContentBlock, 0, len(rawBlocks))
	for _, item := range rawBlocks {
		var block AnthropicContentBlock
		if err := json.Unmarshal(item, &block); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func parseAnthropicMessageContent(raw json.RawMessage) (*string, []AnthropicContentBlock, error) {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return &str, nil, nil
	}

	if blocks, err := unmarshalAnthropicBlocks(raw); err == nil {
		return nil, blocks, nil
	}

	var block AnthropicContentBlock
	if err := json.Unmarshal(raw, &block); err == nil {
		return nil, []AnthropicContentBlock{block}, nil
	}

	return nil, nil, fmt.Errorf("unsupported anthropic message content format")
}

func openAIContentUnionToInterface(contentUnion core.OpenAIContentUnion) interface{} {
	if len(contentUnion.Contents) > 0 {
		contentArray := make([]interface{}, len(contentUnion.Contents))
		for i, c := range contentUnion.Contents {
			contentArray[i] = c.ToMap()
		}
		return contentArray
	}
	if contentUnion.Text != nil {
		return *contentUnion.Text
	}
	return nil
}

func anthropicContentUnionToRawMessage(contentUnion core.AnthropicContentUnion) (json.RawMessage, error) {
	if contentUnion.Text != nil {
		contentJSON, err := json.Marshal(*contentUnion.Text)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(contentJSON), nil
	}

	if len(contentUnion.Contents) > 0 {
		contentArray := make([]interface{}, len(contentUnion.Contents))
		for i, c := range contentUnion.Contents {
			contentArray[i] = c.ToMap()
		}
		contentJSON, err := json.Marshal(contentArray)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(contentJSON), nil
	}

	return json.RawMessage(`""`), nil
}

func typedOpenAIMessagesToProxy(messages []core.OpenAIMessage) []OpenAIMessage {
	proxyMessages := make([]OpenAIMessage, 0, len(messages))
	for _, msg := range messages {
		content := openAIContentUnionToInterface(msg.Content)
		if msg.Role == string(core.RoleAssistant) && len(msg.ToolCalls) > 0 && len(msg.Content.Contents) == 0 {
			// Preserve existing proxy behavior: assistant tool-call messages with
			// text-only content are emitted with nil content.
			content = nil
		}
		proxyMessages = append(proxyMessages, OpenAIMessage{
			Role:       core.Role(msg.Role),
			Content:    content,
			Name:       msg.Name,
			ToolCalls:  typedOpenAIToolCallsToProxy(msg.ToolCalls),
			ToolCallID: msg.ToolCallID,
		})
	}
	return proxyMessages
}

func typedAnthropicMessagesToProxy(messages []core.AnthropicMessage) ([]AnthropicMessage, error) {
	proxyMessages := make([]AnthropicMessage, 0, len(messages))
	for _, msg := range messages {
		content, err := anthropicContentUnionToRawMessage(msg.Content)
		if err != nil {
			return nil, err
		}
		proxyMessages = append(proxyMessages, AnthropicMessage{
			Role:    core.Role(msg.Role),
			Content: content,
		})
	}
	return proxyMessages, nil
}

func typedGoogleMessagesToProxy(messages []core.GoogleMessage) []GoogleContent {
	proxyMessages := make([]GoogleContent, 0, len(messages))
	for _, msg := range messages {
		parts := make([]GooglePart, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			proxyPart := GooglePart{
				Text:             part.Text,
				ThoughtSignature: part.ThoughtSignature,
			}

			if part.FunctionCall != nil {
				proxyPart.FunctionCall = &GoogleFunctionCall{
					Name: part.FunctionCall.Name,
					Args: rawJSONToMap(part.FunctionCall.Args),
				}
			}

			if part.FunctionResponse != nil {
				response := map[string]interface{}{
					"content": part.FunctionResponse.Response.Content,
				}
				if part.FunctionResponse.Response.Error {
					response["error"] = true
				}
				proxyPart.FunctionResp = &GoogleFunctionResp{
					Name:     part.FunctionResponse.Name,
					Response: response,
				}
			}

			if part.InlineData != nil {
				proxyPart.InlineData = &GoogleInlineData{
					MimeType: part.InlineData.MimeType,
					Data:     part.InlineData.Data,
				}
			}

			parts = append(parts, proxyPart)
		}

		proxyMessages = append(proxyMessages, GoogleContent{
			Role:  msg.Role,
			Parts: parts,
		})
	}

	return proxyMessages
}

func typedMessagesToArgoOpenAI(messages []core.TypedMessage) []ArgoMessage {
	argoMessages := make([]ArgoMessage, 0, len(messages))
	for _, msg := range messages {
		var filteredBlocks []core.Block
		var toolResultMessages []ArgoMessage

		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case core.ToolResultBlock:
				toolResultMessages = append(toolResultMessages, ArgoMessage{
					Role:       "tool",
					ToolCallID: b.ToolUseID,
					Content:    b.Content,
				})
			case core.ThinkingBlock:
				continue
			default:
				filteredBlocks = append(filteredBlocks, block)
			}
		}

		argoMessages = append(argoMessages, toolResultMessages...)

		if len(filteredBlocks) == 0 {
			continue
		}

		contentUnion, toolCalls := core.ConvertBlocksToOpenAIContentTyped(filteredBlocks)
		content := openAIContentUnionToInterface(contentUnion)
		if msg.Role == string(core.RoleAssistant) && len(toolCalls) > 0 && content == nil {
			content = ""
		}

		argoMessages = append(argoMessages, ArgoMessage{
			Role:      msg.Role,
			Content:   content,
			ToolCalls: typedOpenAIToolCallsToProxy(toolCalls),
		})
	}

	return argoMessages
}

func typedMessagesToArgoAnthropic(messages []core.TypedMessage) ([]ArgoMessage, error) {
	anthropicMessages, err := typedAnthropicMessagesToProxy(core.ToAnthropicTyped(messages))
	if err != nil {
		return nil, err
	}

	argoMessages := make([]ArgoMessage, 0, len(anthropicMessages))
	for _, msg := range anthropicMessages {
		text, blocks, err := parseAnthropicMessageContent(msg.Content)
		if err == nil && text != nil {
			argoMessages = append(argoMessages, ArgoMessage{
				Role:    string(msg.Role),
				Content: *text,
			})
			continue
		}
		if err == nil {
			argoMessages = append(argoMessages, ArgoMessage{
				Role:    string(msg.Role),
				Content: blocks,
			})
			continue
		}

		argoMessages = append(argoMessages, ArgoMessage{
			Role:    string(msg.Role),
			Content: string(msg.Content),
		})
	}

	return argoMessages, nil
}

func rawJSONToMap(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}

	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}

	return nil
}

func rawJSONToInterface(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}

	var value interface{}
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}

	return nil
}

func openAIContentToTypedUnion(content interface{}) core.OpenAIContentUnion {
	switch value := content.(type) {
	case string:
		return core.OpenAIContentUnion{
			Text:     &value,
			Contents: nil,
		}
	case []interface{}:
		return core.OpenAIContentUnion{
			Contents: openAIContentItemsToTyped(value),
		}
	case []map[string]interface{}:
		items := make([]interface{}, len(value))
		for i, item := range value {
			items[i] = item
		}
		return core.OpenAIContentUnion{
			Contents: openAIContentItemsToTyped(items),
		}
	default:
		return core.OpenAIContentUnion{}
	}
}

func openAIContentItemsToTyped(items []interface{}) []core.OpenAIContent {
	contents := make([]core.OpenAIContent, 0, len(items))
	for _, item := range items {
		contentMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		contentType, _ := contentMap["type"].(string)
		switch contentType {
		case "text":
			contents = append(contents, core.OpenAIContent{
				Type: "text",
				Text: core.GetString(contentMap, "text"),
			})
		case "image_url":
			imageURLMap, ok := contentMap["image_url"].(map[string]interface{})
			if !ok {
				continue
			}
			contents = append(contents, core.OpenAIContent{
				Type: "image_url",
				ImageURL: &core.OpenAIImageURL{
					URL:    core.GetString(imageURLMap, "url"),
					Detail: defaultString(core.GetString(imageURLMap, "detail"), "auto"),
				},
			})
		case "input_audio":
			audioMap, ok := contentMap["input_audio"].(map[string]interface{})
			if !ok {
				continue
			}
			audioData := &core.AudioData{
				ID:       core.GetString(audioMap, "id"),
				Format:   core.GetString(audioMap, "format"),
				Data:     core.GetString(audioMap, "data"),
				URL:      core.GetString(audioMap, "url"),
				Duration: core.GetInt(audioMap, "duration"),
			}
			if audioData.Format == "" && audioData.Data != "" {
				audioData.Format = "wav"
			}
			contents = append(contents, core.OpenAIContent{
				Type:       "input_audio",
				InputAudio: audioData,
			})
		case "file":
			fileMap, ok := contentMap["file"].(map[string]interface{})
			if !ok {
				continue
			}
			contents = append(contents, core.OpenAIContent{
				Type: "file",
				File: &core.FileData{
					FileID:   core.GetString(fileMap, "file_id"),
					Name:     core.GetString(fileMap, "name"),
					MimeType: core.GetString(fileMap, "mime_type"),
					Data:     core.GetString(fileMap, "data"),
					URL:      core.GetString(fileMap, "url"),
					Size:     core.GetInt64(fileMap, "size"),
				},
			})
		}
	}
	return contents
}

func openAIToolCallsToTyped(toolCalls []ToolCall) []core.OpenAIToolCall {
	if len(toolCalls) == 0 {
		return nil
	}

	typedCalls := make([]core.OpenAIToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		typedCalls[i] = core.OpenAIToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: core.OpenAIFunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return typedCalls
}

func openAIToolChoiceToTyped(toolChoice interface{}) *core.ToolChoice {
	if toolChoice == nil {
		return nil
	}

	switch value := toolChoice.(type) {
	case string:
		if value == "auto" || value == "none" {
			return &core.ToolChoice{Type: value}
		}
	case map[string]interface{}:
		if value["type"] != "function" {
			return nil
		}
		switch function := value["function"].(type) {
		case map[string]interface{}:
			name, ok := function["name"].(string)
			if !ok {
				return nil
			}
			return &core.ToolChoice{
				Type: "tool",
				Name: name,
			}
		case map[string]string:
			name, ok := function["name"]
			if !ok {
				return nil
			}
			return &core.ToolChoice{
				Type: "tool",
				Name: name,
			}
		}
	}

	return nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func typedOpenAIToolCallsToProxy(toolCalls []core.OpenAIToolCall) []ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}

	proxyCalls := make([]ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		proxyCalls[i] = ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return proxyCalls
}

// AnthropicBlocksToCore converts Anthropic content blocks to core.Block slice
func AnthropicBlocksToCore(blocks []AnthropicContentBlock) []core.Block {
	if len(blocks) == 0 {
		return nil
	}

	coreBlocks := make([]core.Block, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			coreBlocks = append(coreBlocks, core.TextBlock{Text: block.Text})
		case "thinking":
			coreBlocks = append(coreBlocks, core.ThinkingBlock{
				Thinking:  block.Thinking,
				Signature: block.Signature,
			})
		case "tool_use":
			inputJSON, err := json.Marshal(block.Input)
			if err != nil {
				logger.GetLogger().Warnf("Failed to marshal tool_use input for tool %s: %v", block.Name, err)
				inputJSON = []byte("{}")
			}
			coreBlocks = append(coreBlocks, core.ToolUseBlock{
				ID:    block.ID,
				Name:  block.Name,
				Input: inputJSON,
			})
		case "tool_result":
			coreBlocks = append(coreBlocks, core.ToolResultBlock{
				ToolUseID: block.ToolUseID,
				Content:   proxyToolResultContent(block.Content),
				IsError:   block.IsError,
			})
		case "image":
			if block.Source != nil {
				coreBlocks = append(coreBlocks, core.ImageBlock{
					URL:    core.GetString(block.Source, "url"),
					Detail: "auto",
				})
			}
		case "audio", "input_audio":
			if block.InputAudio != nil {
				coreBlocks = append(coreBlocks, core.AudioBlock{
					ID:       core.GetString(block.InputAudio, "id"),
					Data:     core.GetString(block.InputAudio, "data"),
					Format:   core.GetString(block.InputAudio, "format"),
					URL:      core.GetString(block.InputAudio, "url"),
					Duration: core.GetInt(block.InputAudio, "duration"),
				})
			}
		case "file":
			coreBlocks = append(coreBlocks, core.FileBlock{
				FileID: core.GetString(block.File, "file_id"),
			})
		}
	}

	return coreBlocks
}

// CoreBlocksToAnthropic converts core.Block slice to Anthropic content blocks
func CoreBlocksToAnthropic(blocks []core.Block) []AnthropicContentBlock {
	if len(blocks) == 0 {
		return nil
	}

	typed := core.ToAnthropicTyped([]core.TypedMessage{
		{
			Role:   string(core.RoleAssistant),
			Blocks: blocks,
		},
	})
	if len(typed) == 0 {
		return nil
	}

	union := typed[0].Content
	if union.Text != nil {
		return []AnthropicContentBlock{
			{
				Type: "text",
				Text: *union.Text,
			},
		}
	}

	return coreAnthropicContentsToProxyBlocks(union.Contents)
}

func coreAnthropicContentsToProxyBlocks(contents []core.AnthropicContent) []AnthropicContentBlock {
	blocks := make([]AnthropicContentBlock, 0, len(contents))
	for _, content := range contents {
		block := AnthropicContentBlock{
			Type:      content.Type,
			Text:      content.Text,
			Thinking:  content.Thinking,
			Signature: content.Signature,
			ID:        content.ID,
			Name:      content.Name,
			ToolUseID: content.ToolUseID,
			IsError:   content.IsError,
		}

		if content.Source != nil {
			block.Source = map[string]interface{}{
				"type": content.Source.Type,
			}
			if content.Source.URL != "" {
				block.Source["url"] = content.Source.URL
			}
			if content.Source.Type == "base64" && content.Source.MediaType != "" {
				block.Source["media_type"] = content.Source.MediaType
			}
			if content.Source.Data != "" {
				block.Source["data"] = content.Source.Data
			}
		}
		if len(content.Input) > 0 {
			if err := json.Unmarshal(content.Input, &block.Input); err != nil {
				logger.GetLogger().Warnf("Failed to unmarshal tool_use input for tool %s: %v", content.Name, err)
			}
		}
		if content.Type == "tool_result" {
			contentJSON, err := json.Marshal(content.Content)
			if err != nil {
				logger.GetLogger().Warnf("Failed to marshal tool_result content: %v", err)
				contentJSON = []byte("\"error marshaling content\"")
			}
			block.Content = json.RawMessage(contentJSON)
		}
		if content.InputAudio != nil {
			block.InputAudio = map[string]interface{}{}
			if content.InputAudio.ID != "" {
				block.InputAudio["id"] = content.InputAudio.ID
			}
			if content.InputAudio.Data != "" {
				block.InputAudio["data"] = content.InputAudio.Data
			}
			if content.InputAudio.Format != "" {
				block.InputAudio["format"] = content.InputAudio.Format
			}
			if content.InputAudio.URL != "" {
				block.InputAudio["url"] = content.InputAudio.URL
			}
			if content.InputAudio.Duration > 0 {
				block.InputAudio["duration"] = content.InputAudio.Duration
			}
		}
		if content.File != nil {
			block.File = map[string]interface{}{}
			if content.File.FileID != "" {
				block.File["file_id"] = content.File.FileID
			}
			if content.File.Name != "" {
				block.File["name"] = content.File.Name
			}
			if content.File.MimeType != "" {
				block.File["mime_type"] = content.File.MimeType
			}
			if content.File.Data != "" {
				block.File["data"] = content.File.Data
			}
			if content.File.URL != "" {
				block.File["url"] = content.File.URL
			}
			if content.File.Size > 0 {
				block.File["size"] = content.File.Size
			}
		}

		blocks = append(blocks, block)
	}
	return blocks
}

func proxyToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var content string
	if err := json.Unmarshal(raw, &content); err == nil {
		return content
	}
	return string(raw)
}
