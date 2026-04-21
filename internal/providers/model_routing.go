package providers

import (
	"lmtools/internal/constants"
	"strings"
)

// DetermineArgoModelProvider reports which provider format an Argo model should use.
// Argo's native compatibility layer is binary:
//   - Claude models use Anthropic's messages wire format
//   - Everything else uses OpenAI's chat/completions wire format
func DetermineArgoModelProvider(model string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude") {
		return constants.ProviderAnthropic
	}
	return constants.ProviderOpenAI
}
