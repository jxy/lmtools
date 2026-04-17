package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCompareFlagSetParsesValuesIntoReturnedStruct(t *testing.T) {
	fs := newCompareFlagSet("compare-all")
	if err := fs.flagSet.Parse([]string{"-case", "fixture-case", "-target", "argo-openai", "-against", "openai", "-live-against"}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if fs.caseID != "fixture-case" {
		t.Fatalf("caseID = %q, want fixture-case", fs.caseID)
	}
	if fs.targetID != "argo-openai" {
		t.Fatalf("targetID = %q, want argo-openai", fs.targetID)
	}
	if fs.againstID != "openai" {
		t.Fatalf("againstID = %q, want openai", fs.againstID)
	}
	if !fs.liveAgainst {
		t.Fatal("liveAgainst = false, want true")
	}
}

func TestLooksLikeSSE(t *testing.T) {
	if !looksLikeSSE([]byte("data: {\"ok\":true}\n\n")) {
		t.Fatal("looksLikeSSE(data event) = false, want true")
	}
	if !looksLikeSSE([]byte("event: message_start\ndata: {}\n\n")) {
		t.Fatal("looksLikeSSE(named event) = false, want true")
	}
	if looksLikeSSE([]byte("{\"error\":{\"message\":\"bad request\"}}")) {
		t.Fatal("looksLikeSSE(json error body) = true, want false")
	}
}

func TestSummarizeSSEErrorBody(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		body     string
		wantOK   bool
	}{
		{
			name:     "openai error chunk",
			provider: "openai",
			body:     "data: {\"error\":{\"message\":\"bad request\"}}\n\n",
			wantOK:   true,
		},
		{
			name:     "anthropic event error",
			provider: "anthropic",
			body:     "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"bad request\"}}\n\n",
			wantOK:   true,
		},
		{
			name:     "normal openai stream",
			provider: "openai",
			body:     "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		_, ok := summarizeSSEErrorBody(tt.provider, []byte(tt.body))
		if ok != tt.wantOK {
			t.Fatalf("%s: summarizeSSEErrorBody() ok=%v, want %v", tt.name, ok, tt.wantOK)
		}
	}
}

func TestDefaultComparisonTargetMapsArgoHostedCompatibilityTargets(t *testing.T) {
	tests := []struct {
		target targetConfig
		want   targetConfig
		ok     bool
	}{
		{
			target: targetConfig{ID: "argo-openai", Provider: "openai", Host: "argo"},
			want:   targetConfig{ID: "openai", Provider: "openai", Host: "openai"},
			ok:     true,
		},
		{
			target: targetConfig{ID: "argo-openai-stream", Provider: "openai", Host: "argo", Stream: true},
			want:   targetConfig{ID: "openai-stream", Provider: "openai", Host: "openai", Stream: true},
			ok:     true,
		},
		{
			target: targetConfig{ID: "argo-anthropic", Provider: "anthropic", Host: "argo"},
			want:   targetConfig{ID: "anthropic", Provider: "anthropic", Host: "anthropic"},
			ok:     true,
		},
		{
			target: targetConfig{ID: "argo-anthropic-stream", Provider: "anthropic", Host: "argo", Stream: true},
			want:   targetConfig{ID: "anthropic-stream", Provider: "anthropic", Host: "anthropic", Stream: true},
			ok:     true,
		},
		{
			target: targetConfig{ID: "openai", Provider: "openai"},
			ok:     false,
		},
	}

	for _, tt := range tests {
		got, ok := defaultComparisonTarget(tt.target)
		if ok != tt.ok {
			t.Fatalf("defaultComparisonTarget(%q) ok=%v, want %v", tt.target.ID, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Fatalf("defaultComparisonTarget(%q) = %+v, want %+v", tt.target.ID, got, tt.want)
		}
	}
}

func TestCompareCaseMatchesCheckedInCaptureByJSONShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_live","choices":[{"index":0,"message":{"role":"assistant","content":"live"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":7}}`))
	}))
	defer server.Close()

	t.Setenv("OPENAI_API_KEY", "test-openai-key")
	t.Setenv("OPENAI_API_FIXTURE_URL", server.URL)

	root := writeCompareFixtureCase(t, `{
  "id": "fixture-case",
  "description": "fixture case",
  "kinds": ["request"],
  "ingress_family": "openai",
  "models": {
    "openai": "gpt-5.4-nano",
    "anthropic": "claude-haiku-4-5",
    "google": "gemini-3.1-flash-lite-preview",
    "argo": "gpt5mini"
  },
  "capture_targets": ["openai"]
}
`, `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`+"\n")

	writeCompareRenderFile(t, root, "openai.request.json", `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`+"\n")
	writeCompareCaptureFile(t, root, "openai.response.json", `{"id":"chatcmpl_saved","choices":[{"index":0,"message":{"role":"assistant","content":"saved"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`+"\n")

	if _, err := compareCase(root, "fixture-case", "openai", "", false); err != nil {
		t.Fatalf("compareCase() error = %v", err)
	}
}

func TestCompareCaseDefaultsArgoHostedOpenAIAgainstUpstreamCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/argoapi/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer argo-key" {
				http.Error(w, "missing bearer auth", http.StatusUnauthorized)
				return
			}
			var decoded map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			if got, _ := decoded["model"].(string); got != "gpt5mini" {
				http.Error(w, "unexpected model", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl_live","choices":[{"index":0,"message":{"role":"assistant","content":"live"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("ARGO_API_KEY", "argo-key")
	t.Setenv("ARGO_API_FIXTURE_BASE_URL", server.URL+"/argoapi")

	root := writeCompareFixtureCase(t, `{
  "id": "fixture-case",
  "description": "fixture case",
  "kinds": ["request"],
  "ingress_family": "openai",
  "models": {
    "openai": "gpt-5.4-nano",
    "anthropic": "claude-haiku-4-5",
    "google": "gemini-3.1-flash-lite-preview",
    "argo": "gpt5mini"
  },
  "capture_targets": ["openai", "argo-openai"]
}
`, `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`+"\n")

	writeCompareRenderFile(t, root, "openai.request.json", `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`+"\n")
	writeCompareCaptureFile(t, root, "openai.response.json", `{"id":"chatcmpl_saved","choices":[{"index":0,"message":{"role":"assistant","content":"saved"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`+"\n")

	if _, err := compareCase(root, "fixture-case", "argo-openai", "", false); err != nil {
		t.Fatalf("compareCase() error = %v", err)
	}
}

func TestCompareCaseDetectsArgoHostedOpenAIParityMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/argoapi/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl_live","unexpected":"field"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Setenv("ARGO_API_KEY", "argo-key")
	t.Setenv("ARGO_API_FIXTURE_BASE_URL", server.URL+"/argoapi")

	root := writeCompareFixtureCase(t, `{
  "id": "fixture-case",
  "description": "fixture case",
  "kinds": ["request"],
  "ingress_family": "openai",
  "models": {
    "openai": "gpt-5.4-nano",
    "anthropic": "claude-haiku-4-5",
    "google": "gemini-3.1-flash-lite-preview",
    "argo": "gpt5mini"
  },
  "capture_targets": ["openai", "argo-openai"]
}
`, `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`+"\n")

	writeCompareRenderFile(t, root, "openai.request.json", `{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`+"\n")
	writeCompareCaptureFile(t, root, "openai.response.json", `{"id":"chatcmpl_saved","choices":[{"index":0,"message":{"role":"assistant","content":"saved"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`+"\n")

	_, err := compareCase(root, "fixture-case", "argo-openai", "", false)
	if err == nil {
		t.Fatal("expected compareCase() mismatch error")
	}
	if !strings.Contains(err.Error(), "json-shape mismatch") {
		t.Fatalf("error = %q, want json-shape mismatch", err)
	}
}

func writeCompareFixtureCase(t *testing.T, caseJSON string, ingressJSON string) string {
	t.Helper()

	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case")
	if err := os.MkdirAll(filepath.Join(caseDir, "expected", "render"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(caseDir, "captures"), 0o755); err != nil {
		t.Fatalf("MkdirAll(captures) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(caseJSON), 0o644); err != nil {
		t.Fatalf("WriteFile(case.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "ingress.json"), []byte(ingressJSON), 0o644); err != nil {
		t.Fatalf("WriteFile(ingress.json) error = %v", err)
	}
	return root
}

func writeCompareRenderFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render", name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

func writeCompareCaptureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "captures", name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}
