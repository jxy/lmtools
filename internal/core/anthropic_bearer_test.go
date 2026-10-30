package core

import (
	"os"
	"path/filepath"
	"testing"
)

// The ALCF inference gateway serves Anthropic Messages but reads only
// Authorization: Bearer; -anthropic-bearer sends the key there as well.
func TestAnthropicBearerSendsTheKeyInBothHeaders(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "inference.key")
	if err := os.WriteFile(keyFile, []byte("alcf-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bearer := range []bool{false, true} {
		cfg := newProviderSpecTestConfig("anthropic", "inkling-bf16", "https://gateway.test/resource_server/minerva/api/v1")
		cfg.APIKeyFile = keyFile
		cfg.AnthropicBearer = bearer
		for _, stream := range []bool{false, true} {
			cfg.StreamChat = stream
			req, _, err := BuildRequest(cfg, "hi")
			if err != nil {
				t.Fatal(err)
			}
			wantAuthorization := ""
			if bearer {
				wantAuthorization = "Bearer alcf-token"
			}
			if got := req.Header.Get("x-api-key"); got != "alcf-token" {
				t.Errorf("bearer=%v stream=%v: x-api-key = %q", bearer, stream, got)
			}
			if got := req.Header.Get("Authorization"); got != wantAuthorization {
				t.Errorf("bearer=%v stream=%v: Authorization = %q, want %q", bearer, stream, got, wantAuthorization)
			}
		}
	}
}
