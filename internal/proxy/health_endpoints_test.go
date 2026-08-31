package proxy

import (
	"encoding/json"
	"lmtools/internal/constants"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthEndpointsRespondOK pins the response contract of the two
// credential-free health endpoints; TestAllEndpointsLogging covers their log
// lines. Both are probed with HEAD as well as GET, because real clients use
// both.
func TestHealthEndpointsRespondOK(t *testing.T) {
	SetupTestLogger(t)
	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderAnthropic,
		MaxRequestBodySize: fixtureMaxBodySize,
	})
	t.Cleanup(cleanup)

	tests := []struct {
		path string
		want map[string]string
	}{
		{path: "/", want: map[string]string{"status": "ok", "name": "lmtools-proxy"}},
		// The liveness body mirrors upstream's `{"message": "hello"}` rather than
		// the proxy's own `/` identity payload; see handleHello for why.
		{path: "/api/hello", want: map[string]string{"message": "hello"}},
	}

	for _, test := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+" "+test.path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(method, test.path, nil))

				// Result() snapshots the header map, so a Content-Type set after
				// WriteHeader fails here exactly as it would on the wire.
				result := rec.Result()
				defer result.Body.Close()
				if result.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want %d", result.StatusCode, http.StatusOK)
				}
				if got := result.Header.Get("Content-Type"); got != "application/json" {
					t.Fatalf("Content-Type = %q, want %q", got, "application/json")
				}
				if method == http.MethodHead {
					// net/http drops the body for HEAD but the recorder keeps it,
					// so asserting one here would pin a recorder artifact.
					return
				}

				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("json.Unmarshal(%q) error = %v", rec.Body.String(), err)
				}
				if !maps.Equal(body, test.want) {
					t.Fatalf("body = %v, want %v", body, test.want)
				}
			})
		}
	}
}
