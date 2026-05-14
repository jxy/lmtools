package session

import (
	"lmtools/internal/core"
	"lmtools/internal/prompts"
)

type ResumeForkDecision struct {
	ShouldFork bool
	NewSystem  string
}

func DecideResumeFork(sessionSystem *string, cfg core.RequestOptions) ResumeForkDecision {
	if cfg.IsSystemExplicitlySet() {
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

	if sessionSystem != nil {
		if *sessionSystem == prompts.DefaultSystemPrompt && cfg.IsToolEnabled() {
			return ResumeForkDecision{
				ShouldFork: true,
				NewSystem:  prompts.ToolSystemPrompt,
			}
		}
		return ResumeForkDecision{}
	}

	if cfg.IsToolEnabled() {
		return ResumeForkDecision{
			ShouldFork: true,
			NewSystem:  prompts.ToolSystemPrompt,
		}
	}

	return ResumeForkDecision{}
}
