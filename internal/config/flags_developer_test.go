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
		{"openai_chat", []string{"-provider", "openai", "-chat-completions"}, ""},
		{"openai_responses", []string{"-provider", "openai"}, ""},
		{"argo_chat", []string{"-provider", "argo", "-model", "gpt5", "-chat-completions"}, "cannot preserve the developer role"},
		{"argo_responses", []string{"-provider", "argo", "-model", "gpt5"}, ""},
		{"argo_chat_default_model", []string{"-provider", "argo", "-chat-completions"}, "cannot preserve the developer role"},
		{"argo_responses_default_model", []string{"-provider", "argo"}, ""},
		{"anthropic", []string{"-provider", "anthropic"}, "cannot preserve the developer role"},
		{"google", []string{"-provider", "google"}, "cannot preserve the developer role"},
		{"argo_claude", []string{"-provider", "argo", "-model", "claude-opus-4-7"}, "cannot preserve the developer role"},
		{"argo_legacy", []string{"-provider", "argo", "-model", "gpt5", "-argo-legacy"}, "cannot preserve the developer role"},
		{"embed", []string{"-provider", "openai", "-embed"}, "only supported in chat mode"},
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
