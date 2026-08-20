package core

import "fmt"

// ToolResultsMessageBlocks builds the canonical block layout of the user
// message that carries one round of tool results: a tool_result block per
// result, in order, then a single text block when additionalText (truncation
// notes) is non-empty. Every builder of that message must go through here so
// staged requests, in-memory history, and persisted sessions agree.
// toolNamesByID may be nil when names are resolved later from the preceding
// assistant tool_use blocks.
func ToolResultsMessageBlocks(results []ToolResult, additionalText string, toolNamesByID map[string]string) []Block {
	blocks := make([]Block, 0, len(results)+1)
	for _, result := range results {
		blocks = append(blocks, ToolResultBlockFromResult(result, toolNamesByID[result.ID]))
	}
	if additionalText != "" {
		blocks = append(blocks, TextBlock{Text: additionalText})
	}
	return blocks
}

// ToolResultBlockFromResult converts an execution result into a message block.
// Failed commands keep captured output so the model can diagnose and recover.
func ToolResultBlockFromResult(result ToolResult, name string) ToolResultBlock {
	block := ToolResultBlock{
		ToolUseID: result.ID,
		Name:      name,
		Content:   result.Output,
	}
	if result.Error != "" {
		block.IsError = true
		block.Content = formatFailedToolResultContent(result)
	}
	return block
}

func formatFailedToolResultContent(result ToolResult) string {
	if result.Output == "" {
		return result.Error
	}
	return fmt.Sprintf("Output:\n%s\n\nError:\n%s", result.Output, result.Error)
}
