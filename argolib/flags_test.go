package argo

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
	if cfg.LogDir != defaultLogDir {
		t.Errorf("LogDir = %q; want %q", cfg.LogDir, defaultLogDir)
	}
	if cfg.User != "testuser" {
		t.Errorf("User = %q; want %q", cfg.User, "testuser")
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q; want %q", cfg.LogLevel, DefaultLogLevel)
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
		name string
		args []string
	}{
		{"embed+stream", []string{"-u", "testuser", "-e", "-stream"}},
		{"embed+prompt-chat", []string{"-u", "testuser", "-e", "-prompt-chat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseFlags(tt.args); err == nil {
				t.Errorf("ParseFlags(%v) did not error", tt.args)
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
		Model:       "modelA",
		StreamChat:  true,
		LogDir:      defaultLogDir,
		User:        "alice",
		System:      "sys",
		Env:         "dev",
		Timeout:     10 * time.Minute,
		LogLevel:    DefaultLogLevel,
		Retries:     3,               // Default value
		BackoffTime: 1 * time.Second, // Default value
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("ParseFlags = %+v; want %+v", cfg, want)
	}
}
