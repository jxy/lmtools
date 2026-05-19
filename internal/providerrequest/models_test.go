package providerrequest

import (
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"lmtools/internal/providerconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildModelsRequestAppliesProviderAuthWithKeyFile(t *testing.T) {
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
			wantValue:  "Bearer test-key",
		},
		{
			name:       "anthropic",
			provider:   constants.ProviderAnthropic,
			wantHeader: "x-api-key",
			wantValue:  "test-key",
		},
		{
			name:       "google",
			provider:   constants.ProviderGoogle,
			wantHeader: "x-goog-api-key",
			wantValue:  "test-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := BuildModelsRequest(ModelsRequestOptions{
				ProviderOptions: providerconfig.Options{
					Provider:    tt.provider,
					ProviderURL: "http://localhost:8080/v1",
					APIKeyFile:  writeProviderRequestKeyFile(t, "test-key"),
				},
			})
			if err != nil {
				t.Fatalf("BuildModelsRequest() error = %v", err)
			}
			if got := req.Header.Get(tt.wantHeader); got != tt.wantValue {
				t.Fatalf("%s = %q, want %q", tt.wantHeader, got, tt.wantValue)
			}
		})
	}
}

func TestBuildModelsRequestProviderURLWithoutKeyAllowed(t *testing.T) {
	req, err := BuildModelsRequest(ModelsRequestOptions{
		ProviderOptions: providerconfig.Options{
			Provider:    constants.ProviderOpenAI,
			ProviderURL: "http://localhost:8080/v1",
		},
		RequireAPIKeyWithoutProviderURL: true,
	})
	if err != nil {
		t.Fatalf("BuildModelsRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
}

func TestBuildModelsRequestRequiresAPIKeyWithoutProviderURL(t *testing.T) {
	_, err := BuildModelsRequest(ModelsRequestOptions{
		ProviderOptions:                 providerconfig.Options{Provider: constants.ProviderOpenAI},
		RequireAPIKeyWithoutProviderURL: true,
	})
	if err == nil {
		t.Fatal("BuildModelsRequest() succeeded without key or provider URL")
	}
	if !strings.Contains(err.Error(), "-api-key-file is required for openai provider when listing models") {
		t.Fatalf("BuildModelsRequest() error = %q, want missing key error", err)
	}
}

func TestBuildModelsRequestAppliesExplicitProviderKey(t *testing.T) {
	providerKey, err := auth.NewProviderKey(constants.ProviderOpenAI, "explicit-key")
	if err != nil {
		t.Fatalf("NewProviderKey() error = %v", err)
	}
	req, err := BuildModelsRequest(ModelsRequestOptions{
		ProviderOptions: providerconfig.Options{
			Provider:    constants.ProviderOpenAI,
			ProviderURL: "http://localhost:8080/v1",
		},
		ProviderKey: &providerKey,
	})
	if err != nil {
		t.Fatalf("BuildModelsRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer explicit-key" {
		t.Fatalf("Authorization = %q, want Bearer explicit-key", got)
	}
}

func writeProviderRequestKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(key), constants.FilePerm); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
