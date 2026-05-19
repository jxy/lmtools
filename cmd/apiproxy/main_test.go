package main

import (
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(t *testing.T) {
	// This is mainly a compilation test
	// We can't easily test main() as it starts a server
	t.Log("Main function exists and compiles")
}

func TestCompilation(t *testing.T) {
	// This test ensures the package compiles without errors
	t.Log("Package compiles successfully")
}

func TestRepeatableStringFlag(t *testing.T) {
	var values repeatableStringFlag
	if err := values.Set("^gpt-4o$=gpt-5"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := values.Set("^claude-.*=claude-opus-4-1"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got, want := len(values), 2; got != want {
		t.Fatalf("len(values) = %d, want %d", got, want)
	}
	if values[0] != "^gpt-4o$=gpt-5" || values[1] != "^claude-.*=claude-opus-4-1" {
		t.Fatalf("values not preserved in order: %#v", values)
	}
}

func TestLoadProviderKeysRoutesSelectedProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     auth.ProviderKeySet
	}{
		{
			name:     "openai",
			provider: constants.ProviderOpenAI,
			want:     auth.ProviderKeySet{OpenAIAPIKey: "proxy-key"},
		},
		{
			name:     "anthropic",
			provider: constants.ProviderAnthropic,
			want:     auth.ProviderKeySet{AnthropicAPIKey: "proxy-key"},
		},
		{
			name:     "google",
			provider: constants.ProviderGoogle,
			want:     auth.ProviderKeySet{GoogleAPIKey: "proxy-key"},
		},
		{
			name:     "argo",
			provider: constants.ProviderArgo,
			want:     auth.ProviderKeySet{ArgoAPIKey: "proxy-key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadProviderKeys(tt.provider, writeApiproxyTestKeyFile(t, "proxy-key"))
			if err != nil {
				t.Fatalf("loadProviderKeys() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("loadProviderKeys() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLoadProviderKeysEmptyFilePathReturnsEmptySet(t *testing.T) {
	got, err := loadProviderKeys(constants.ProviderOpenAI, "")
	if err != nil {
		t.Fatalf("loadProviderKeys() error = %v", err)
	}
	if got != (auth.ProviderKeySet{}) {
		t.Fatalf("loadProviderKeys() = %#v, want empty set", got)
	}
}

func writeApiproxyTestKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(key), constants.FilePerm); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
