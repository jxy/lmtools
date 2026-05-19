package auth

import (
	"lmtools/internal/constants"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProviderKeyFileAppliesProviderCredentials(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		wantHeader string
		wantValue  string
	}{
		{
			name:       "openai",
			provider:   constants.ProviderOpenAI,
			wantHeader: "Authorization",
			wantValue:  "Bearer shared-key",
		},
		{
			name:       "anthropic",
			provider:   constants.ProviderAnthropic,
			wantHeader: "x-api-key",
			wantValue:  "shared-key",
		},
		{
			name:       "google",
			provider:   constants.ProviderGoogle,
			wantHeader: "x-goog-api-key",
			wantValue:  "shared-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providerKey, err := LoadProviderKeyFile(tt.provider, writeAuthTestKeyFile(t, "shared-key"))
			if err != nil {
				t.Fatalf("LoadProviderKeyFile() error = %v", err)
			}
			req, err := http.NewRequest(http.MethodPost, "https://example.test/v1", nil)
			if err != nil {
				t.Fatalf("http.NewRequest() error = %v", err)
			}
			if err := providerKey.Apply(req); err != nil {
				t.Fatalf("ProviderKey.Apply() error = %v", err)
			}
			if got := req.Header.Get(tt.wantHeader); got != tt.wantValue {
				t.Fatalf("%s = %q, want %q", tt.wantHeader, got, tt.wantValue)
			}
		})
	}
}

func TestProviderKeySetRoutesKeyToSelectedProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     ProviderKeySet
	}{
		{
			name:     "openai",
			provider: constants.ProviderOpenAI,
			want:     ProviderKeySet{OpenAIAPIKey: "shared-key"},
		},
		{
			name:     "anthropic",
			provider: constants.ProviderAnthropic,
			want:     ProviderKeySet{AnthropicAPIKey: "shared-key"},
		},
		{
			name:     "google",
			provider: constants.ProviderGoogle,
			want:     ProviderKeySet{GoogleAPIKey: "shared-key"},
		},
		{
			name:     "argo",
			provider: constants.ProviderArgo,
			want:     ProviderKeySet{ArgoAPIKey: "shared-key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providerKey, err := LoadProviderKeyFile(tt.provider, writeAuthTestKeyFile(t, "shared-key"))
			if err != nil {
				t.Fatalf("LoadProviderKeyFile() error = %v", err)
			}
			if got := providerKey.Set(); got != tt.want {
				t.Fatalf("ProviderKey.Set() = %#v, want %#v", got, tt.want)
			}
			if got := providerKey.Set().KeyForProvider(tt.provider); got != "shared-key" {
				t.Fatalf("KeyForProvider(%q) = %q, want shared-key", tt.provider, got)
			}
		})
	}
}

func TestLoadProviderKeyFileRejectsUnknownProvider(t *testing.T) {
	_, err := LoadProviderKeyFile("unknown", writeAuthTestKeyFile(t, "shared-key"))
	if err == nil {
		t.Fatal("LoadProviderKeyFile() succeeded for unknown provider")
	}
}

func TestApplyProviderCredentialsGoogleUsesHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	if err := ApplyProviderCredentials(req, constants.ProviderGoogle, "test-google-key"); err != nil {
		t.Fatalf("ApplyProviderCredentials() error = %v", err)
	}

	if got := req.Header.Get("x-goog-api-key"); got != "test-google-key" {
		t.Fatalf("x-goog-api-key = %q, want test-google-key", got)
	}
	if got := req.URL.RawQuery; got != "" {
		t.Fatalf("RawQuery = %q, want empty", got)
	}
}

func writeAuthTestKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
