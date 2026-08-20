package core

import (
	"reflect"
	"testing"
)

func TestToolResultsMessageBlocksOrdersResultsBeforeNote(t *testing.T) {
	results := []ToolResult{
		{ID: "call-1", Output: "one"},
		{ID: "call-2", Error: "exit status 1"},
	}
	names := map[string]string{"call-1": "universal_command"}

	blocks := ToolResultsMessageBlocks(results, "note: output truncated\n", names)
	want := []Block{
		ToolResultBlock{ToolUseID: "call-1", Name: "universal_command", Content: "one"},
		ToolResultBlock{ToolUseID: "call-2", Content: "exit status 1", IsError: true},
		TextBlock{Text: "note: output truncated\n"},
	}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("ToolResultsMessageBlocks() = %#v, want %#v", blocks, want)
	}
}

func TestToolResultsMessageBlocksWithoutNoteOrNames(t *testing.T) {
	blocks := ToolResultsMessageBlocks([]ToolResult{{ID: "call-1", Output: "one"}}, "", nil)
	want := []Block{ToolResultBlock{ToolUseID: "call-1", Content: "one"}}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("ToolResultsMessageBlocks() = %#v, want %#v", blocks, want)
	}
}
