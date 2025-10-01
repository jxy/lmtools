package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

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

func TestParseFlagsRequiredUser(t *testing.T) {
	// Test that user is required if getDefaultUser returns empty
	// We can't easily mock getDefaultUser, but we can test with empty -argo-user flag
	_, err := ParseFlags([]string{"-argo-user", ""})
	if err == nil || !strings.Contains(err.Error(), "user identifier (-argo-user) is required") {
		t.Errorf("ParseFlags with empty user should error about required user, got: %v", err)
	}
}

func TestParseFlagsEnvValidation(t *testing.T) {
	if _, err := ParseFlags([]string{"-argo-user", "testuser", "-argo-env", "foo"}); err == nil {
		t.Errorf("ParseFlags did not error for invalid env")
	}
}

func TestParseFlagsEnvCustomURL(t *testing.T) {
	custom := "https://custom.example/api"
	cfg, err := ParseFlags([]string{"-argo-user", "testuser", "-argo-env", custom})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ArgoEnv != custom {
		t.Errorf("ArgoEnv = %q; want %q", cfg.ArgoEnv, custom)
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
		Model:               "modelA",
		StreamChat:          true,
		ArgoUser:            "alice",
		System:              "sys",
		SystemExplicitlySet: true, // -s flag was provided
		ArgoEnv:             "dev",
		Timeout:             10 * time.Minute,
		Retries:             3,                // Default value
		Provider:            "argo",           // Default value
		ToolTimeout:         30 * time.Second, // Default value
		MaxToolRounds:       32,               // Default value
		MaxToolParallel:     4,                // Default value
		ToolMaxOutputBytes:  1048576,          // Default value (1MB)
		LogLevel:            "INFO",           // Default value
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("ParseFlags = %+v; want %+v", cfg, want)
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
