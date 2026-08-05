package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/prompts"
	"lmtools/internal/providers"
	"os"
	"strings"
)

type explicitFlagState struct {
	noSession bool
}

func applyExplicitFlags(fs *flag.FlagSet, cfg *Config) explicitFlagState {
	var explicit explicitFlagState

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "s":
			cfg.SystemExplicitlySet = true
		case "no-session":
			explicit.noSession = true
		}
	})

	return explicit
}

func applyEmbedModeDefaults(cfg *Config, explicit explicitFlagState) error {
	if cfg.Embed && explicit.noSession && !cfg.NoSession {
		return fmt.Errorf("invalid flag combination: embed mode requires sessions to be disabled. Remove -no-session=false or use chat mode instead")
	}

	if cfg.Embed && !explicit.noSession {
		cfg.NoSession = true
	}

	return nil
}

func validateModeFlagCombinations(cfg Config) error {
	if cfg.Embed && cfg.StreamChat {
		return fmt.Errorf(prompts.ErrEmbedWithStream)
	}
	if cfg.Embed && cfg.EnableTool {
		return fmt.Errorf(prompts.ErrEmbedWithTool)
	}
	if cfg.Embed && (cfg.Resume != "" || cfg.Branch != "") {
		return fmt.Errorf(prompts.ErrEmbedWithSession)
	}
	return nil
}

func validateOutputFlags(cfg *Config) error {
	if cfg.JSONMode && cfg.JSONSchemaPath != "" {
		return fmt.Errorf("invalid flag combination: -json and -json-schema cannot be used together")
	}

	if cfg.Embed && (cfg.Effort != "" || cfg.ReasoningMode != "" || cfg.ReasoningContext != "" || cfg.JSONMode || cfg.JSONSchemaPath != "") {
		return fmt.Errorf("invalid flag combination: -effort, -reasoning-mode, -reasoning-context, -json, and -json-schema are only supported in chat mode")
	}
	if cfg.OpenAIResponses && cfg.Embed {
		return fmt.Errorf("invalid flag combination: -openai-responses is only supported in chat mode")
	}

	if cfg.Effort != "" && !isValidEffortFlag(cfg.Effort) {
		return fmt.Errorf("-effort must be one of: none, minimal, low, medium, high, xhigh, max")
	}

	if cfg.ReasoningMode != "" && !isValidReasoningModeFlag(cfg.ReasoningMode) {
		return fmt.Errorf("-reasoning-mode must be one of: standard, pro")
	}

	if cfg.ReasoningContext != "" && !isValidReasoningContextFlag(cfg.ReasoningContext) {
		return fmt.Errorf("-reasoning-context must be one of: auto, current_turn, all_turns")
	}

	if cfg.JSONSchemaPath == "" {
		return nil
	}

	data, err := os.ReadFile(cfg.JSONSchemaPath)
	if err != nil {
		return fmt.Errorf("read -json-schema file: %w", err)
	}
	if !json.Valid(data) {
		return fmt.Errorf("-json-schema file must contain valid JSON")
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		return fmt.Errorf("-json-schema file must contain a JSON object: %w", err)
	}
	if schema == nil {
		return fmt.Errorf("-json-schema file must contain a JSON object")
	}
	cfg.JSONSchema = append(cfg.JSONSchema[:0], data...)
	return nil
}

func isValidEffortFlag(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func isValidReasoningModeFlag(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "standard", "pro":
		return true
	default:
		return false
	}
}

func isValidReasoningContextFlag(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "current_turn", "all_turns":
		return true
	default:
		return false
	}
}

func validateSessionFlagCombinations(cfg Config) error {
	if cfg.ShowSessions && (cfg.Resume != "" || cfg.Branch != "" || cfg.NoSession || cfg.Delete != "" || cfg.Show != "") {
		return fmt.Errorf("invalid flag combination: -show-sessions cannot be used with other session flags")
	}

	if cfg.Delete != "" && (cfg.Resume != "" || cfg.Branch != "" || cfg.NoSession || cfg.Show != "") {
		return fmt.Errorf("invalid flag combination: -delete cannot be used with other session flags")
	}

	if cfg.Show != "" && (cfg.Resume != "" || cfg.Branch != "" || cfg.Delete != "" || cfg.ShowSessions || cfg.NoSession || cfg.Embed || cfg.StreamChat) {
		return fmt.Errorf("invalid flag combination: -show cannot be used with other session or operation flags")
	}

	if cfg.Resume != "" && cfg.Branch != "" {
		return fmt.Errorf("invalid flag combination: -resume and -branch cannot be used together")
	}

	if cfg.NoSession && (cfg.Resume != "" || cfg.Branch != "") {
		return fmt.Errorf("invalid flag combination: -no-session cannot be used with -resume or -branch")
	}

	return nil
}

func validateProviderCredentials(cfg Config) error {
	if cfg.ShowSessions || cfg.Delete != "" || cfg.Show != "" || cfg.ListModels {
		return nil
	}

	return cfg.ValidateCredentials(providers.ValidationSurfaceCLI, auth.ProviderKeySet{})
}

func validateToolFlags(cfg Config) error {
	if cfg.EnableTool && cfg.ToolNonInteractive && !cfg.ToolAutoApprove && cfg.ToolWhitelist == "" {
		return fmt.Errorf("tool-non-interactive requires either -tool-auto-approve or a -tool-whitelist file")
	}

	return nil
}
