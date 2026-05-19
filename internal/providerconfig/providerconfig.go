package providerconfig

import (
	"flag"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
)

type Options struct {
	Provider    string
	ProviderURL string
	APIKeyFile  string
	ArgoUser    string
	ArgoDev     bool
	ArgoTest    bool
	ArgoLegacy  bool
	ArgoEnv     string
}

type Defaults struct {
	Provider string
	ArgoUser string
}

func RegisterFlags(fs *flag.FlagSet, opts *Options, defaults Defaults) {
	provider := defaults.Provider
	if provider == "" {
		provider = constants.ProviderArgo
	}

	fs.BoolVar(&opts.ArgoDev, "argo-dev", false, "use the Argo dev environment instead of prod")
	fs.BoolVar(&opts.ArgoTest, "argo-test", false, "use the Argo test environment instead of prod")
	fs.BoolVar(&opts.ArgoLegacy, "argo-legacy", false, "use legacy Argo /api/v1/resource chat endpoints")
	fs.StringVar(&opts.ArgoUser, "argo-user", defaults.ArgoUser, "Argo user/API key (or use -api-key-file)")
	fs.StringVar(&opts.Provider, "provider", provider, "provider: argo, openai, google, anthropic")
	fs.StringVar(&opts.ProviderURL, "provider-url", "", "custom provider API endpoint")
	fs.StringVar(&opts.APIKeyFile, "api-key-file", "", "path to API key file (required for openai/google/anthropic; optional alternative for argo)")
}

func ResolveArgoEnvironment(dev, test bool) string {
	if dev {
		return "dev"
	}
	if test {
		return "test"
	}
	return "prod"
}

func (o *Options) Normalize() error {
	if err := ValidateArgoEnvironmentFlags(o.ArgoDev, o.ArgoTest); err != nil {
		return err
	}
	o.Provider = constants.NormalizeProvider(o.Provider)
	if o.Provider == "" {
		o.Provider = constants.ProviderArgo
	}
	if !constants.IsValidProvider(o.Provider) {
		return fmt.Errorf("invalid provider: %q, must be one of: %s", o.Provider, constants.JoinedProviders())
	}
	if o.ArgoEnv == "" {
		o.ArgoEnv = ResolveArgoEnvironment(o.ArgoDev, o.ArgoTest)
	}
	return nil
}

func ValidateArgoEnvironmentFlags(dev, test bool) error {
	if dev && test {
		return fmt.Errorf("invalid flag combination: -argo-dev and -argo-test cannot be used together")
	}
	return nil
}

func (o Options) CredentialState(keys auth.ProviderKeySet) providers.CredentialState {
	return providers.CredentialState{
		ProviderURL: o.ProviderURL != "",
		APIKey:      o.APIKeyFile != "" || keys.KeyForProvider(o.Provider) != "",
		ArgoUser:    o.ArgoUser != "",
	}
}

func (o Options) ValidateCredentials(surface providers.ValidationSurface, keys auth.ProviderKeySet, allowSkip bool) error {
	if allowSkip {
		return nil
	}
	normalized := o
	if err := normalized.Normalize(); err != nil {
		return err
	}
	state := normalized.CredentialState(keys)
	if ok, _ := providers.EvaluateCredentialState(normalized.Provider, state, surface); !ok {
		return fmt.Errorf("%s", providers.ValidationError(normalized.Provider, surface))
	}
	return nil
}
