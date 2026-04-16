package auth

import (
	"lmtools/internal/constants"
	"net/http"
	"testing"
)

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
