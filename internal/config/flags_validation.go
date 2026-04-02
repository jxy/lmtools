package config

import (
	"flag"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"lmtools/internal/providers"
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

func validateEnvironmentValue(env string) error {
	if IsValidEnvironment(env) {
		return nil
	}

	return fmt.Errorf("invalid argo-env: %q, must be one of: %s, or a custom URL (http://... or https://...)",
		env, strings.Join(Environments, ", "))
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
