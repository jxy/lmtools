package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfigureArgoAnthropicRequestUsesArgoUserAsAPIKey(t *testing.T) {
	server := &Server{config: &Config{ArgoUser: "argo-user-key"}}
	req := httptest.NewRequest(http.MethodPost, "http://argo.test/v1/messages", nil)

	server.configureArgoAnthropicRequest(req)

	if got := req.Header.Get("x-api-key"); got != "argo-user-key" {
		t.Fatalf("x-api-key = %q, want %q", got, "argo-user-key")
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want %q", got, "2023-06-01")
	}
}

func TestConfigureArgoOpenAIRequestUsesArgoUserAsBearerToken(t *testing.T) {
	server := &Server{config: &Config{ArgoUser: "argo-user-key"}}
	req := httptest.NewRequest(http.MethodPost, "http://argo.test/v1/chat/completions", nil)

	server.configureArgoOpenAIRequest(req)

	if got := req.Header.Get("Authorization"); got != "Bearer argo-user-key" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer argo-user-key")
	}
}

func TestConfigureArgoRequestPrefersExplicitAPIKey(t *testing.T) {
	server := &Server{config: &Config{ArgoAPIKey: "file-key", ArgoUser: "argo-user-key"}}
	req := httptest.NewRequest(http.MethodPost, "http://argo.test/v1/messages", nil)

	server.configureArgoAnthropicRequest(req)

	if got := req.Header.Get("x-api-key"); got != "file-key" {
		t.Fatalf("x-api-key = %q, want %q", got, "file-key")
	}
}
