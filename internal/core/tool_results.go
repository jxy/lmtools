package core

import "fmt"

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
