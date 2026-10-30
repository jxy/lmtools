package proxy

import (
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The ALCF gateway has no /models under a cluster's API; -models-url points
// /v1/models at the cluster's list, a bare JSON array, under either provider.
// The list reads only Authorization: Bearer, which the anthropic provider
// sends with -anthropic-bearer, and has no pages.
func TestModelsURLServesTheGatewayList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/resource_server/minerva/models" || r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": "inkling-bf16", "object": "model", "cluster": "minerva"}, {"id": "muse-spark-bf16", "object": "model", "cluster": "minerva"}]`))
	}))
	defer upstream.Close()

	for _, config := range []*Config{
		{Provider: constants.ProviderOpenAI, ProviderKeySet: ProviderKeySet{OpenAIAPIKey: "token"}},
		{Provider: constants.ProviderAnthropic, ProviderKeySet: ProviderKeySet{AnthropicAPIKey: "token"}, AnthropicBearer: true},
	} {
		t.Run(config.Provider, func(t *testing.T) {
			config.ProviderURL = upstream.URL + "/resource_server/minerva/api/v1"
			config.ModelsURL = upstream.URL + "/resource_server/minerva/models"
			config.MaxRequestBodySize = fixtureMaxBodySize
			handler, cleanup := NewTestServer(t, config)
			defer cleanup()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			for _, want := range []string{`"object":"list"`, `"id":"inkling-bf16"`, `"id":"muse-spark-bf16"`} {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("/v1/models lacks %s:\n%s", want, w.Body.String())
				}
			}
		})
	}
}
