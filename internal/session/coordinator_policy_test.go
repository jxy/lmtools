package session

import (
	"lmtools/internal/prompts"
	"testing"
)

func TestDecideResumeFork(t *testing.T) {
	tests := []struct {
		name          string
		sessionSystem *string
		configure     func(*testCoordinatorConfig)
		wantFork      bool
		wantSystem    string
	}{
		{
			name:          "explicit system forks when session prompt differs",
			sessionSystem: stringPtr(prompts.DefaultSystemPrompt),
			configure: func(cfg *testCoordinatorConfig) {
				cfg.System = "custom system"
				cfg.SystemExplicitlySet = true
			},
			wantFork:   true,
			wantSystem: "custom system",
		},
		{
			name:          "explicit empty system removes existing prompt",
			sessionSystem: stringPtr(prompts.DefaultSystemPrompt),
			configure: func(cfg *testCoordinatorConfig) {
				cfg.System = ""
				cfg.SystemExplicitlySet = true
				cfg.EffectiveSystemOverride = stringPtr("")
			},
			wantFork: true,
		},
		{
			name:          "tool enable upgrades default prompt",
			sessionSystem: stringPtr(prompts.DefaultSystemPrompt),
			configure: func(cfg *testCoordinatorConfig) {
				cfg.IsToolEnabledFlag = true
			},
			wantFork:   true,
			wantSystem: prompts.ToolSystemPrompt,
		},
		{
			name:          "custom prompt is preserved when enabling tools",
			sessionSystem: stringPtr("custom system"),
			configure: func(cfg *testCoordinatorConfig) {
				cfg.IsToolEnabledFlag = true
			},
			wantFork: false,
		},
		{
			name:          "no prompt forks to tool prompt when tools enabled",
			sessionSystem: nil,
			configure: func(cfg *testCoordinatorConfig) {
				cfg.IsToolEnabledFlag = true
			},
			wantFork:   true,
			wantSystem: prompts.ToolSystemPrompt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newTestCoordinatorConfig()
			if tt.configure != nil {
				tt.configure(cfg)
			}

			decision := DecideResumeFork(tt.sessionSystem, cfg)
			if decision.ShouldFork != tt.wantFork {
				t.Fatalf("ShouldFork = %v, want %v", decision.ShouldFork, tt.wantFork)
			}
			if decision.NewSystem != tt.wantSystem {
				t.Fatalf("NewSystem = %q, want %q", decision.NewSystem, tt.wantSystem)
			}
		})
	}
}
