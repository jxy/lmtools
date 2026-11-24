package config

import (
	"lmtools/internal/prompts"
	"testing"
)

// The system prompt is the custom one -system sets, an empty one included,
// or else the built-in prompt for the run's tools. The command rules follow
// any prompt of a run with tools.
func TestSystemPromptWithToolFlag(t *testing.T) {
	tests := []struct {
		name               string
		args               []string
		expectedSystem     string
		expectedEnableTool bool
		expectedCustom     bool
	}{
		{
			name:               "tool flag only - should use ToolSystemPrompt",
			args:               []string{"-tool", "-argo-user", "testuser"},
			expectedSystem:     prompts.ToolSystemPrompt,
			expectedEnableTool: true,
		},
		{
			name:               "tool flag with custom system prompt",
			args:               []string{"-tool", "-system", "Custom system prompt", "-argo-user", "testuser"},
			expectedSystem:     "Custom system prompt\n\n" + prompts.ToolPromptRules,
			expectedEnableTool: true,
			expectedCustom:     true,
		},
		{
			name:           "no tool flag - should use default",
			args:           []string{"-argo-user", "testuser"},
			expectedSystem: prompts.DefaultSystemPrompt,
		},
		{
			name:           "explicit system prompt without tool",
			args:           []string{"-system", "My custom prompt", "-argo-user", "testuser"},
			expectedSystem: "My custom prompt",
			expectedCustom: true,
		},
		{
			name:               "tool with explicit default system prompt",
			args:               []string{"-tool", "-system", prompts.DefaultSystemPrompt, "-argo-user", "testuser"},
			expectedSystem:     prompts.DefaultSystemPrompt + "\n\n" + prompts.ToolPromptRules,
			expectedEnableTool: true,
			expectedCustom:     true, // custom even when it matches the default
		},
		{
			name:           "empty system prompt",
			args:           []string{"-system", "", "-argo-user", "testuser"},
			expectedSystem: "",
			expectedCustom: true,
		},
		{
			name:               "empty system prompt with tools",
			args:               []string{"-tool", "-system", "", "-argo-user", "testuser"},
			expectedSystem:     prompts.ToolPromptRules,
			expectedEnableTool: true,
			expectedCustom:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(tt.args)
			if err != nil {
				t.Fatalf("ParseFlags failed: %v", err)
			}
			if cfg.EnableTool != tt.expectedEnableTool {
				t.Errorf("EnableTool: got %v, want %v", cfg.EnableTool, tt.expectedEnableTool)
			}
			if custom := cfg.Change.System != nil; custom != tt.expectedCustom {
				t.Errorf("custom prompt: got %v, want %v", custom, tt.expectedCustom)
			}
			if actual := cfg.RequestOptions().GetEffectiveSystem(); actual != tt.expectedSystem {
				t.Errorf("RequestOptions().GetEffectiveSystem(): got %q, want %q", actual, tt.expectedSystem)
			}
		})
	}
}
