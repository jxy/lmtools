package config

import (
	"strings"
	"testing"
)

func TestDeveloperFlagValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"openai_chat", []string{"-provider", "openai"}, ""},
		{"openai_responses", []string{"-provider", "openai", "-openai-responses"}, ""},
		{"argo_chat", []string{"-provider", "argo", "-model", "gpt5"}, "cannot preserve the developer role"},
		{"argo_responses", []string{"-provider", "argo", "-model", "gpt5", "-openai-responses"}, ""},
		{"argo_default_model", []string{"-provider", "argo"}, "cannot preserve the developer role"},
		{"argo_responses_default_model", []string{"-provider", "argo", "-openai-responses"}, ""},
		{"anthropic", []string{"-provider", "anthropic"}, "cannot preserve the developer role"},
		{"google", []string{"-provider", "google"}, "cannot preserve the developer role"},
		{"argo_claude", []string{"-provider", "argo", "-model", "claude-opus-4-7"}, "cannot preserve the developer role"},
		{"argo_legacy", []string{"-provider", "argo", "-model", "gpt5", "-argo-legacy"}, "cannot preserve the developer role"},
		{"embed", []string{"-provider", "openai", "-e"}, "only supported in chat mode"},
		{"show_sessions", []string{"-provider", "openai", "-show-sessions"}, "only supported when sending a chat request"},
		{"list_models", []string{"-provider", "openai", "-list-models"}, "only supported when sending a chat request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"-provider-url", "http://example.test/v1", "-argo-user", "test", "-developer", "Use concise answers."}, test.args...)
			cfg, err := ParseFlags(args)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error=%v, want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Developer != "Use concise answers." || cfg.RequestOptions().Developer != cfg.Developer {
				t.Fatalf("developer flag lost: %+v", cfg.RequestOptions())
			}
		})
	}
}

func TestEmptyDeveloperFlagAddsNoInstruction(t *testing.T) {
	cfg, err := ParseFlags([]string{"-provider", "anthropic", "-provider-url", "http://example.test/v1", "-developer", ""})
	if err != nil || cfg.Developer != "" {
		t.Fatalf("config=%+v error=%v", cfg, err)
	}
}
