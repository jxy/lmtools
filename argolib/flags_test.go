package argo

import (
	"reflect"
	"testing"
	"time"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := ParseFlags([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogDir != defaultLogDir {
		t.Errorf("LogDir = %q; want %q", cfg.LogDir, defaultLogDir)
	}
	if cfg.User != "xjin" {
		t.Errorf("User = %q; want %q", cfg.User, "xjin")
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q; want %q", cfg.LogLevel, DefaultLogLevel)
	}
}

func TestParseFlagsEnvValidation(t *testing.T) {
	if _, err := ParseFlags([]string{"-env", "foo"}); err == nil {
		t.Errorf("ParseFlags did not error for invalid env")
	}
}

func TestParseFlagsEnvCustomURL(t *testing.T) {
	custom := "https://custom.example/api"
	cfg, err := ParseFlags([]string{"-env", custom})
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
		{"embed+stream", []string{"-e", "-stream"}},
		{"embed+prompt-chat", []string{"-e", "-prompt-chat"}},
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
		Model:      "modelA",
		StreamChat: true,
		LogDir:     defaultLogDir,
		User:       "alice",
		System:     "sys",
		Env:        "dev",
		Timeout:    10 * time.Minute,
		LogLevel:   DefaultLogLevel,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("ParseFlags = %+v; want %+v", cfg, want)
	}
}
