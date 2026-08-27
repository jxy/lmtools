package main

import (
	"bytes"
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// apiproxy replaces flag's own usage output with a hand-written one, so the
// default it advertises is a second copy of a number nothing checks. It went
// stale the moment the real default moved from 10MB to 512MB, and a 50x
// understatement is worst exactly where the help is read for it: a
// default-sized request costs the proxy well over a gigabyte to convert.
func TestUsageAdvertisesRequestBodyDefault(t *testing.T) {
	if want := int64(constants.DefaultMaxRequestBodySize) / bytesPerMB; defaultMaxRequestBodySizeMB != want {
		t.Fatalf("-max-request-body-size default = %d, want %d MB from constants.DefaultMaxRequestBodySize", defaultMaxRequestBodySizeMB, want)
	}

	var usage bytes.Buffer
	printUsage(&usage)
	line := ""
	for _, candidate := range strings.Split(usage.String(), "\n") {
		if strings.Contains(candidate, "-max-request-body-size") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatalf("usage does not mention -max-request-body-size:\n%s", usage.String())
	}
	if want := "(default: " + strconv.FormatInt(defaultMaxRequestBodySizeMB, 10) + ")"; !strings.Contains(line, want) {
		t.Fatalf("usage line = %q, want it to advertise %q", line, want)
	}
}

func TestUsageAdvertisesEncryptedReasoningRecovery(t *testing.T) {
	var usage bytes.Buffer
	printUsage(&usage)
	if !strings.Contains(usage.String(), "-strip-encrypted-reasoning") {
		t.Fatalf("usage does not mention encrypted reasoning recovery:\n%s", usage.String())
	}
}

func TestRequestBodyLimitBytes(t *testing.T) {
	maxWholeMB := int64(math.MaxInt64) / bytesPerMB
	for _, tc := range []struct {
		name      string
		megabytes int64
		want      int64
		wantError bool
	}{
		{name: "one MB", megabytes: 1, want: bytesPerMB},
		{name: "largest representable whole MB", megabytes: maxWholeMB, want: maxWholeMB * bytesPerMB},
		{name: "zero", megabytes: 0, wantError: true},
		{name: "negative", megabytes: -1, wantError: true},
		{name: "overflow", megabytes: maxWholeMB + 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requestBodyLimitBytes(tc.megabytes)
			if (err != nil) != tc.wantError {
				t.Fatalf("requestBodyLimitBytes(%d) error = %v, wantError %v", tc.megabytes, err, tc.wantError)
			}
			if got != tc.want {
				t.Fatalf("requestBodyLimitBytes(%d) = %d, want %d", tc.megabytes, got, tc.want)
			}
		})
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
