package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestCaptureTokenCountCaseWritesArtifacts(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("ANTHROPIC_API_FIXTURE_URL", "https://api.anthropic.com/v1/messages")

	root := t.TempDir()
	caseID := "anthropic-count-tokens"
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", caseID)
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "request.json"), []byte(`{"model":"placeholder","messages":[{"role":"user","content":"count me"}]}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(request.json) error = %v", err)
	}

	client := &http.Client{Transport: captureRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if r.Method != http.MethodPost {
			return captureJSONResponse(http.StatusBadRequest, `{"error":"bad method"}`), nil
		}
		if r.URL.Path != "/v1/messages/count_tokens" {
			return captureJSONResponse(http.StatusBadRequest, `{"error":"bad path"}`), nil
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-key" {
			return captureJSONResponse(http.StatusBadRequest, `{"error":"bad key"}`), nil
		}
		if !bytes.Contains(body, []byte(`"model":"claude-live"`)) || !bytes.Contains(body, []byte("count me")) {
			return captureJSONResponse(http.StatusBadRequest, `{"error":"bad body"}`), nil
		}
		return captureJSONResponse(http.StatusOK, `{"input_tokens":12}`), nil
	})}

	meta := apifixtures.CaseMeta{
		ID:       caseID,
		Kinds:    []string{"token-count"},
		Provider: "anthropic",
		Models:   map[string]string{"anthropic": "claude-live"},
	}
	target := targetConfig{ID: "anthropic", Provider: "anthropic", Host: "anthropic"}
	if err := captureTokenCountCaseWithClient(root, meta, target, client); err != nil {
		t.Fatalf("captureTokenCountCaseWithClient() error = %v", err)
	}

	requestBytes, err := os.ReadFile(filepath.Join(caseDir, "captures", "anthropic.request.json"))
	if err != nil {
		t.Fatalf("ReadFile(request capture) error = %v", err)
	}
	if !bytes.Contains(requestBytes, []byte(`"path": "/v1/messages/count_tokens"`)) {
		t.Fatalf("request capture = %s, want count_tokens path", requestBytes)
	}
	responseBytes, err := os.ReadFile(filepath.Join(caseDir, "captures", "anthropic.response.json"))
	if err != nil {
		t.Fatalf("ReadFile(response capture) error = %v", err)
	}
	if !bytes.Contains(responseBytes, []byte(`"input_tokens": 12`)) {
		t.Fatalf("response capture = %s, want input_tokens", responseBytes)
	}
}

func TestCaptureStatefulCaseWritesStepArtifacts(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("OPENAI_RESPONSES_API_FIXTURE_URL", "https://example.test/v1/responses")

	root := t.TempDir()
	caseID := "stateful-case"
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", caseID)
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "scenario.json"), []byte(`{
  "provider": "anthropic",
  "model": "claude-test",
  "steps": [
    {
      "id": "create-response",
      "method": "POST",
      "path": "/v1/responses",
      "body": {"model":"claude-test","input":"hi"},
      "expect": {"status":200},
      "bind": {"response_id":"id"}
    },
    {
      "id": "retrieve-response",
      "method": "GET",
      "path": "/v1/responses/${response_id}",
      "expect": {"status":200}
    }
  ]
}
`), 0o644); err != nil {
		t.Fatalf("WriteFile(scenario.json) error = %v", err)
	}

	requests := make([]captureTestRequest, 0, 2)
	client := &http.Client{Transport: captureRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		requests = append(requests, captureTestRequest{method: r.Method, path: r.URL.Path, body: string(body)})
		switch len(requests) {
		case 1:
			return captureJSONResponse(http.StatusOK, `{"id":"resp_live","object":"response","status":"completed","model":"gpt-live","output":[]}`), nil
		case 2:
			return captureJSONResponse(http.StatusOK, `{"id":"resp_live","object":"response","status":"completed","model":"gpt-live","output":[]}`), nil
		default:
			return captureJSONResponse(http.StatusBadRequest, `{"error":"unexpected"}`), nil
		}
	})}

	meta := apifixtures.CaseMeta{
		ID:       caseID,
		Provider: "openai-responses",
		Models:   map[string]string{"openai-responses": "gpt-live"},
	}
	if err := captureStatefulCaseWithClient(root, meta, targetConfig{ID: "openai-responses", Provider: "openai-responses", Host: "openai"}, client); err != nil {
		t.Fatalf("captureStatefulCaseWithClient() error = %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].method != http.MethodPost || requests[0].path != "/v1/responses" {
		t.Fatalf("first request = %+v", requests[0])
	}
	if !strings.Contains(requests[0].body, `"model":"gpt-live"`) {
		t.Fatalf("first request body = %s, want capture model rewrite", requests[0].body)
	}
	if requests[1].method != http.MethodGet || requests[1].path != "/v1/responses/resp_live" {
		t.Fatalf("second request = %+v, want bound response id", requests[1])
	}

	for _, rel := range []string{
		"captures/openai-responses.stateful.json",
		"captures/openai-responses/001-create-response.request.json",
		"captures/openai-responses/001-create-response.response.json",
		"captures/openai-responses/001-create-response.meta.json",
		"captures/openai-responses/002-retrieve-response.request.json",
		"captures/openai-responses/002-retrieve-response.response.json",
		"captures/openai-responses/002-retrieve-response.meta.json",
	} {
		if _, err := os.Stat(filepath.Join(caseDir, rel)); err != nil {
			t.Fatalf("expected capture artifact %s: %v", rel, err)
		}
	}
}

func TestStatefulCaptureURLMapsResponsesAndConversations(t *testing.T) {
	base := "https://api.openai.com/v1/responses"
	tests := []struct {
		path string
		want string
	}{
		{path: "/v1/responses", want: "https://api.openai.com/v1/responses"},
		{path: "/v1/responses/resp_123/input_items", want: "https://api.openai.com/v1/responses/resp_123/input_items"},
		{path: "/v1/conversations", want: "https://api.openai.com/v1/conversations"},
		{path: "/v1/conversations/conv_123/items", want: "https://api.openai.com/v1/conversations/conv_123/items"},
	}
	for _, tt := range tests {
		got, err := statefulCaptureURL(base, tt.path)
		if err != nil {
			t.Fatalf("statefulCaptureURL(%q) error = %v", tt.path, err)
		}
		if got != tt.want {
			t.Fatalf("statefulCaptureURL(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestStatefulCaptureOverridesBindAndPoll(t *testing.T) {
	step := apifixtures.StatefulStep{
		Bind: map[string]string{
			"response_id":  "id",
			"tool_call_id": "output.1.call_id",
		},
		CaptureBind: map[string]string{
			"tool_call_id": "output.0.call_id",
		},
		PollUntil: map[string]interface{}{
			"status":      "completed",
			"output_text": "mock answer",
		},
		CapturePollUntil: map[string]interface{}{
			"status": "completed",
		},
	}

	bindings := statefulCaptureBindings(step)
	if bindings["response_id"] != "id" {
		t.Fatalf("response_id binding = %q, want id", bindings["response_id"])
	}
	if bindings["tool_call_id"] != "output.0.call_id" {
		t.Fatalf("tool_call_id binding = %q, want capture override", bindings["tool_call_id"])
	}

	pollUntil := statefulCapturePollUntil(step)
	if len(pollUntil) != 1 || pollUntil["status"] != "completed" {
		t.Fatalf("capture poll_until = %#v, want status-only override", pollUntil)
	}
}

type captureTestRequest struct {
	method string
	path   string
	body   string
}

type captureRoundTripFunc func(*http.Request) (*http.Response, error)

func (f captureRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func captureJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
}

func TestNormalizeStreamCapture(t *testing.T) {
	input := []byte("data: one\r\n\r\ndata: two\r\n\r\n\r\n")
	got := string(normalizeStreamCapture(input))
	want := "data: one\n\ndata: two\n"
	if got != want {
		t.Fatalf("normalizeStreamCapture() = %q, want %q", got, want)
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

func TestLoadTokenCountRequestBodyAddsGoogleGenerateContentModel(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "testdata/api-fixtures/cases", "google-count-tokens")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(caseDir, "request.json"), []byte(`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(request.json) error = %v", err)
	}

	body, err := loadTokenCountRequestBody(root, apifixtures.CaseMeta{
		ID:     "google-count-tokens",
		Models: map[string]string{"google": "gemini-test"},
	}, targetConfig{ID: "google", Provider: "google", Host: "google"})
	if err != nil {
		t.Fatalf("loadTokenCountRequestBody() error = %v", err)
	}
	if !bytes.Contains(body, []byte(`"model":"models/gemini-test"`)) {
		t.Fatalf("body = %s, want nested generateContentRequest model", body)
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

func TestTokenCountEndpointForTarget(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "test-anthropic-key")
		url, headers, err := tokenCountEndpointForTarget(targetConfig{ID: "anthropic", Provider: "anthropic", Host: "anthropic"}, apifixtures.CaseMeta{
			ID:     "anthropic-count-tokens",
			Models: map[string]string{"anthropic": "claude-haiku-4-5"},
		})
		if err != nil {
			t.Fatalf("tokenCountEndpointForTarget() error = %v", err)
		}
		if got, want := url, "https://api.anthropic.com/v1/messages/count_tokens"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["x-api-key"]; got != "test-anthropic-key" {
			t.Fatalf("x-api-key = %q, want test-anthropic-key", got)
		}
	})

	t.Run("google", func(t *testing.T) {
		t.Setenv("GOOGLE_API_KEY", "test-google-key")
		url, headers, err := tokenCountEndpointForTarget(targetConfig{ID: "google", Provider: "google", Host: "google"}, apifixtures.CaseMeta{
			ID:     "google-count-tokens",
			Models: map[string]string{"google": "gemini-3.1-flash-lite-preview"},
		})
		if err != nil {
			t.Fatalf("tokenCountEndpointForTarget() error = %v", err)
		}
		if got, want := url, "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-flash-lite-preview:countTokens"; got != want {
			t.Fatalf("url = %q, want %q", got, want)
		}
		if got := headers["x-goog-api-key"]; got != "test-google-key" {
			t.Fatalf("x-goog-api-key = %q, want test-google-key", got)
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

func TestDoCaptureModelsRequestFollowsAnthropicPagination(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, want 1000", got)
		}
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = w.Write([]byte(`{
				"data": [{"id": "claude-first", "display_name": "Claude First", "type": "model"}],
				"first_id": "claude-first",
				"has_more": true,
				"last_id": "claude-first"
			}`))
		case "claude-first":
			_, _ = w.Write([]byte(`{
				"data": [{"id": "claude-second", "display_name": "Claude Second", "type": "model"}],
				"first_id": "claude-second",
				"has_more": false,
				"last_id": "claude-second"
			}`))
		default:
			t.Fatalf("unexpected after_id %q", r.URL.Query().Get("after_id"))
		}
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, data, err := doCaptureModelsRequest(context.Background(), server.Client(), req, targetConfig{ID: "anthropic", Provider: "anthropic"})
	if err != nil {
		t.Fatalf("doCaptureModelsRequest() error = %v", err)
	}
	defer resp.Body.Close()

	var got struct {
		Data    []map[string]interface{} `json:"data"`
		HasMore bool                     `json:"has_more"`
		FirstID string                   `json:"first_id"`
		LastID  string                   `json:"last_id"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(got.Data) != 2 || got.Data[0]["id"] != "claude-first" || got.Data[1]["id"] != "claude-second" {
		t.Fatalf("combined data = %#v", got.Data)
	}
	if got.HasMore || got.FirstID != "claude-first" || got.LastID != "claude-second" {
		t.Fatalf("pagination metadata = has_more:%v first:%q last:%q", got.HasMore, got.FirstID, got.LastID)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2: %v", len(requests), requests)
	}
}

func TestDoCaptureModelsRequestFollowsGooglePagination(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if got := r.URL.Query().Get("pageSize"); got != "1000" {
			t.Errorf("pageSize = %q, want 1000", got)
		}
		switch r.URL.Query().Get("pageToken") {
		case "":
			_, _ = w.Write([]byte(`{
				"models": [{"name": "models/gemini-first", "displayName": "Gemini First"}],
				"nextPageToken": "next-page"
			}`))
		case "next-page":
			_, _ = w.Write([]byte(`{
				"models": [{"name": "models/gemini-second", "displayName": "Gemini Second"}]
			}`))
		default:
			t.Fatalf("unexpected pageToken %q", r.URL.Query().Get("pageToken"))
		}
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1beta/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, data, err := doCaptureModelsRequest(context.Background(), server.Client(), req, targetConfig{ID: "google", Provider: "google"})
	if err != nil {
		t.Fatalf("doCaptureModelsRequest() error = %v", err)
	}
	defer resp.Body.Close()

	var got struct {
		Models        []map[string]interface{} `json:"models"`
		NextPageToken string                   `json:"nextPageToken"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(got.Models) != 2 || got.Models[0]["name"] != "models/gemini-first" || got.Models[1]["name"] != "models/gemini-second" {
		t.Fatalf("combined models = %#v", got.Models)
	}
	if got.NextPageToken != "" {
		t.Fatalf("nextPageToken = %q, want empty", got.NextPageToken)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2: %v", len(requests), requests)
	}
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
	resp, body, err := apifixtures.DoCaptureRequest(context.Background(), server.Client(), req, []byte(`{"hello":"world"}`), "openai", cfg)
	if err != nil {
		t.Fatalf("apifixtures.DoCaptureRequest() error = %v", err)
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
	resp, body, err := apifixtures.DoCaptureRequest(context.Background(), server.Client(), req, []byte(`{"hello":"world"}`), "openai", cfg)
	if err != nil {
		t.Fatalf("apifixtures.DoCaptureRequest() error = %v", err)
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
