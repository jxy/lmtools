package proxy

import (
	"io"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Chat Completions backends return reasoning beside the content, as
// reasoning_content or, from newer vLLM (ALCF's Minerva), reasoning. A
// buffered answer, a stream the proxy decodes to enforce stop sequences, and
// the history a client sends back all keep the field they came in.
func TestChatReasoningFieldsPassThroughTheProxy(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"Two and two."}}]}` + "\n\n" +
				`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"Still four."}}]}` + "\n\n" +
				`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"4"},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"4","reasoning":"Two and two.","reasoning_content":"Still four."},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        upstream.URL + "/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "token"},
		MaxRequestBodySize: fixtureMaxBodySize,
	})
	defer cleanup()
	send := func(body string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	history := `[{"role":"user","content":"2+2?"},{"role":"assistant","content":"4","reasoning":"Two and two."},{"role":"user","content":"Sure?"}]`
	buffered := send(`{"model":"m","messages":` + history + `}`)
	streamed := send(`{"model":"m","stream":true,"stop":["XYZ"],"messages":[{"role":"user","content":"2+2?"}]}`)
	for _, want := range []string{`"reasoning":"Two and two."`, `"reasoning_content":"Still four."`} {
		if !strings.Contains(buffered, want) {
			t.Errorf("buffered answer lacks %s:\n%s", want, buffered)
		}
		if !strings.Contains(streamed, want) {
			t.Errorf("stream lacks %s:\n%s", want, streamed)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(requests[0], `"reasoning":"Two and two."`) {
		t.Errorf("upstream request dropped the history's reasoning:\n%s", requests[0])
	}
}
