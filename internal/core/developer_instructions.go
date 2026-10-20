package core

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
)

// ValidateDeveloperInstructions checks whether this invocation can send a new
// instruction with its actual developer role.
func ValidateDeveloperInstructions(cfg RequestOptions) error {
	if cfg.Developer == "" {
		return nil
	}
	return ValidateDeveloperRole(cfg)
}

// ValidateDeveloperRole checks the wire for a developer message already stored
// in session history, even when this invocation supplies no new instruction.
func ValidateDeveloperRole(cfg RequestOptions) error {
	if cfg.Embed {
		return fmt.Errorf("-developer is only supported in chat mode")
	}
	return validateDeveloperWire(cfg, EffectiveChatModel(cfg))
}

// ValidateDeveloperTurn requires a new user turn for a new instruction. The
// instruction is staged with that user message and consumed after commit.
func ValidateDeveloperTurn(cfg RequestOptions, input string, isRegeneration bool) error {
	if err := ValidateDeveloperInstructions(cfg); err != nil || cfg.Developer == "" {
		return err
	}
	if isRegeneration {
		return fmt.Errorf("-developer requires a new user turn; branch from a user message or resume with prompt text instead of regenerating an assistant answer")
	}
	if input == "" && len(cfg.Images) == 0 {
		return fmt.Errorf("-developer requires prompt text or an -image for its first turn; supply input when resuming pending tools")
	}
	return nil
}

func validateDeveloperWire(cfg RequestOptions, model string) error {
	provider := getProviderWithDefault(cfg, constants.ProviderArgo)
	if provider == constants.ProviderOpenAI {
		return nil
	}
	if provider == constants.ProviderArgo && providers.UsesResponsesAPIWire(provider, model, cfg.OpenAIResponses, cfg.ArgoLegacy) {
		return nil
	}
	return fmt.Errorf("provider %q model %q cannot preserve the developer role; use -provider openai or Argo -openai-responses with a gpt* model, or supply a system instruction with -s", provider, model)
}

func validateDeveloperMessages(cfg RequestOptions, model string, messages []TypedMessage) error {
	for _, message := range messages {
		if message.Role == string(RoleDeveloper) {
			return validateDeveloperWire(cfg, model)
		}
	}
	return nil
}
