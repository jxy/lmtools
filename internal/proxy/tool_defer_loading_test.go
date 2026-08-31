package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	deferLoadingDropWarning     = `Dropping Anthropic field "tools[].defer_loading" while converting to `
	deferLoadingEstimateWarning = `Ignoring Anthropic field "tools[].defer_loading" in the local token estimate`
)

// TestAnthropicToolDeferLoadingSurvivesAnthropicWire covers both consequences of
// declaring the field: the decoder stops reporting it as unknown, and the parsed
// struct — which Anthropic-wire routes marshal straight to the upstream — renders
// it back unchanged.
func TestAnthropicToolDeferLoadingSurvivesAnthropicWire(t *testing.T) {
	// A deferred tool is incoherent without the search tool that surfaces it, so
	// the fixture carries the pair a real client sends.
	const body = `{
		"model": "claude-opus-5",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{"name": "Read", "description": "read a file", "input_schema": {"type": "object"}},
			{"name": "Grep", "description": "search files", "defer_loading": true, "input_schema": {"type": "object"}},
			{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"}
		]
	}`

	server := NewMinimalTestServer(t, &Config{})
	var req AnthropicRequest
	logs := captureWarnLogs(t, func() {
		httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		if err := server.decodeEndpointRequest(httpReq, &req); err != nil {
			t.Fatalf("decodeEndpointRequest() error = %v", err)
		}
	})
	if strings.Contains(logs, "defer_loading") {
		t.Fatalf("defer_loading still reported as an unknown field:\n%s", logs)
	}

	rendered, err := json.Marshal(req.Tools)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var wire []map[string]interface{}
	if err := json.Unmarshal(rendered, &wire); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", rendered, err)
	}
	if len(wire) != 3 {
		t.Fatalf("len(tools) = %d, want 3: %s", len(wire), rendered)
	}
	if _, ok := wire[0]["defer_loading"]; ok {
		t.Fatalf("tools[0] gained defer_loading it never carried: %s", rendered)
	}
	if got, ok := wire[1]["defer_loading"].(bool); !ok || !got {
		t.Fatalf("tools[1].defer_loading = %v, want true: %s", wire[1]["defer_loading"], rendered)
	}
}

// Converted routes rebuild the tool array and cannot carry the flag, so it is
// warned as dropped alongside the sibling tools[].cache_control warning.
func TestAnthropicToolDeferLoadingWarnsOnConvertedPaths(t *testing.T) {
	// Two deferred tools, so the "exactly once" assertion distinguishes a
	// per-request warning from a per-tool one. Neither warn function mutates the
	// request, so one instance serves both subtests.
	deferred := true
	req := &AnthropicRequest{
		Model:     "gpt-5",
		MaxTokens: 100,
		Messages:  []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"hi"`)}},
		Tools: []AnthropicTool{
			{Name: "GrepOne", InputSchema: map[string]interface{}{"type": "object"}, DeferLoading: &deferred},
			{Name: "GrepTwo", InputSchema: map[string]interface{}{"type": "object"}, DeferLoading: &deferred},
		},
	}

	tests := []struct {
		target string
		warn   func(context.Context, *AnthropicRequest)
	}{
		{target: "OpenAI", warn: warnAnthropicRequestDropsForOpenAI},
		{target: "Google", warn: warnAnthropicRequestDropsForGoogle},
	}

	for _, test := range tests {
		t.Run(test.target, func(t *testing.T) {
			logs := captureWarnLogs(t, func() {
				test.warn(context.Background(), req)
			})
			want := deferLoadingDropWarning + test.target
			if got := strings.Count(logs, want); got != 1 {
				t.Fatalf("warning %q occurred %d times, want exactly once:\n%s", want, got, logs)
			}
		})
	}
}

// An explicit false is the API default, so a converted route loses nothing and
// must stay quiet. Warning here would also make the estimate's "counted as if
// loaded" wording a false statement about a request that deferred nothing.
func TestAnthropicToolDeferLoadingFalseIsNotReportedAsLost(t *testing.T) {
	notDeferred := false
	req := &AnthropicRequest{
		Model:     "gpt-5",
		MaxTokens: 100,
		Messages:  []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"hi"`)}},
		Tools: []AnthropicTool{
			{Name: "Grep", InputSchema: map[string]interface{}{"type": "object"}, DeferLoading: &notDeferred},
		},
	}

	logs := captureWarnLogs(t, func() {
		warnAnthropicRequestDropsForOpenAI(context.Background(), req)
	})
	if strings.Contains(logs, "defer_loading") {
		t.Fatalf("defer_loading:false reported as a loss:\n%s", logs)
	}
}

