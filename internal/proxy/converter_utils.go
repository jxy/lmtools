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
