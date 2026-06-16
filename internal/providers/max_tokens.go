package providers

import "strings"

// Default Claude output-token limits applied when a caller does not provide an
// explicit max_tokens. The Anthropic Messages wire format requires a positive
// max_tokens, and Argo Claude routes reject max_tokens:0, so an absent limit
// must be defaulted rather than omitted.
const (
	DefaultClaudeOpusMaxTokens    = 128000
	DefaultClaudeDefaultMaxTokens = 64000
)

// DefaultClaudeMaxTokens returns the compatibility default output-token limit
// for a Claude model: 128000 for Opus models, 64000 for other Claude models.
func DefaultClaudeMaxTokens(model string) int {
	if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "opus") {
		return DefaultClaudeOpusMaxTokens
	}
	return DefaultClaudeDefaultMaxTokens
}
