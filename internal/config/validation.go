package config

import (
	"fmt"
	"lmtools/internal/core"
)

// ValidateChatModel validates that the given model is supported for chat
func ValidateChatModel(model string) error {
	if !core.IsValidChatModel(model) {
		return fmt.Errorf("invalid chat model: %q", model)
	}
	return nil
}

// ValidateEmbedModel validates that the given model is supported for embedding
func ValidateEmbedModel(model string) error {
	if !core.IsValidEmbedModel(model) {
		return fmt.Errorf("invalid embed model: %q", model)
	}
	return nil
}
