package proxy

import (
	"encoding/json"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The ALCF inference gateway serves /messages, reads only Authorization:
// Bearer, and has no /messages/count_tokens.
func TestAnthropicBearerReachesAGatewayWithoutCountTokens(t *testing.T) {
	var mu sync.Mutex
	var authorization []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorization = append(authorization, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("<!doctype html><title>Not Found</title>"))
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderAnthropic,
		ProviderURL:        upstream.URL + "/v1",
		ProviderKeySet:     ProviderKeySet{AnthropicAPIKey: "alcf-token"},
		AnthropicBearer:    true,
		MaxRequestBodySize: fixtureMaxBodySize,
	})
	defer cleanup()

	send := func(path, body string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	send("/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if body := send("/v1/messages", `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`); !strings.Contains(body, "message_stop") {
		t.Fatalf("stream body:\n%s", body)
	}
	var count struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal([]byte(send("/v1/messages/count_tokens", `{"model":"m","messages":[{"role":"user","content":"hello there"}]}`)), &count); err != nil || count.InputTokens <= 0 {
		t.Fatalf("count_tokens = %+v, %v; want the local estimate", count, err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"/v1/messages Bearer alcf-token",
		"/v1/messages Bearer alcf-token",
		"/v1/messages/count_tokens Bearer alcf-token",
	}
	if strings.Join(authorization, ",") != strings.Join(want, ",") {
		t.Fatalf("upstream saw %q, want %q", authorization, want)
	}
}
