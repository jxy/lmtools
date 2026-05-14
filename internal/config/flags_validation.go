package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
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

func resolveArgoEnvironment(dev, test bool) string {
	if dev {
		return "dev"
	}
	if test {
		return "test"
	}
	return "prod"
}

func validateArgoEnvironmentFlagCombinations(cfg Config) error {
	if cfg.ArgoDev && cfg.ArgoTest {
		return fmt.Errorf("invalid flag combination: -argo-dev and -argo-test cannot be used together")
	}
	return nil
}

func validateModeFlagCombinations(cfg Config) error {
	if err := validateArgoEnvironmentFlagCombinations(cfg); err != nil {
		return err
	}

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

	if cfg.Embed && (cfg.Effort != "" || cfg.JSONMode || cfg.JSONSchemaPath != "") {
		return fmt.Errorf("invalid flag combination: -effort, -json, and -json-schema are only supported in chat mode")
	}
	if cfg.OpenAIResponses && cfg.Embed {
		return fmt.Errorf("invalid flag combination: -openai-responses is only supported in chat mode")
	}

	if cfg.Effort != "" && !isValidEffortFlag(cfg.Effort) {
		return fmt.Errorf("-effort must be one of: none, minimal, low, medium, high, xhigh, max")
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

func normalizeAndValidateProvider(cfg *Config) error {
	cfg.Provider = constants.NormalizeProvider(cfg.Provider)
	if cfg.Provider != "" && !constants.IsValidProvider(cfg.Provider) {
		return fmt.Errorf("invalid provider: %q, must be one of: %s",
			cfg.Provider, constants.JoinedProviders())
	}
	if cfg.Provider == "" {
		cfg.Provider = constants.ProviderArgo
	}
	if cfg.OpenAIResponses && cfg.Provider != constants.ProviderOpenAI {
		return fmt.Errorf("invalid flag combination: -openai-responses requires -provider openai")
	}
	return nil
}

func validateProviderCredentials(cfg Config) error {
	if cfg.ShowSessions || cfg.Delete != "" || cfg.Show != "" || cfg.ListModels {
		return nil
	}

	ok, _ := providers.EvaluateCredentialState(cfg.Provider, providers.CredentialState{
		ProviderURL: cfg.ProviderURL != "",
		APIKey:      cfg.APIKeyFile != "",
		ArgoUser:    cfg.ArgoUser != "",
	}, providers.ValidationSurfaceCLI)
	if !ok {
		return fmt.Errorf("%s", providers.ValidationError(cfg.Provider, providers.ValidationSurfaceCLI))
	}
	return nil
}

func validateToolFlags(cfg Config) error {
	if cfg.EnableTool {
		if err := core.ValidateToolSupport(cfg.Provider, cfg.Model); err != nil {
			return err
		}
	}

	if cfg.EnableTool && cfg.ToolNonInteractive && !cfg.ToolAutoApprove && cfg.ToolWhitelist == "" {
		return fmt.Errorf("tool-non-interactive requires either -tool-auto-approve or a -tool-whitelist file")
	}

	return nil
}
