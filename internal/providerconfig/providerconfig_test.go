package providerconfig

import (
	"flag"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"strings"
	"testing"
)

func TestOptionsNormalize(t *testing.T) {
	opts := Options{Provider: "OPENAI", ArgoTest: true}
	if err := opts.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if opts.Provider != constants.ProviderOpenAI {
		t.Fatalf("Provider = %q, want openai", opts.Provider)
	}
	if opts.ArgoEnv != "test" {
		t.Fatalf("ArgoEnv = %q, want test", opts.ArgoEnv)
	}
}

func TestOptionsNormalizeRejectsArgoDevAndTest(t *testing.T) {
	opts := Options{Provider: constants.ProviderArgo, ArgoDev: true, ArgoTest: true}
	err := opts.Normalize()
	if err == nil {
		t.Fatal("Normalize() succeeded with both ArgoDev and ArgoTest")
	}
	if !strings.Contains(err.Error(), "-argo-dev and -argo-test cannot be used together") {
		t.Fatalf("Normalize() error = %q, want Argo flag conflict", err)
	}
}

func TestRegisterFlagsParsesProviderOptions(t *testing.T) {
	var opts Options
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	RegisterFlags(fs, &opts, Defaults{Provider: constants.ProviderArgo, ArgoUser: "default-user"})

	err := fs.Parse([]string{
		"-provider", "openai",
		"-provider-url", "http://localhost:8080/v1",
		"-api-key-file", "key.txt",
		"-argo-user", "alice",
		"-argo-test",
		"-argo-legacy",
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if opts.Provider != constants.ProviderOpenAI || opts.ProviderURL != "http://localhost:8080/v1" || opts.APIKeyFile != "key.txt" {
		t.Fatalf("provider fields not parsed: %#v", opts)
	}
	if opts.ArgoUser != "alice" || !opts.ArgoTest || !opts.ArgoLegacy {
		t.Fatalf("argo fields not parsed: %#v", opts)
	}
}

func TestValidateCredentials(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		keys    auth.ProviderKeySet
		surface providers.ValidationSurface
		wantErr bool
	}{
		{
			name:    "cli openai accepts api key file",
			opts:    Options{Provider: constants.ProviderOpenAI, APIKeyFile: "key.txt"},
			surface: providers.ValidationSurfaceCLI,
		},
		{
			name:    "cli openai accepts provider url without key",
			opts:    Options{Provider: constants.ProviderOpenAI, ProviderURL: "http://localhost:8080/v1"},
			surface: providers.ValidationSurfaceCLI,
		},
		{
			name:    "cli openai rejects missing credentials",
			opts:    Options{Provider: constants.ProviderOpenAI},
			surface: providers.ValidationSurfaceCLI,
			wantErr: true,
		},
		{
			name:    "proxy openai accepts loaded key",
			opts:    Options{Provider: constants.ProviderOpenAI},
			keys:    auth.ProviderKeySet{OpenAIAPIKey: "key"},
			surface: providers.ValidationSurfaceProxy,
		},
		{
			name:    "proxy argo accepts argo user",
			opts:    Options{Provider: constants.ProviderArgo, ArgoUser: "user"},
			surface: providers.ValidationSurfaceProxy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.ValidateCredentials(tt.surface, tt.keys)
			if tt.wantErr && err == nil {
				t.Fatal("ValidateCredentials() succeeded, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateCredentials() error = %v", err)
			}
		})
	}
}
