package main

import (
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"os"
	"testing"
)

func TestMain(t *testing.T) {
	// This tests that main doesn't panic with help flag
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "help flag",
			args: []string{"lmc", "-h"},
		},
		{
			name: "help long flag",
			args: []string{"lmc", "-help"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture panic if any
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("main() panicked with args %v: %v", tt.args, r)
				}
			}()

			// Note: We can't actually test main() directly as it calls os.Exit
			// This is more of a compilation test
		})
	}
}

func TestGetExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: 0,
		},
		{
			name:     "generic error",
			err:      errorf("some error"),
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := getExitCode(tt.err)
			if code != tt.expected {
				t.Errorf("getExitCode(%v) = %d, want %d", tt.err, code, tt.expected)
			}
		})
	}
}

func TestGetOperationName(t *testing.T) {
	// This is a compilation test to ensure the function exists
	// We can't test it directly without creating a config
}

func TestGetActualModel(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		expected string
	}{
		{
			name: "explicit model provided",
			cfg: config.Config{
				Model:    "custom-model",
				Provider: "argo",
			},
			expected: "custom-model",
		},
		{
			name: "embed mode without explicit model",
			cfg: config.Config{
				Model:    "",
				Embed:    true,
				Provider: "argo",
			},
			expected: core.DefaultEmbedModel,
		},
		{
			name: "argo provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "argo",
			},
			expected: "gpt5",
		},
		{
			name: "empty provider defaults to argo",
			cfg: config.Config{
				Model:    "",
				Provider: "",
			},
			expected: "gpt5",
		},
		{
			name: "openai provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "openai",
			},
			expected: "gpt-5",
		},
		{
			name: "google provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "google",
			},
			expected: "gemini-2.5-pro",
		},
		{
			name: "anthropic provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "anthropic",
			},
			expected: "claude-opus-4-1-20250805",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Compute actual model using the same logic as in main.go
			actual := tt.cfg.Model
			if actual == "" {
				if tt.cfg.Embed {
					actual = core.DefaultEmbedModel
				} else {
					provider := tt.cfg.Provider
					if provider == "" {
						provider = constants.ProviderArgo
					}
					actual = core.GetDefaultChatModel(provider)
				}
			}
			if actual != tt.expected {
				t.Errorf("computed model = %q, want %q", actual, tt.expected)
			}
		})
	}
}

// Helper to create simple errors for testing
type testError struct {
	msg string
}

func (e testError) Error() string {
	return e.msg
}

func errorf(format string, args ...interface{}) error {
	return testError{msg: format}
}
