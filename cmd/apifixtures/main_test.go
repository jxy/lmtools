package main

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/apifixtures"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRequestMeta() apifixtures.CaseMeta {
	return apifixtures.CaseMeta{
		ID: "fixture-case",
		Models: map[string]string{
			"openai":    "gpt-5.4-nano",
			"anthropic": "claude-haiku-4-5",
			"google":    "gemini-3.1-flash-lite-preview",
			"argo":      "gpt5mini",
		},
	}
}

func TestParseSubcommandArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantCmd   string
		wantArgs  []string
		wantValid bool
	}{
		{
			name:      "plain subcommand",
			args:      []string{"compare-all", "-target", "argo-openai"},
			wantCmd:   "compare-all",
			wantArgs:  []string{"-target", "argo-openai"},
			wantValid: true,
		},
		{
			name:      "go-run separator",
			args:      []string{"--", "compare-all", "-target", "argo-openai"},
			wantCmd:   "compare-all",
			wantArgs:  []string{"-target", "argo-openai"},
			wantValid: true,
		},
		{
			name:      "empty",
			args:      nil,
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCmd, gotArgs, gotValid := parseSubcommandArgs(tt.args)
			if gotValid != tt.wantValid {
				t.Fatalf("parseSubcommandArgs(%v) valid=%v, want %v", tt.args, gotValid, tt.wantValid)
			}
			if gotCmd != tt.wantCmd {
				t.Fatalf("parseSubcommandArgs(%v) cmd=%q, want %q", tt.args, gotCmd, tt.wantCmd)
			}
			if strings.Join(gotArgs, "\x00") != strings.Join(tt.wantArgs, "\x00") {
				t.Fatalf("parseSubcommandArgs(%v) args=%v, want %v", tt.args, gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestCaptureModelForTarget(t *testing.T) {
	meta := apifixtures.CaseMeta{
		ID: "fixture-case",
		Models: map[string]string{
			"openai":         "gpt-5.4-nano",
			"anthropic":      "claude-haiku-4-5",
			"argo":           "gpt5mini",
			"argo-openai":    "gpt5mini",
			"argo-anthropic": "claudesonnet4",
		},
	}

	if got := captureModelForTarget(meta, targetConfig{ID: "argo-openai", Provider: "openai", Host: "argo"}); got != "gpt5mini" {
		t.Fatalf("captureModelForTarget(argo-openai) = %q, want gpt5mini", got)
	}
	if got := captureModelForTarget(meta, targetConfig{ID: "argo-anthropic", Provider: "anthropic", Host: "argo"}); got != "claudesonnet4" {
		t.Fatalf("captureModelForTarget(argo-anthropic) = %q, want claudesonnet4", got)
	}

	meta = apifixtures.CaseMeta{
		ID: "fixture-case",
		Models: map[string]string{
			"openai":    "gpt-5.4-nano",
			"anthropic": "claude-haiku-4-5",
			"argo":      "gpt5mini",
		},
	}
	if got := captureModelForTarget(meta, targetConfig{ID: "argo-anthropic", Provider: "anthropic", Host: "argo"}); got != "claude-haiku-4-5" {
		t.Fatalf("captureModelForTarget(argo-anthropic fallback) = %q, want claude-haiku-4-5", got)
	}
}

func TestCaptureRequestRelPrefersCaptureOverride(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai.capture.request.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := captureRequestRel(root, "fixture-case", targetConfig{ID: "openai", Provider: "openai"})
	want := "expected/render/openai.capture.request.json"
	if got != want {
		t.Fatalf("captureRequestRel() = %q, want %q", got, want)
	}
}

func TestCaptureRequestRelFallsBackToRenderedRequest(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai.request.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := captureRequestRel(root, "fixture-case", targetConfig{ID: "openai", Provider: "openai"})
	want := "expected/render/openai.request.json"
	if got != want {
		t.Fatalf("captureRequestRel() = %q, want %q", got, want)
	}
}

func TestCaptureRequestRelPrefersTargetSpecificStreamOverride(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai.capture.request.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai-stream.capture.request.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := captureRequestRel(root, "fixture-case", targetConfig{ID: "openai-stream", Provider: "openai", Stream: true})
	want := "expected/render/openai-stream.capture.request.json"
	if got != want {
		t.Fatalf("captureRequestRel() = %q, want %q", got, want)
	}
}

func TestCaptureRequestRelFallsBackToWireFormatForArgoHostedTarget(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai.request.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got := captureRequestRel(root, "fixture-case", targetConfig{ID: "argo-openai", Provider: "openai", Host: "argo"})
	want := "expected/render/openai.request.json"
	if got != want {
		t.Fatalf("captureRequestRel() = %q, want %q", got, want)
	}
}

func TestCaptureCaseRejectsUnsupportedTarget(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "case.json"), []byte(`{
  "id": "fixture-case",
  "description": "fixture case",
  "kinds": ["request"],
  "capture_targets": ["openai"]
}
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := captureCase(root, "fixture-case", "anthropic")
	if err == nil {
		t.Fatal("expected captureCase() to reject unsupported target")
	}
	if !strings.Contains(err.Error(), `does not support capture target "anthropic"`) {
		t.Fatalf("captureCase() error = %q, want unsupported target message", err)
	}
}

func TestRefreshDerivedArtifactsWritesParsedResponse(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	meta := apifixtures.CaseMeta{
		ID:       "fixture-case",
		Kinds:    []string{"request", "response"},
		Provider: "anthropic",
	}
	target := targetConfig{ID: "anthropic", Provider: "anthropic"}
	data := []byte(`{
  "content": [
    {
      "type": "text",
      "text": "Hello from Claude."
    }
  ]
}`)

	if err := refreshDerivedArtifacts(root, meta, target, data); err != nil {
		t.Fatalf("refreshDerivedArtifacts() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(caseDir, "parsed.json"))
	if err != nil {
		t.Fatalf("ReadFile(parsed.json) error = %v", err)
	}
	if !strings.Contains(string(got), `"Hello from Claude."`) {
		t.Fatalf("parsed.json = %s, want refreshed parsed response", got)
	}
}

func TestRefreshDerivedArtifactsWritesParsedModels(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "models-openai", "expected")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	meta := apifixtures.CaseMeta{
		ID:       "models-openai",
		Kinds:    []string{"models"},
		Provider: "openai",
	}
	target := targetConfig{ID: "openai", Provider: "openai"}
	data := []byte(`{
  "object": "list",
  "data": [
    {
      "id": "gpt-5",
      "object": "model",
      "created": 1700000000,
      "owned_by": "openai"
    }
  ]
}`)

	if err := refreshDerivedArtifacts(root, meta, target, data); err != nil {
		t.Fatalf("refreshDerivedArtifacts() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(caseDir, "parsed.json"))
	if err != nil {
		t.Fatalf("ReadFile(parsed.json) error = %v", err)
	}
	if !strings.Contains(string(got), `"gpt-5"`) || strings.Contains(string(got), `"created"`) {
		t.Fatalf("parsed.json = %s, want deterministic model projection without created", got)
	}
}

func TestLoadCaptureRequestBodyEnablesOpenAIStreaming(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai.capture.request.json"), []byte(`{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":8}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	body, err := loadCaptureRequestBody(root, "fixture-case", testRequestMeta(), targetConfig{ID: "openai-stream", Provider: "openai", Stream: true})
	if err != nil {
		t.Fatalf("loadCaptureRequestBody() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got, ok := decoded["stream"].(bool); !ok || !got {
		t.Fatalf("stream = %v, want true", decoded["stream"])
	}
}

func TestLoadCaptureRequestBodyEnablesAnthropicStreaming(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "anthropic.request.json"), []byte(`{"model":"claude-haiku-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	body, err := loadCaptureRequestBody(root, "fixture-case", testRequestMeta(), targetConfig{ID: "anthropic-stream", Provider: "anthropic", Stream: true})
	if err != nil {
		t.Fatalf("loadCaptureRequestBody() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got, ok := decoded["stream"].(bool); !ok || !got {
		t.Fatalf("stream = %v, want true", decoded["stream"])
	}
}

func TestLoadCaptureRequestBodyEnablesArgoHostedOpenAIStreamingWithoutLegacyUserInjection(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai.request.json"), []byte(`{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":8}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	body, err := loadCaptureRequestBody(root, "fixture-case", testRequestMeta(), targetConfig{ID: "argo-openai-stream", Provider: "openai", Host: "argo", Stream: true})
	if err != nil {
		t.Fatalf("loadCaptureRequestBody() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got, ok := decoded["stream"].(bool); !ok || !got {
		t.Fatalf("stream = %v, want true", decoded["stream"])
	}
	if got, ok := decoded["model"].(string); !ok || got != "gpt5mini" {
		t.Fatalf("model = %v, want models.argo rewrite", decoded["model"])
	}
	if _, exists := decoded["user"]; exists {
		t.Fatalf("user = %v, want no legacy Argo user injection", decoded["user"])
	}
}

func TestLoadCaptureRequestBodyRewritesArgoHostedAnthropicModelAndEnablesStreaming(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "anthropic.request.json"), []byte(`{"model":"claude-haiku-4-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	meta := testRequestMeta()
	meta.Models["argo"] = "gpt5mini"
	body, err := loadCaptureRequestBody(root, "fixture-case", meta, targetConfig{ID: "argo-anthropic-stream", Provider: "anthropic", Host: "argo", Stream: true})
	if err != nil {
		t.Fatalf("loadCaptureRequestBody() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got, ok := decoded["stream"].(bool); !ok || !got {
		t.Fatalf("stream = %v, want true", decoded["stream"])
	}
	if got, ok := decoded["model"].(string); !ok || got != "claude-haiku-4-5" {
		t.Fatalf("model = %v, want anthropic-compatible fallback rewrite", decoded["model"])
	}
	if _, exists := decoded["user"]; exists {
		t.Fatalf("user = %v, want no legacy Argo user injection", decoded["user"])
	}
}

func TestLoadCaptureRequestBodyLeavesGoogleStreamingBodyUntouched(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	want := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":8}}` + "\n"
	if err := os.WriteFile(filepath.Join(caseDir, "google.request.json"), []byte(want), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	body, err := loadCaptureRequestBody(root, "fixture-case", testRequestMeta(), targetConfig{ID: "google-stream", Provider: "google", Stream: true})
	if err != nil {
		t.Fatalf("loadCaptureRequestBody() error = %v", err)
	}

	wantCanonical, err := apifixtures.CanonicalJSON([]byte(want))
	if err != nil {
		t.Fatalf("CanonicalJSON(want) error = %v", err)
	}
	gotCanonical, err := apifixtures.CanonicalJSON(body)
	if err != nil {
		t.Fatalf("CanonicalJSON(body) error = %v", err)
	}
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("body = %s, want unchanged google request", body)
	}
}

func TestLoadCaptureRequestBodyInjectsArgoAPIKey(t *testing.T) {
	t.Setenv("ARGO_API_KEY", "secret-argo-token")

	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "argo.request.json"), []byte(`{"user":"fixture-user","model":"gpt5mini","messages":[{"role":"user","content":"hi"}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	body, err := loadCaptureRequestBody(root, "fixture-case", testRequestMeta(), targetConfig{ID: "argo", Provider: "argo"})
	if err != nil {
		t.Fatalf("loadCaptureRequestBody() error = %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got, ok := decoded["user"].(string); !ok || got != "secret-argo-token" {
		t.Fatalf("user = %v, want injected ARGO_API_KEY", decoded["user"])
	}
}

func TestLoadCaptureRequestBodyRejectsArgoWithoutAPIKey(t *testing.T) {
	t.Setenv("ARGO_API_KEY", "")

	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "argo.request.json"), []byte(`{"user":"fixture-user","model":"gpt5mini","messages":[{"role":"user","content":"hi"}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := loadCaptureRequestBody(root, "fixture-case", testRequestMeta(), targetConfig{ID: "argo", Provider: "argo"})
	if err == nil {
		t.Fatal("expected loadCaptureRequestBody() error")
	}
	if !strings.Contains(err.Error(), "ARGO_API_KEY is required") {
		t.Fatalf("error = %q, want missing ARGO_API_KEY message", err)
	}
}

func TestLoadCaptureRequestBodyRejectsArgoHostedCompatibilityTargetWithoutArgoModel(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "fixture-case", "expected", "render")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "openai.request.json"), []byte(`{"model":"gpt-5.4-nano","messages":[{"role":"user","content":"hi"}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := loadCaptureRequestBody(root, "fixture-case", apifixtures.CaseMeta{ID: "fixture-case"}, targetConfig{ID: "argo-openai", Provider: "openai", Host: "argo"})
	if err == nil {
		t.Fatal("expected loadCaptureRequestBody() error")
	}
	if !strings.Contains(err.Error(), "missing a compatible model") {
		t.Fatalf("error = %q, want missing compatible model message", err)
	}
}

func TestEndpointForTargetUsesStreamingEndpoint(t *testing.T) {
	t.Run("google-stream", func(t *testing.T) {
		t.Setenv("GOOGLE_API_KEY", "test-google-key")
		url, headers, err := endpointForTarget(targetConfig{ID: "google-stream", Provider: "google", Stream: true}, apifixtures.CaseMeta{
			ID:     "fixture-case",
			Models: map[string]string{"google": "gemini-3.1-flash-lite-preview"},
		})
		if err != nil {
			t.Fatalf("endpointForTarget() error = %v", err)
		}
		if got, want := url, "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-flash-lite-preview:streamGenerateContent?alt=sse"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["x-goog-api-key"]; got != "test-google-key" {
			t.Fatalf("x-goog-api-key = %q, want test-google-key", got)
		}
	})

	t.Run("argo-stream", func(t *testing.T) {
		url, headers, err := endpointForTarget(targetConfig{ID: "argo-stream", Provider: "argo", Host: "argo", Stream: true}, apifixtures.CaseMeta{})
		if err != nil {
			t.Fatalf("endpointForTarget() error = %v", err)
		}
		if got, want := url, "https://apps.inside.anl.gov/argoapi/api/v1/resource/streamchat/"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if len(headers) != 0 {
			t.Fatalf("headers = %v, want none", headers)
		}
	})

	t.Run("argo", func(t *testing.T) {
		url, headers, err := endpointForTarget(targetConfig{ID: "argo", Provider: "argo", Host: "argo"}, apifixtures.CaseMeta{})
		if err != nil {
			t.Fatalf("endpointForTarget() error = %v", err)
		}
		if got, want := url, "https://apps.inside.anl.gov/argoapi/api/v1/resource/chat/"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if len(headers) != 0 {
			t.Fatalf("headers = %v, want none", headers)
		}
	})

	t.Run("argo-openai", func(t *testing.T) {
		t.Setenv("ARGO_API_KEY", "argo-key")
		url, headers, err := endpointForTarget(targetConfig{ID: "argo-openai", Provider: "openai", Host: "argo"}, apifixtures.CaseMeta{})
		if err != nil {
			t.Fatalf("endpointForTarget() error = %v", err)
		}
		if got, want := url, "https://apps.inside.anl.gov/argoapi/v1/chat/completions"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["Authorization"]; got != "Bearer argo-key" {
			t.Fatalf("Authorization = %q, want bearer ARGO_API_KEY", got)
		}
	})

	t.Run("argo-anthropic", func(t *testing.T) {
		t.Setenv("ARGO_API_KEY", "argo-key")
		url, headers, err := endpointForTarget(targetConfig{ID: "argo-anthropic-stream", Provider: "anthropic", Host: "argo", Stream: true}, apifixtures.CaseMeta{})
		if err != nil {
			t.Fatalf("endpointForTarget() error = %v", err)
		}
		if got, want := url, "https://apps.inside.anl.gov/argoapi/v1/messages"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["x-api-key"]; got != "argo-key" {
			t.Fatalf("x-api-key = %q, want ARGO_API_KEY", got)
		}
		if got := headers["anthropic-version"]; got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
		}
		if got := headers["Accept"]; got != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", got)
		}
	})
}

func TestEndpointForTargetAcceptsRootOrLegacyArgoBase(t *testing.T) {
	t.Run("legacy resource base", func(t *testing.T) {
		got, err := argoFixtureEndpoints("https://apps.inside.anl.gov/argoapi/api/v1/resource")
		if err != nil {
			t.Fatalf("argoFixtureEndpoints() error = %v", err)
		}
		if got.root != "https://apps.inside.anl.gov/argoapi" {
			t.Fatalf("root = %q, want /argoapi root", got.root)
		}
		if got.openAI != "https://apps.inside.anl.gov/argoapi/v1/chat/completions" {
			t.Fatalf("openAI = %q", got.openAI)
		}
		if got.anthropic != "https://apps.inside.anl.gov/argoapi/v1/messages" {
			t.Fatalf("anthropic = %q", got.anthropic)
		}
	})

	t.Run("root base", func(t *testing.T) {
		got, err := argoFixtureEndpoints("https://apps.inside.anl.gov/argoapi")
		if err != nil {
			t.Fatalf("argoFixtureEndpoints() error = %v", err)
		}
		if got.legacyChat != "https://apps.inside.anl.gov/argoapi/api/v1/resource/chat/" {
			t.Fatalf("legacyChat = %q", got.legacyChat)
		}
		if got.legacyStream != "https://apps.inside.anl.gov/argoapi/api/v1/resource/streamchat/" {
			t.Fatalf("legacyStream = %q", got.legacyStream)
		}
	})
}

func TestModelsEndpointForTarget(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "openai-key")
		url, headers, err := modelsEndpointForTarget(targetConfig{ID: "openai", Provider: "openai"})
		if err != nil {
			t.Fatalf("modelsEndpointForTarget() error = %v", err)
		}
		if got, want := url, "https://api.openai.com/v1/models"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["Authorization"]; got != "Bearer openai-key" {
			t.Fatalf("Authorization = %q, want bearer OPENAI_API_KEY", got)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
		url, headers, err := modelsEndpointForTarget(targetConfig{ID: "anthropic", Provider: "anthropic"})
		if err != nil {
			t.Fatalf("modelsEndpointForTarget() error = %v", err)
		}
		if got, want := url, "https://api.anthropic.com/v1/models"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["x-api-key"]; got != "anthropic-key" {
			t.Fatalf("x-api-key = %q, want ANTHROPIC_API_KEY", got)
		}
	})

	t.Run("google", func(t *testing.T) {
		t.Setenv("GOOGLE_API_KEY", "google-key")
		url, headers, err := modelsEndpointForTarget(targetConfig{ID: "google", Provider: "google"})
		if err != nil {
			t.Fatalf("modelsEndpointForTarget() error = %v", err)
		}
		if got, want := url, "https://generativelanguage.googleapis.com/v1beta/models"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["x-goog-api-key"]; got != "google-key" {
			t.Fatalf("x-goog-api-key = %q, want GOOGLE_API_KEY", got)
		}
	})

	t.Run("argo", func(t *testing.T) {
		url, headers, err := modelsEndpointForTarget(targetConfig{ID: "argo", Provider: "argo"})
		if err != nil {
			t.Fatalf("modelsEndpointForTarget() error = %v", err)
		}
		if got, want := url, "https://apps.inside.anl.gov/argoapi/api/v1/models/"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if len(headers) != 0 {
			t.Fatalf("headers = %v, want none", headers)
		}
	})
}

func TestDoCaptureRequestRetriesRateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	cfg := &retry.Config{
		MaxRetries:     3,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2,
	}
	resp, body, err := doCaptureRequest(context.Background(), server.Client(), req, []byte(`{"hello":"world"}`), "openai", cfg)
	if err != nil {
		t.Fatalf("doCaptureRequest() error = %v", err)
	}
	defer resp.Body.Close()

	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := string(body); got != `{"ok":true}` {
		t.Fatalf("body = %q, want success body", got)
	}
}

func TestDoCaptureRequestReturnsLastRetryableResponse(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintf(w, `{"attempt":%d}`, attempts)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	cfg := &retry.Config{
		MaxRetries:     2,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		BackoffFactor:  2,
	}
	resp, body, err := doCaptureRequest(context.Background(), server.Client(), req, []byte(`{"hello":"world"}`), "openai", cfg)
	if err != nil {
		t.Fatalf("doCaptureRequest() error = %v", err)
	}
	defer resp.Body.Close()

	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if got := string(body); got != `{"attempt":3}` {
		t.Fatalf("body = %q, want last retryable response body", got)
	}
}

func TestExtractGoogleRetryDelay(t *testing.T) {
	body := []byte(`{
  "error": {
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.Help"
      },
      {
        "@type": "type.googleapis.com/google.rpc.RetryInfo",
        "retryDelay": "22.5s"
      }
    ]
  }
}`)

	if got := extractGoogleRetryDelay(body); got != 22500*time.Millisecond {
		t.Fatalf("extractGoogleRetryDelay() = %v, want 22.5s", got)
	}
}
