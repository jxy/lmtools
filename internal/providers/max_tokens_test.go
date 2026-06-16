package providers

import "testing"

func TestDefaultClaudeMaxTokens(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  int
	}{
		{"opus alias", "claude-opus-4-8", DefaultClaudeOpusMaxTokens},
		{"opus dated", "claude-3-opus-20240229", DefaultClaudeOpusMaxTokens},
		{"opus uppercase", "Claude-OPUS-4-6", DefaultClaudeOpusMaxTokens},
		{"opus with spaces", "  claude-opus-4-8  ", DefaultClaudeOpusMaxTokens},
		{"sonnet", "claude-sonnet-4-6", DefaultClaudeDefaultMaxTokens},
		{"haiku", "claude-haiku-4-5", DefaultClaudeDefaultMaxTokens},
		{"non-claude", "gpt-4", DefaultClaudeDefaultMaxTokens},
		{"empty", "", DefaultClaudeDefaultMaxTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultClaudeMaxTokens(tt.model); got != tt.want {
				t.Errorf("DefaultClaudeMaxTokens(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}
