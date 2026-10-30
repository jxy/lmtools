package proxy

import (
	"lmtools/internal/auth"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// alcfauth renews a running proxy's token by renaming a new key file over the
// old one. The proxy must send the new key on its next request, on every route.
func TestProxyReadsTheKeyFileAgainWhenItChanges(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "inference.key")
	if err := os.WriteFile(keyPath, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile, err := auth.OpenKeyFile(constants.ProviderOpenAI, keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        upstream.URL + "/v1",
		ProviderKeySet:     keyFile.Key().Set(),
		KeyFile:            keyFile,
		MaxRequestBodySize: fixtureMaxBodySize,
	})
	defer cleanup()

	send := func(path, body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", path, w.Code, w.Body.String())
		}
	}
	chat := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	messages := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`

	send("/v1/chat/completions", chat)
	send("/v1/messages", messages)
	next := filepath.Join(dir, "next.key")
	if err := os.WriteFile(next, []byte("second-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, keyPath); err != nil {
		t.Fatal(err)
	}
	send("/v1/chat/completions", chat)
	send("/v1/messages", messages)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"Bearer first-token", "Bearer first-token", "Bearer second-token", "Bearer second-token"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("upstream saw %q, want %q", seen, want)
	}
}
