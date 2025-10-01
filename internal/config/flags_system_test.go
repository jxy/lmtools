package config

import (
	"lmtools/internal/prompts"
	"testing"
)

func TestSystemPromptWithToolFlag(t *testing.T) {
	tests := []struct {
		name                  string
		args                  []string
		expectedSystem        string
		expectedEnableTool    bool
		expectedExplicitlySet bool
	}{
		{
			name:                  "tool flag only - should use ToolSystemPrompt",
			args:                  []string{"-tool", "-argo-user", "testuser"},
			expectedSystem:        prompts.ToolSystemPrompt,
			expectedEnableTool:    true,
			expectedExplicitlySet: false,
		},
		{
			name:                  "tool flag with custom system prompt",
			args:                  []string{"-tool", "-s", "Custom system prompt", "-argo-user", "testuser"},
			expectedSystem:        "Custom system prompt",
			expectedEnableTool:    true,
			expectedExplicitlySet: true,
		},
		{
			name:                  "no tool flag - should use default",
			args:                  []string{"-argo-user", "testuser"},
			expectedSystem:        prompts.DefaultSystemPrompt,
			expectedEnableTool:    false,
			expectedExplicitlySet: false,
		},
		{
			name:                  "explicit system prompt without tool",
			args:                  []string{"-s", "My custom prompt", "-argo-user", "testuser"},
			expectedSystem:        "My custom prompt",
			expectedEnableTool:    false,
			expectedExplicitlySet: true,
		},
		{
			name:                  "tool with explicit default system prompt",
			args:                  []string{"-tool", "-s", prompts.DefaultSystemPrompt, "-argo-user", "testuser"},
			expectedSystem:        prompts.DefaultSystemPrompt,
			expectedEnableTool:    true,
			expectedExplicitlySet: true, // Should be true even if value matches default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags(tt.args)
			if err != nil {
				t.Fatalf("ParseFlags failed: %v", err)
			}

			// Check EnableTool
			if cfg.EnableTool != tt.expectedEnableTool {
				t.Errorf("EnableTool: got %v, want %v", cfg.EnableTool, tt.expectedEnableTool)
			}

			// Check SystemExplicitlySet
			if cfg.SystemExplicitlySet != tt.expectedExplicitlySet {
				t.Errorf("SystemExplicitlySet: got %v, want %v", cfg.SystemExplicitlySet, tt.expectedExplicitlySet)
			}

			// Check GetEffectiveSystem()
			actualSystem := cfg.GetEffectiveSystem()
			if actualSystem != tt.expectedSystem {
				t.Errorf("GetEffectiveSystem(): got %q, want %q", actualSystem, tt.expectedSystem)
			}

			// Also verify the raw System field for transparency
			t.Logf("Raw System field: %q", cfg.System)
		})
	}
}

func TestSystemPromptDetectionEdgeCases(t *testing.T) {
	// Test various flag formats
	testCases := []struct {
		name             string
		args             []string
		shouldBeExplicit bool
	}{
		{
			name:             "flag with space separator",
			args:             []string{"-s", "Test prompt", "-argo-user", "user"},
			shouldBeExplicit: true,
		},
		{
			name:             "no system flag at all",
			args:             []string{"-argo-user", "user"},
			shouldBeExplicit: false,
		},
		{
			name:             "system flag with empty value",
			args:             []string{"-s", "", "-argo-user", "user"},
			shouldBeExplicit: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseFlags(tc.args)
			if err != nil {
				t.Fatalf("ParseFlags failed: %v", err)
			}

			if cfg.SystemExplicitlySet != tc.shouldBeExplicit {
				t.Errorf("SystemExplicitlySet: got %v, want %v for args %v",
					cfg.SystemExplicitlySet, tc.shouldBeExplicit, tc.args)
			}
		})
	}
}
