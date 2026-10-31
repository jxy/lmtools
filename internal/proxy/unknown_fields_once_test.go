package proxy

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// unknownFieldLines returns the log records at level that name unknown
// fields, from the message on.
func unknownFieldLines(logs, level string) []string {
	var lines []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.HasPrefix(line, "["+level+"]") && strings.Contains(line, "Unknown JSON fields") {
			lines = append(lines, line[strings.Index(line, "Unknown JSON fields"):])
		}
	}
	return lines
}

// A field a backend adds to every response is a warning the first time it
// comes, and DEBUG after that. A new field, or the same field from another
// source, is a warning of its own.
func TestUnknownFieldsWarnOncePerField(t *testing.T) {
	ctx := withUnknownFieldWarnings(context.Background(), newUnknownFieldWarnings())
	logs := captureStderrWithLevel(t, "DEBUG", func() {
		warnUnknownFields(ctx, []byte(`{"id":"x","google":{}}`), OpenAIResponse{}, "Argo OpenAI response")
		warnUnknownFields(ctx, []byte(`{"id":"x","google":{}}`), OpenAIResponse{}, "Argo OpenAI response")
		warnUnknownFields(ctx, []byte(`{"id":"x","google":{},"extra":1}`), OpenAIResponse{}, "Argo OpenAI response")
		warnUnknownFields(ctx, []byte(`{"id":"x","google":{}}`), OpenAIResponse{}, "OpenAI response")
	})
	wantWarnings := []string{
		"Unknown JSON fields in Argo OpenAI response (ignored): google",
		"Unknown JSON fields in Argo OpenAI response (ignored): extra",
		"Unknown JSON fields in OpenAI response (ignored): google",
	}
	if got := unknownFieldLines(logs, "WARN"); strings.Join(got, "\n") != strings.Join(wantWarnings, "\n") {
		t.Fatalf("warnings = %q, want %q", got, wantWarnings)
	}
	wantDebug := []string{"Unknown JSON fields in Argo OpenAI response (ignored): google"}
	if got := unknownFieldLines(logs, "DEBUG"); strings.Join(got, "\n") != strings.Join(wantDebug, "\n") {
		t.Fatalf("debug records = %q, want %q", got, wantDebug)
	}
}

// The server keeps that record for its lifetime: two replies that carry the
// fields Argo's Gemini route always adds give one warning between them.
func TestServerWarnsOnceAboutFieldsItsBackendAlwaysAdds(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gemini4argon","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop","google":{"finish_reason":"STOP"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"google":{"traffic_type":"ON_DEMAND"}}}`)
	}))
	defer backend.Close()
	handler, cleanup := NewTestServer(t, &Config{Provider: constants.ProviderArgo, ProviderURL: backend.URL + "/v1", ArgoUser: "testuser"})
	defer cleanup()

	logs := captureStderrWithLevel(t, "WARN", func() {
		for i := 0; i < 2; i++ {
			code, body := requestJSONStatus(t, handler, http.MethodPost, "/v1/messages", map[string]interface{}{
				"model":      "gemini4argon",
				"max_tokens": 10,
				"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
			})
			if code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", code, body)
			}
		}
	})
	if got := unknownFieldLines(logs, "WARN"); len(got) != 1 || !strings.Contains(got[0], "choices[].google") {
		t.Fatalf("warnings = %q, want one naming choices[].google", got)
	}
}
