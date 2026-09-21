package session

import (
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"strings"
)

type ResumeForkDecision struct {
	ShouldFork bool
	NewSystem  string
}

func DecideResumeFork(sessionSystem *string, cfg core.RequestOptions) ResumeForkDecision {
	if cfg.SystemExplicitlySet {
		specifiedPrompt := cfg.GetEffectiveSystem()
		if sessionSystem == nil {
			if specifiedPrompt != "" {
				return ResumeForkDecision{
					ShouldFork: true,
					NewSystem:  specifiedPrompt,
				}
			}
			return ResumeForkDecision{}
		}
		if *sessionSystem != specifiedPrompt {
			return ResumeForkDecision{
				ShouldFork: true,
				NewSystem:  specifiedPrompt,
			}
		}
		return ResumeForkDecision{}
	}

	if !cfg.ToolEnabled {
		return ResumeForkDecision{}
	}

	// The built-in tool prompt of this run: the tool prompt, or the shorter
	// one when universal_command is excluded, extended by the MCP addendum
	// when servers are configured. A session on a built-in prompt follows
	// it; a custom prompt the operator once passed stays.
	want := core.ToolSystemPromptFor(cfg.ExcludedTools) + core.MCPSystemPromptAddendum(cfg.MCP)
	if sessionSystem == nil {
		return ResumeForkDecision{ShouldFork: true, NewSystem: want}
	}
	if isBuiltinPrompt(*sessionSystem) && *sessionSystem != want {
		return ResumeForkDecision{ShouldFork: true, NewSystem: want}
	}
	return ResumeForkDecision{}
}

// isBuiltinPrompt reports whether a session's system prompt is one lmc
// wrote on its own: the default prompt, either tool prompt, or either tool
// prompt with an MCP addendum from an earlier run.
func isBuiltinPrompt(system string) bool {
	return system == prompts.DefaultSystemPrompt ||
		strings.HasPrefix(system, prompts.ToolSystemPrompt) ||
		strings.HasPrefix(system, prompts.ToolSystemPromptWithoutCommand)
}
