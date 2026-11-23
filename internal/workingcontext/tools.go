package workingcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"slices"
	"strings"
	"time"
)

// Tools mounts branch-bound history alongside the existing MCP host. It uses
// the same per-tool approvals, journal, and selection as external servers.
type Tools struct {
	Remote  core.MCPTools
	History History
	local   []mcp.QualifiedTool
	timeout time.Duration
}

func NewTools(remote core.MCPTools, local *mcp.ServerConfig, settings contextcfg.Settings, timeout time.Duration, warn mcp.Logf) *Tools {
	t := &Tools{Remote: remote, timeout: timeout}
	t.History.phaseHints = settings.PhaseHints()
	if local != nil {
		for _, tool := range mcp.SelectedTools(*local, t.History.Definitions(), warn) {
			t.local = append(t.local, mcp.QualifiedTool{Server: "history", Name: mcp.QualifiedName("history", tool.Name), Tool: tool})
		}
	}
	return t
}

func (t *Tools) Tools() []mcp.QualifiedTool {
	var list []mcp.QualifiedTool
	if t.Remote != nil {
		list = t.Remote.Tools()
	}
	return append(list, t.local...)
}

func (t *Tools) Instructions() []mcp.ServerInstructions {
	var list []mcp.ServerInstructions
	if t.Remote != nil {
		list = t.Remote.Instructions()
	}
	if len(t.local) > 0 {
		guidance := strings.TrimSpace(historyInstructions)
		if slices.ContainsFunc(t.local, func(tool mcp.QualifiedTool) bool { return tool.Tool.Name == "request_compaction" }) {
			guidance += "\n" + strings.TrimSpace(phaseHintInstructions)
		}
		list = append(list, mcp.ServerInstructions{Server: "history", Instructions: guidance})
	}
	return list
}

func (t *Tools) Call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	for _, q := range t.local {
		if q.Name == name {
			if t.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, t.timeout)
				defer cancel()
			}
			value, err := t.History.CallTool(ctx, q.Tool.Name, args)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return mcp.TextResult(value, err), nil
		}
	}
	if IsHistoryTool(name) {
		return nil, fmt.Errorf("history tool %q is not enabled", name)
	}
	if t.Remote == nil {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	return t.Remote.Call(ctx, name, args)
}
