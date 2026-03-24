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

func toolDefinitionsToOpenAITools(tools []core.ToolDefinition) []OpenAITool {
	if len(tools) == 0 {
		return nil
	}

	openAITools := make([]OpenAITool, 0, len(tools))
	for _, tool := range tools {
		parameters := filterSchemaMetadata(tool.InputSchema)
		paramsMap, _ := parameters.(map[string]interface{})
		openAITools = append(openAITools, OpenAITool{
			Type: "function",
			Function: OpenAIFunc{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  paramsMap,
			},
		})
	}
	return openAITools
}

func toolDefinitionsToAnthropicTools(tools []core.ToolDefinition) []AnthropicTool {
	if len(tools) == 0 {
		return nil
	}

	anthropicTools := make([]AnthropicTool, 0, len(tools))
	for _, tool := range tools {
		anthropicTools = append(anthropicTools, AnthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}
	return anthropicTools
}

func toolChoiceToOpenAI(choice *core.ToolChoice) interface{} {
	if choice == nil {
		return nil
	}

	switch choice.Type {
	case "any", "auto", "none":
		return choice.Type
	case "tool":
		if choice.Name != "" {
			return map[string]interface{}{
				"type": "function",
				"function": map[string]string{
					"name": choice.Name,
				},
			}
		}
	}

	return nil
}

func toolChoiceToAnthropic(choice *core.ToolChoice) *AnthropicToolChoice {
	if choice == nil {
		return nil
	}

	return &AnthropicToolChoice{
		Type: choice.Type,
		Name: choice.Name,
	}
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
// This is the centralized converter to avoid duplication across the codebase
func AnthropicBlocksToCore(blocks []AnthropicContentBlock) []core.Block {
	coreBlocks := make([]core.Block, 0, len(blocks))

	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				coreBlocks = append(coreBlocks, core.TextBlock{Text: block.Text})
			}

		case "image":
			if block.Source != nil {
				if url, ok := block.Source["url"].(string); ok {
					detail := "auto"
					if d, ok := block.Source["detail"].(string); ok {
						detail = d
					}
					coreBlocks = append(coreBlocks, core.ImageBlock{URL: url, Detail: detail})
				}
			}

		case "input_audio", "audio":
			if block.InputAudio != nil {
				audioBlock := core.AudioBlock{}

				// Check for 'id' field first (standard format)
				if id, ok := block.InputAudio["id"].(string); ok && id != "" {
					audioBlock.ID = id
				}

				// Check for data and format fields
				if data, ok := block.InputAudio["data"].(string); ok && data != "" {
					audioBlock.Data = data
				}
				if format, ok := block.InputAudio["format"].(string); ok && format != "" {
					audioBlock.Format = format
				}

				// Only add if we have at least ID or data
				if audioBlock.ID != "" || audioBlock.Data != "" {
					coreBlocks = append(coreBlocks, audioBlock)
				}
			}

		case "file":
			if block.File != nil {
				if fileID, ok := block.File["file_id"].(string); ok && fileID != "" {
					coreBlocks = append(coreBlocks, core.FileBlock{FileID: fileID})
				} else if name, ok := block.File["name"].(string); ok && name != "" {
					// Use name as FileID if file_id is not present
					coreBlocks = append(coreBlocks, core.FileBlock{FileID: name})
				}
			}

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
			// Extract content from json.RawMessage
			var content string
			if err := json.Unmarshal(block.Content, &content); err != nil {
				content = string(block.Content)
			}
			coreBlocks = append(coreBlocks, core.ToolResultBlock{
				ToolUseID: block.ToolUseID,
				Content:   content,
				IsError:   false,
			})

		case "thinking":
			// Thinking blocks are not supported in OpenAI format, skip them
			// They are logged at DEBUG level by the caller if needed
			continue
		}
	}

	return coreBlocks
}

// CoreBlocksToAnthropic converts core.Block slice to Anthropic content blocks
func CoreBlocksToAnthropic(blocks []core.Block) []AnthropicContentBlock {
	anthBlocks := make([]AnthropicContentBlock, 0, len(blocks))

	for _, block := range blocks {
		switch b := block.(type) {
		case core.TextBlock:
			anthBlocks = append(anthBlocks, AnthropicContentBlock{
				Type: "text",
				Text: b.Text,
			})

		case core.ImageBlock:
			anthBlocks = append(anthBlocks, AnthropicContentBlock{
				Type: "image",
				Source: map[string]interface{}{
					"type":   "url",
					"url":    b.URL,
					"detail": b.Detail,
				},
			})

		case core.AudioBlock:
			// Reconstruct audio block based on available data
			audioMap := make(map[string]interface{})

			// If we have data and format, include them
			if b.Data != "" {
				audioMap["data"] = b.Data
				if b.Format != "" {
					audioMap["format"] = b.Format
				} else {
					audioMap["format"] = "wav" // Default format
				}
			} else if b.ID != "" {
				// Otherwise use ID if available
				audioMap["id"] = b.ID
			}

			if len(audioMap) > 0 {
				anthBlocks = append(anthBlocks, AnthropicContentBlock{
					Type:       "input_audio",
					InputAudio: audioMap,
				})
			}

		case core.FileBlock:
			anthBlocks = append(anthBlocks, AnthropicContentBlock{
				Type: "file",
				File: map[string]interface{}{
					"file_id": b.FileID,
				},
			})

		case core.ToolUseBlock:
			var input map[string]interface{}
			if len(b.Input) > 0 {
				if err := json.Unmarshal(b.Input, &input); err != nil {
					logger.GetLogger().Warnf("Failed to unmarshal tool_use input for tool %s: %v", b.Name, err)
					// Leave input as nil on error
				}
			}
			anthBlocks = append(anthBlocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    b.ID,
				Name:  b.Name,
				Input: input,
			})

		case core.ToolResultBlock:
			content, err := json.Marshal(b.Content)
			if err != nil {
				logger.GetLogger().Warnf("Failed to marshal tool_result content: %v", err)
				content = []byte("\"error marshaling content\"")
			}
			anthBlocks = append(anthBlocks, AnthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: b.ToolUseID,
				Content:   json.RawMessage(content),
			})
		}
	}

	return anthBlocks
}
