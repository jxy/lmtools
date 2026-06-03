package proxy

import (
	"context"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDoJSONAcceptsNoContent(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	server := NewTestServerDirectWithClient(t, &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}, retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport))

	var out map[string]interface{}
	if err := server.doJSON(context.Background(), "http://openai.local/v1/test", map[string]string{"ok": "true"}, nil, &out, constants.ProviderOpenAI, "OpenAI"); err != nil {
		t.Fatalf("doJSON() error = %v", err)
	}
}
