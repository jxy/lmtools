package workingcontext

import (
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/session"
)

// Preserve the identity of the latest exchange whose answer was interrupted.
// The note records what reached the branch. The next user can revise the task.
func interruptedWork(raw []core.TypedMessage, refs []session.ContextRef, begin, end int) string {
	start := begin
	for start > 0 {
		start--
		if raw[start].Role == "user" && refs[start].Pin == nil && !resultMessage(raw[start]) {
			break
		}
	}
	var user, lastResult, note string
	waiting := false
	for i := start; i < end; i++ {
		msg := raw[i]
		switch {
		case refs[i].Pin != nil:
			continue
		case msg.Role == "user" && !resultMessage(msg):
			if waiting && user != "" && i >= begin {
				note = user + lastResult
			}
			user, lastResult = "", ""
			for index, block := range msg.Blocks {
				if t, ok := block.(core.TextBlock); ok {
					user = fmt.Sprintf("Previous exchange without a completed answer. Original user request %s.%d (%d bytes): %s", refs[i].Source, index, len(t.Text), t.Text[:mcp.TextBoundary(t.Text, min(512, len(t.Text)))])
					break
				}
			}
			waiting = true
		case resultMessage(msg):
			for index, block := range msg.Blocks {
				if b, ok := block.(core.ToolResultBlock); ok {
					lastResult = fmt.Sprintf("\nLast recorded tool result %s.%d (call %s, error=%t, %d bytes): %s", refs[i].Source, index, b.ToolUseID, b.IsError, len(b.Content), b.Content[:mcp.TextBoundary(b.Content, min(256, len(b.Content)))])
				}
			}
		case msg.Role == "assistant" && !hasCall(msg):
			if refs[i].Incomplete && i >= begin && user != "" {
				note = user + lastResult
			}
			waiting = false
		}
	}
	return note
}
