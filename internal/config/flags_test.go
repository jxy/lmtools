package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseFlagsDefaults(t *testing.T) {
	// Test with explicit user since it's now required if OS user is not available
	cfg, err := ParseFlags([]string{"-u", "testuser"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LogDir field has been removed - logs now always go to ~/.lmc/logs
	if cfg.User != "testuser" {
		t.Errorf("User = %q; want %q", cfg.User, "testuser")
	}
}

func TestParseFlagsRequiredUser(t *testing.T) {
	// Test that user is required if getDefaultUser returns empty
	// We can't easily mock getDefaultUser, but we can test with empty -u flag
	_, err := ParseFlags([]string{"-u", ""})
	if err == nil || !strings.Contains(err.Error(), "user identifier (-u) is required") {
		t.Errorf("ParseFlags with empty user should error about required user, got: %v", err)
	}
}

func TestParseFlagsEnvValidation(t *testing.T) {
	if _, err := ParseFlags([]string{"-u", "testuser", "-env", "foo"}); err == nil {
		t.Errorf("ParseFlags did not error for invalid env")
	}
}

func TestParseFlagsEnvCustomURL(t *testing.T) {
	custom := "https://custom.example/api"
	cfg, err := ParseFlags([]string{"-u", "testuser", "-env", custom})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != custom {
		t.Errorf("Env = %q; want %q", cfg.Env, custom)
	}
}

func TestParseFlagsInvalidCombos(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"embed+stream", []string{"-u", "testuser", "-e", "-stream"}, "embed mode cannot be used with stream"},
		{"embed+resume", []string{"-u", "testuser", "-e", "-resume", "123"}, "embed mode cannot be used with session flags"},
		{"embed+branch", []string{"-u", "testuser", "-e", "-branch", "123/456"}, "embed mode cannot be used with session flags"},
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
	args := []string{"-m", "modelA", "-u", "alice", "-s", "sys", "-stream"}
	cfg, err := ParseFlags(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		Model:      "modelA",
		StreamChat: true,
		User:       "alice",
		System:     "sys",
		Env:        "dev",
		Timeout:    10 * time.Minute,
		Retries:    3, // Default value
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("ParseFlags = %+v; want %+v", cfg, want)
	}
}

func TestEmbedModeAutoDisablesSessions(t *testing.T) {
	args := []string{"-u", "testuser", "-e"}
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
	args := []string{"-u", "testuser", "-e", "-no-session=false"}
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
	args := []string{"-u", "testuser", "-e", "-no-session=true"}
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
