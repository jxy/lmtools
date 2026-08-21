package config

import (
	"flag"
	"lmtools/internal/providerconfig"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestToolFlagHelpExplainsApprovalPolicy(t *testing.T) {
	var cfg Config
	fs := flag.NewFlagSet("lmc", flag.ContinueOnError)
	registerFlags(fs, &cfg)

	want := map[string][]string{
		"tool":                 {"universal_command", "no shell"},
		"tool-whitelist":       {"JSON command-rule whitelist", "redirected calls", "exact object"},
		"tool-blacklist":       {"JSON command-rule blacklist", "always denied"},
		"tool-auto-approve":    {"without prompting", "blacklist", "non-interactive whitelist"},
		"tool-non-interactive": {"never prompt", "deny non-matching commands"},
	}
	for name, required := range want {
		got := fs.Lookup(name)
		if got == nil {
			t.Fatalf("flag -%s is not registered", name)
		}
		for _, text := range required {
			if !strings.Contains(got.Usage, text) {
				t.Errorf("-%s help = %q, want it to mention %q", name, got.Usage, text)
			}
		}
	}
}

func TestToolFlagDefaults(t *testing.T) {
	var cfg Config
	fs := flag.NewFlagSet("lmc", flag.ContinueOnError)
	registerFlags(fs, &cfg)

	want := map[string]string{
		"tool-timeout":      "1m0s",
		"max-tool-rounds":   "64",
		"max-tool-parallel": "8",
	}
	for name, wantDefault := range want {
		got := fs.Lookup(name)
		if got == nil {
			t.Fatalf("flag -%s is not registered", name)
		}
		if got.DefValue != wantDefault {
			t.Errorf("-%s default = %q, want %q", name, got.DefValue, wantDefault)
		}
	}
}

func TestParseFlagsDefaults(t *testing.T) {
	// Test with explicit user since it's now required if OS user is not available
	cfg, err := ParseFlags([]string{"-argo-user", "testuser"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LogDir field has been removed - logs now always go to ~/.lmc/logs
	if cfg.ArgoUser != "testuser" {
		t.Errorf("ArgoUser = %q; want %q", cfg.ArgoUser, "testuser")
	}
}

func TestParseFlagsArgoRequiresCredential(t *testing.T) {
	_, err := ParseFlags([]string{"-argo-user", ""})
	if err == nil || !strings.Contains(err.Error(), "either -argo-user or -api-key-file is required for Argo provider") {
		t.Errorf("ParseFlags with empty Argo credentials should error, got: %v", err)
	}
}

func TestParseFlagsArgoAcceptsAPIKeyFile(t *testing.T) {
	cfg, err := ParseFlags([]string{"-provider", "argo", "-api-key-file", "test.key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.APIKeyFile != "test.key" {
		t.Errorf("APIKeyFile = %q; want %q", cfg.APIKeyFile, "test.key")
	}
}

func TestParseFlagsArgoDev(t *testing.T) {
	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-argo-dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.ArgoDev {
		t.Error("ArgoDev should be true")
	}
	if cfg.ArgoEnv != "dev" {
		t.Errorf("ArgoEnv = %q; want %q", cfg.ArgoEnv, "dev")
	}
}

func TestParseFlagsArgoTest(t *testing.T) {
	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-argo-test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.ArgoTest {
		t.Error("ArgoTest should be true")
	}
	if cfg.ArgoEnv != "test" {
		t.Errorf("ArgoEnv = %q; want %q", cfg.ArgoEnv, "test")
	}
}

func TestParseFlagsArgoDevAndTestConflict(t *testing.T) {
	_, err := ParseFlags([]string{"-argo-user", "testuser", "-argo-dev", "-argo-test"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "-argo-dev and -argo-test cannot be used together") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseFlagsArgoLegacy(t *testing.T) {
	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-argo-legacy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.ArgoLegacy {
		t.Error("ArgoLegacy should be true")
	}
}

func TestParseFlagsInvalidCombos(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"embed+stream", []string{"-argo-user", "testuser", "-e", "-stream"}, "embed mode cannot be used with stream"},
		{"embed+resume", []string{"-argo-user", "testuser", "-e", "-resume", "123"}, "embed mode cannot be used with session flags"},
		{"embed+branch", []string{"-argo-user", "testuser", "-e", "-branch", "123/456"}, "embed mode cannot be used with session flags"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlags(tt.args)
			if err == nil {
				t.Errorf("ParseFlags(%v) did not error", tt.args)
			} else if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseFlags(%v) error = %v, want error containing %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestParseFlagsCustom(t *testing.T) {
	args := []string{"-model", "modelA", "-argo-user", "alice", "-s", "sys", "-stream"}
	cfg, err := ParseFlags(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		Model:      "modelA",
		StreamChat: true,
		Options: providerconfig.Options{
			ArgoUser: "alice",
			ArgoEnv:  "prod",
			Provider: "argo",
		},
		System:              "sys",
		SystemExplicitlySet: true, // -s flag was provided
		Timeout:             10 * time.Minute,
		Retries:             3,                // Default value
		ToolTimeout:         60 * time.Second, // Default value
		MaxToolRounds:       64,               // Default value
		MaxToolParallel:     8,                // Default value
		ToolMaxOutputBytes:  1048576,          // Default value (1MB)
		LogLevel:            "INFO",           // Default value
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("ParseFlags = %+v; want %+v", cfg, want)
	}
}

func TestRequestOptionsAppliesCoreDefaults(t *testing.T) {
	cfg, err := ParseFlags([]string{
		"-argo-user", "alice",
		"-tool",
		"-tool-timeout", "0s",
		"-max-tool-rounds", "0",
		"-max-tool-parallel", "0",
		"-tool-max-output-bytes", "0",
	})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	opts := cfg.RequestOptions()
	if opts.GetEffectiveSystem() == cfg.System {
		t.Fatalf("effective system prompt was not switched for tool mode")
	}
	if got := opts.GetToolTimeout(); got != 60*time.Second {
		t.Fatalf("GetToolTimeout() = %v, want 1m0s", got)
	}
	if got := opts.GetMaxToolRounds(); got != 64 {
		t.Fatalf("GetMaxToolRounds() = %d, want 64", got)
	}
	if got := opts.GetMaxToolParallel(); got != 8 {
		t.Fatalf("GetMaxToolParallel() = %d, want 8", got)
	}
	if got := opts.GetToolMaxOutputBytes(); got != 1024*1024 {
		t.Fatalf("GetToolMaxOutputBytes() = %d, want 1MiB", got)
	}
}

// -tool-max-output-bytes used to accept any number and then capture a
// different one: RequestOptions.GetToolMaxOutputBytes rewrites a non-positive
// value to the 1MiB default and clamps anything past 100MiB, and nothing said
// so. An operator who asked for 500MB got 100MiB, and the truncation note the
// model plans its next command against quoted the clamped figure while the
// command line said otherwise.
func TestParseFlagsRejectsToolMaxOutputBytesThatWouldNotBeHonored(t *testing.T) {
	rejected := []struct {
		name  string
		value string
	}{
		{name: "negative", value: "-1"},
		{name: "above the ceiling", value: "209715200"},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlags([]string{"-argo-user", "alice", "-tool", "-tool-max-output-bytes", tt.value})
			if err == nil {
				t.Fatalf("ParseFlags(-tool-max-output-bytes %s) succeeded, want a rejection", tt.value)
			}
			for _, want := range []string{"-tool-max-output-bytes", "104857600"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not name %q", err, want)
				}
			}
		})
	}

	accepted := []struct {
		name  string
		value string
		want  int
	}{
		// Zero is how every other tool limit spells "use the default", and
		// TestRequestOptionsAppliesCoreDefaults pins that meaning.
		{name: "the default sentinel", value: "0", want: 1024 * 1024},
		{name: "a smaller cap", value: "4096", want: 4096},
		{name: "exactly the ceiling", value: "104857600", want: 104857600},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseFlags([]string{"-argo-user", "alice", "-tool", "-tool-max-output-bytes", tt.value})
			if err != nil {
				t.Fatalf("ParseFlags(-tool-max-output-bytes %s) = %v", tt.value, err)
			}
			if got := cfg.RequestOptions().GetToolMaxOutputBytes(); got != tt.want {
				t.Fatalf("GetToolMaxOutputBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseFlagsOutputOptions(t *testing.T) {
	schemaPath := t.TempDir() + "/schema.json"
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","properties":{"answer":{"type":"string"}}}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := ParseFlags([]string{
		"-argo-user", "alice",
		"-effort", "high",
		"-json-schema", schemaPath,
	})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	if cfg.Effort != "high" {
		t.Fatalf("Effort = %q, want high", cfg.Effort)
	}
	if string(cfg.JSONSchema) == "" {
		t.Fatal("JSONSchema was not loaded")
	}

	opts := cfg.RequestOptions()
	if opts.Effort != "high" {
		t.Fatalf("RequestOptions.Effort = %q, want high", opts.Effort)
	}
	if string(opts.GetJSONSchema()) != string(cfg.JSONSchema) {
		t.Fatalf("RequestOptions schema was not preserved")
	}
}

func TestParseFlagsPrintCurl(t *testing.T) {
	cfg, err := ParseFlags([]string{"-argo-user", "alice", "-print-curl"})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	if !cfg.PrintCurl {
		t.Fatal("PrintCurl = false, want true")
	}
}

func TestParseFlagsOutputOptionValidation(t *testing.T) {
	invalidSchemaPath := t.TempDir() + "/schema.json"
	if err := os.WriteFile(invalidSchemaPath, []byte(`not json`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	nullSchemaPath := t.TempDir() + "/null-schema.json"
	if err := os.WriteFile(nullSchemaPath, []byte(`null`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "json and schema conflict",
			args:    []string{"-argo-user", "alice", "-json", "-json-schema", invalidSchemaPath},
			wantErr: "-json and -json-schema cannot be used together",
		},
		{
			name:    "invalid effort",
			args:    []string{"-argo-user", "alice", "-effort", "extreme"},
			wantErr: "-effort must be one of",
		},
		{
			name:    "invalid reasoning mode",
			args:    []string{"-argo-user", "alice", "-reasoning-mode", "turbo"},
			wantErr: "-reasoning-mode must be one of",
		},
		{
			name:    "invalid reasoning context",
			args:    []string{"-argo-user", "alice", "-reasoning-context", "everything"},
			wantErr: "-reasoning-context must be one of",
		},
		{
			name:    "embed reasoning mode",
			args:    []string{"-argo-user", "alice", "-e", "-reasoning-mode", "pro"},
			wantErr: "only supported in chat mode",
		},
		{
			name:    "embed output option",
			args:    []string{"-argo-user", "alice", "-e", "-json"},
			wantErr: "only supported in chat mode",
		},
		{
			name:    "invalid schema json",
			args:    []string{"-argo-user", "alice", "-json-schema", invalidSchemaPath},
			wantErr: "valid JSON",
		},
		{
			name:    "null schema json",
			args:    []string{"-argo-user", "alice", "-json-schema", nullSchemaPath},
			wantErr: "JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlags(tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestEmbedModeAutoDisablesSessions(t *testing.T) {
	args := []string{"-argo-user", "testuser", "-e"}
	cfg, err := ParseFlags(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Embed {
		t.Error("Embed should be true")
	}
	if !cfg.NoSession {
		t.Error("NoSession should be automatically set to true in embed mode")
	}
}

func TestEmbedModeWithExplicitNoSessionFalse(t *testing.T) {
	args := []string{"-argo-user", "testuser", "-e", "-no-session=false"}
	_, err := ParseFlags(args)
	if err == nil {
		t.Error("Expected error when using -e with -no-session=false")
	}
	expectedErr := "invalid flag combination: embed mode requires sessions to be disabled"
	if err != nil && !strings.Contains(err.Error(), expectedErr) {
		t.Errorf("Expected error containing %q, got: %v", expectedErr, err)
	}
}

func TestEmbedModeWithExplicitNoSessionTrue(t *testing.T) {
	args := []string{"-argo-user", "testuser", "-e", "-no-session=true"}
	cfg, err := ParseFlags(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Embed {
		t.Error("Embed should be true")
	}
	if !cfg.NoSession {
		t.Error("NoSession should be true")
	}
}
