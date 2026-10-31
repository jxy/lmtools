package providerconfig

import (
	"errors"
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
	// ChatCompletions is -chat-completions: use the provider's
	// chat/completions route where it also has a Responses API, which is
	// used otherwise. It changes nothing for a provider without one.
	ChatCompletions bool
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
	fs.BoolVar(&opts.ChatCompletions, "chat-completions", false, "use the provider's chat/completions route instead of its Responses API (/v1/responses), which -provider openai and argo gpt* models use by default")
}

// RemovedFlags maps the name of a bool flag a binary no longer has to what
// replaced it, so a script that still passes one is told what to use instead
// of "flag provided but not defined". Register them on the set being parsed,
// never on one printed as usage, and check Err after parsing.
type RemovedFlags map[string]string

// removedFlag stands in for a removed bool flag. It takes no value, like the
// flag it replaces, so -name and -name=false both parse and Err answers them.
type removedFlag struct{}

func (removedFlag) String() string   { return "" }
func (removedFlag) Set(string) error { return nil }
func (removedFlag) IsBoolFlag() bool { return true }

func (r RemovedFlags) Register(fs *flag.FlagSet) {
	for name := range r {
		fs.Var(removedFlag{}, name, "")
	}
}

// Err names every removed flag the parsed command line gave, with its
// replacement.
func (r RemovedFlags) Err(fs *flag.FlagSet) error {
	var errs []error
	fs.Visit(func(f *flag.Flag) {
		if replacement, ok := r[f.Name]; ok {
			errs = append(errs, fmt.Errorf("-%s was removed: %s", f.Name, replacement))
		}
	})
	return errors.Join(errs...)
}

// ResponsesAPIAvailable reports whether the provider exposes a Responses
// endpoint. Which models use it is providers.UsesResponsesAPIWire's call. An
// empty provider is Argo, as Normalize makes it.
func ResponsesAPIAvailable(provider string, argoLegacy bool) bool {
	switch constants.NormalizeProvider(provider) {
	case constants.ProviderOpenAI:
		return true
	case constants.ProviderArgo, "":
		return !argoLegacy
	default:
		return false
	}
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

// Normalize resolves the provider and Argo environment and validates the
// provider-level flag combinations. lmc and apiproxy both go through it (apiproxy
// via proxy.Config.Validate) so they reject the same combinations.
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

func (o Options) ValidateCredentials(surface providers.ValidationSurface, keys auth.ProviderKeySet) error {
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