func TestHandleCountTokensWarnsOnceWhenDeferLoadingIsDropped(t *testing.T) {
	tests := []struct {
		name              string
		provider          string
		model             string
		wantWarning       string
		wantUpstreamCalls int
	}{
		{
			name:              "Google conversion",
			provider:          constants.ProviderGoogle,
			model:             "gemini-count",
			wantWarning:       deferLoadingDropWarning + "Google",
			wantUpstreamCalls: 1,
		},
		{
			name:              "OpenAI local estimation",
			provider:          constants.ProviderOpenAI,
			model:             "gpt-count",
			wantWarning:       deferLoadingEstimateWarning,
			wantUpstreamCalls: 0,
		},
		{
			name:              "Argo OpenAI local estimation",
			provider:          constants.ProviderArgo,
			model:             "gpt-count",
			wantWarning:       deferLoadingEstimateWarning,
			wantUpstreamCalls: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstreamCalls := 0
			server := newDeferLoadingCountServer(t, test.provider, func(r *http.Request) (*http.Response, error) {
				upstreamCalls++
				return jsonRoundTripResponse(http.StatusOK, GoogleCountTokensResponse{TotalTokens: 37}), nil
			})

			logs := captureWarnLogs(t, func() {
				resp := postCountTokens(t, server, countTokensBodyWithDeferredTools(test.model))
				if resp.InputTokens <= 0 {
					t.Fatalf("input_tokens = %d, want > 0", resp.InputTokens)
				}
			})

			if got := strings.Count(logs, test.wantWarning); got != 1 {
				t.Fatalf("warning %q occurred %d times, want exactly once:\n%s", test.wantWarning, got, logs)
			}
			if upstreamCalls != test.wantUpstreamCalls {
				t.Fatalf("upstream calls = %d, want %d", upstreamCalls, test.wantUpstreamCalls)
			}
		})
	}
}

// The Google count_tokens route renders to Gemini without going through
// ConvertAnthropicToGoogle, so it has to ask for the drop warnings itself. Pin a
// sibling field too: if the route ever goes back to naming one field by hand,
// tools[].cache_control silently stops being reported here.
func TestHandleCountTokensGoogleWarnsEveryDroppedToolField(t *testing.T) {
	server := newDeferLoadingCountServer(t, constants.ProviderGoogle, func(r *http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, GoogleCountTokensResponse{TotalTokens: 37}), nil
	})

	logs := captureWarnLogs(t, func() {
		postCountTokens(t, server, `{
		  "model": "gemini-count",
		  "messages": [{"role": "user", "content": "count"}],
		  "tools": [{
		    "name": "lookup",
		    "defer_loading": true,
		    "cache_control": {"type": "ephemeral"},
		    "input_schema": {"type": "object"}
		  }]
		}`)
	})

	for _, want := range []string{
		deferLoadingDropWarning + "Google",
		`Dropping Anthropic field "tools[].cache_control" while converting to Google`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing warning %q:\n%s", want, logs)
		}
	}
}

func TestHandleCountTokensPreservesDeferLoadingOnNativeRoutes(t *testing.T) {
	for _, provider := range []string{constants.ProviderAnthropic, constants.ProviderArgo} {
		t.Run(provider, func(t *testing.T) {
			var upstreamReq AnthropicTokenCountRequest
			server := newDeferLoadingCountServer(t, provider, func(r *http.Request) (*http.Response, error) {
				if err := json.NewDecoder(r.Body).Decode(&upstreamReq); err != nil {
					return nil, err
				}
				return jsonRoundTripResponse(http.StatusOK, AnthropicTokenCountResponse{InputTokens: 41}), nil
			})

			logs := captureWarnLogs(t, func() {
				resp := postCountTokens(t, server, countTokensBodyWithDeferredTools("claude-count"))
				if resp.InputTokens != 41 {
					t.Fatalf("input_tokens = %d, want 41", resp.InputTokens)
				}
			})

			if strings.Contains(logs, "defer_loading") {
				t.Fatalf("native count route reported defer_loading as lost:\n%s", logs)
			}
			if len(upstreamReq.Tools) != 2 {
				t.Fatalf("len(upstream tools) = %d, want 2: %#v", len(upstreamReq.Tools), upstreamReq.Tools)
			}
			for i, tool := range upstreamReq.Tools {
				if tool.DeferLoading == nil || !*tool.DeferLoading {
					t.Fatalf("upstream tools[%d].defer_loading = %v, want true", i, tool.DeferLoading)
				}
			}
		})
	}
}

func countTokensBodyWithDeferredTools(model string) string {
	return `{
	  "model": "` + model + `",
	  "messages": [{"role": "user", "content": "count deferred tools"}],
	  "tools": [
	    {"name": "first", "defer_loading": true, "input_schema": {"type": "object"}},
	    {"name": "second", "defer_loading": true, "input_schema": {"type": "object"}}
	  ]
	}`
}

// newDeferLoadingCountServer carries keys for every provider these tests reach,
// so each case names only the provider it is exercising.
func newDeferLoadingCountServer(t *testing.T, provider string, roundTrip roundTripFunc) *Server {
	t.Helper()
	providerURL := "http://provider.local/v1"
	if provider == constants.ProviderGoogle {
		providerURL = "http://provider.local/v1beta/models"
	}
	config := &Config{
		Provider:    provider,
		ProviderURL: providerURL,
		ProviderKeySet: ProviderKeySet{
			AnthropicAPIKey: "anthropic-key",
			OpenAIAPIKey:    fixtureOpenAIKey,
			GoogleAPIKey:    "google-key",
			ArgoAPIKey:      "argo-key",
		},
		ArgoUser:           "fixture-user",
		MaxRequestBodySize: fixtureMaxBodySize,
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, roundTrip)
	return NewTestServerDirectWithClient(t, config, client)
}
