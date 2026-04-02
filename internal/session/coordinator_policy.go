package session

import (
	"lmtools/internal/core"
	"lmtools/internal/prompts"
)

type CoordinatorConfig interface {
	core.SystemConfig
	core.ToolConfig
	core.SessionResumeConfig
}

type ResumeForkDecision struct {
	ShouldFork bool
	NewSystem  string
}

func DecideResumeFork(sessionSystem *string, cfg CoordinatorConfig) ResumeForkDecision {
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
