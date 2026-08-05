package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var errUnexpectedUpstreamCall = errors.New("unexpected upstream call")

type capturedUpstreamRequest struct {
	Method string
	Path   string
	Auth   string
	Body   string
}

// upstreamLog records the requests the test server sent upstream, in order.
type upstreamLog struct {
	reqs []capturedUpstreamRequest
}

func (l *upstreamLog) last() capturedUpstreamRequest {
	return l.reqs[len(l.reqs)-1]
}

func newArgoResponsesTestServer(t *testing.T, respond func(*http.Request) (*http.Response, error), modelMapSpecs ...string) (*Server, *upstreamLog) {
	t.Helper()
	upstreamReqs := &upstreamLog{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.Body != nil {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			body = string(raw)
		}
		upstreamReqs.reqs = append(upstreamReqs.reqs, capturedUpstreamRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Body:   body,
		})
		return respond(r)
	})

	rules := make([]ModelMapRule, 0, len(modelMapSpecs))
	for _, spec := range modelMapSpecs {
		rule, err := ParseModelMapSpec(spec)
		if err != nil {
			t.Fatalf("ParseModelMapSpec(%q) error = %v", spec, err)
		}
		rules = append(rules, rule)
	}

	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		OpenAIResponses:    true,
		ModelMapRules:      rules,
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client), upstreamReqs
}

func argoResponsesJSON() *http.Response {
	return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
		"id":                 "resp_argo",
		"object":             "response",
		"status":             "completed",
		"model":              "gpt5",
		"output":             []interface{}{},
		"vendor_only_output": "kept",
	})
}

func TestArgoResponsesPassThroughPreservesRawRequestBody(t *testing.T) {
	server, upstreamReqs := newArgoResponsesTestServer(t, func(*http.Request) (*http.Response, error) {
		return argoResponsesJSON(), nil
	})

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model":            "gpt5",
		"input":            "hi",
		"reasoning":        map[string]interface{}{"effort": "low", "mode": "standard"},
		"prompt_cache_key": "cache-1",
	})

	if resp["id"] != "resp_argo" {
		t.Fatalf("response id = %#v, want resp_argo", resp["id"])
	}
	if resp["vendor_only_output"] != "kept" {
		t.Fatalf("response dropped upstream-only field: %#v", resp)
	}
	if len(upstreamReqs.reqs) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(upstreamReqs.reqs))
	}
	got := upstreamReqs.reqs[0]
	if got.Path != "/argoapi/v1/responses" {
		t.Fatalf("upstream path = %q, want /argoapi/v1/responses", got.Path)
	}
	if got.Auth != "Bearer argo-user" {
		t.Fatalf("upstream Authorization = %q, want Bearer argo-user", got.Auth)
	}

	var upstream map[string]interface{}
	if err := json.Unmarshal([]byte(got.Body), &upstream); err != nil {
		t.Fatalf("json.Unmarshal(upstream body) error = %v, body = %s", err, got.Body)
	}
	// These are exactly the fields the converted Argo path warns about and drops.
	reasoning, ok := upstream["reasoning"].(map[string]interface{})
	if !ok || reasoning["mode"] != "standard" {
		t.Fatalf("upstream reasoning = %#v, want mode standard preserved", upstream["reasoning"])
	}
	if upstream["prompt_cache_key"] != "cache-1" {
		t.Fatalf("upstream prompt_cache_key = %#v, want cache-1", upstream["prompt_cache_key"])
	}
	if _, ok := upstream["messages"]; ok {
		t.Fatalf("upstream request was converted to chat completions: %s", got.Body)
	}
}

func TestArgoResponsesPassThroughAppliesModelMap(t *testing.T) {
	server, upstreamReqs := newArgoResponsesTestServer(t, func(*http.Request) (*http.Response, error) {
		return argoResponsesJSON(), nil
	}, "^gpt-5$=gpt5")

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "gpt-5",
		"input": "hi",
	})

	var upstream map[string]interface{}
	if err := json.Unmarshal([]byte(upstreamReqs.reqs[0].Body), &upstream); err != nil {
		t.Fatalf("json.Unmarshal(upstream body) error = %v", err)
	}
	if upstream["model"] != "gpt5" {
		t.Fatalf("upstream model = %#v, want mapped gpt5", upstream["model"])
	}
	if resp["model"] != "gpt-5" {
		t.Fatalf("client-visible model = %#v, want original gpt-5", resp["model"])
	}
}

func TestArgoResponsesPassThroughStreams(t *testing.T) {
	upstreamBody := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_argo_stream","model":"gpt5"}}`,
		"",
		": keepalive",
		"",
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	server, upstreamReqs := newArgoResponsesTestServer(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		}, nil
	})

	recorder := newFlushableRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt5","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, req)

	if got := upstreamReqs.reqs[0].Path; got != "/argoapi/v1/responses" {
		t.Fatalf("upstream path = %q, want /argoapi/v1/responses", got)
	}
	body := recorder.Body.String()
	for _, want := range []string{"event: response.created\n", ": keepalive\n", `"delta":"hi"`, "data: [DONE]\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q: %q", want, body)
		}
	}
}

func TestArgoResponsesConvertsNonGPTModels(t *testing.T) {
	server, upstreamReqs := newArgoResponsesTestServer(t, func(*http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
			ID:      "msg_1",
			Type:    "message",
			Role:    "assistant",
			Model:   "claude-opus-4-1-20250805",
			Content: []AnthropicContentBlock{{Type: "text", Text: "hello"}},
		}), nil
	})

	resp := requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "claude-opus-4-1-20250805",
		"input": "hi",
	})

	if got := upstreamReqs.reqs[0].Path; got != "/argoapi/v1/messages" {
		t.Fatalf("upstream path = %q, want converted Argo Anthropic route", got)
	}
	if resp["object"] != "response" {
		t.Fatalf("converted response object = %#v, want response", resp["object"])
	}
}

// TestArgoResponsesLifecycleResolvesLocalStateFirst covers the mixed case: a
// converted response is stored locally and must be served from there, while an
// unknown id belongs to a passed-through response and is fetched from Argo.
func TestArgoResponsesLifecycleResolvesLocalStateFirst(t *testing.T) {
	server, upstreamReqs := newArgoResponsesTestServer(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/v1/messages") {
			return jsonRoundTripResponse(http.StatusOK, AnthropicResponse{
				ID:      "msg_1",
				Type:    "message",
				Role:    "assistant",
				Model:   "claude-opus-4-1-20250805",
				Content: []AnthropicContentBlock{{Type: "text", Text: "hello"}},
			}), nil
		}
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
			"id":     "resp_argo",
			"object": "response",
			"status": "completed",
			"model":  "gpt5",
		}), nil
	})

	created := requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "claude-opus-4-1-20250805",
		"input": "hi",
	})
	localID, _ := created["id"].(string)
	if localID == "" {
		t.Fatalf("converted response has no id: %#v", created)
	}
	upstreamCallsAfterCreate := len(upstreamReqs.reqs)

	retrieved := requestJSON(t, server, http.MethodGet, "/v1/responses/"+localID, nil)
	if retrieved["id"] != localID {
		t.Fatalf("retrieved id = %#v, want %q", retrieved["id"], localID)
	}
	if len(upstreamReqs.reqs) != upstreamCallsAfterCreate {
		t.Fatalf("locally stored response was fetched upstream: %#v", upstreamReqs.reqs[upstreamCallsAfterCreate:])
	}

	passthrough := requestJSON(t, server, http.MethodGet, "/v1/responses/resp_argo", nil)
	if passthrough["id"] != "resp_argo" {
		t.Fatalf("passthrough retrieve id = %#v, want resp_argo", passthrough["id"])
	}
	last := upstreamReqs.last()
	if last.Method != http.MethodGet || last.Path != "/argoapi/v1/responses/resp_argo" {
		t.Fatalf("upstream lifecycle call = %s %s, want GET /argoapi/v1/responses/resp_argo", last.Method, last.Path)
	}
}

func TestArgoConversationsForwardWhenPassthroughEnabled(t *testing.T) {
	server, upstreamReqs := newArgoResponsesTestServer(t, func(*http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
			"id":     "conv_argo",
			"object": "conversation",
		}), nil
	})

	resp := requestJSON(t, server, http.MethodPost, "/v1/conversations", map[string]interface{}{})
	if resp["id"] != "conv_argo" {
		t.Fatalf("conversation id = %#v, want conv_argo", resp["id"])
	}
	if got := upstreamReqs.reqs[0].Path; got != "/argoapi/v1/conversations" {
		t.Fatalf("upstream path = %q, want /argoapi/v1/conversations", got)
	}
}

// TestArgoConversationsResolveLocalStateFirst is the conversations counterpart of
// the lifecycle mixed case: an id this proxy stores locally is served from local
// state, and only unknown ids reach Argo.
func TestArgoConversationsResolveLocalStateFirst(t *testing.T) {
	server, upstreamReqs := newArgoResponsesTestServer(t, func(*http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
			"id":     "conv_argo",
			"object": "conversation",
		}), nil
	})

	local, _, err := server.responsesState.createConversation(nil, "")
	if err != nil {
		t.Fatalf("createConversation() error = %v", err)
	}

	retrieved := requestJSON(t, server, http.MethodGet, "/v1/conversations/"+local.ID, nil)
	if retrieved["id"] != local.ID {
		t.Fatalf("retrieved id = %#v, want %q", retrieved["id"], local.ID)
	}
	if len(upstreamReqs.reqs) != 0 {
		t.Fatalf("locally stored conversation was fetched upstream: %#v", upstreamReqs.reqs)
	}

	passthrough := requestJSON(t, server, http.MethodGet, "/v1/conversations/conv_argo", nil)
	if passthrough["id"] != "conv_argo" {
		t.Fatalf("passthrough retrieve id = %#v, want conv_argo", passthrough["id"])
	}
	if got := upstreamReqs.last().Path; got != "/argoapi/v1/conversations/conv_argo" {
		t.Fatalf("upstream path = %q, want /argoapi/v1/conversations/conv_argo", got)
	}
}

func TestArgoResponsesConvertsWithoutFlag(t *testing.T) {
	var sawPath string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawPath = r.URL.Path
		return jsonRoundTripResponse(http.StatusOK, OpenAIResponse{
			ID:      "chatcmpl_1",
			Object:  "chat.completion",
			Model:   "gpt5",
			Choices: []OpenAIChoice{{Index: 0, Message: OpenAIMessage{Role: "assistant", Content: "hello"}}},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	server := NewTestServerDirectWithClient(t, config, client)

	_ = requestJSON(t, server, http.MethodPost, "/v1/responses", map[string]interface{}{
		"model": "gpt5",
		"input": "hi",
	})

	if sawPath != "/argoapi/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want converted chat/completions route without -openai-responses", sawPath)
	}
}

func TestResponsesPassthroughTargetSelection(t *testing.T) {
	tests := []struct {
		name            string
		config          *Config
		wantOK          bool
		wantURL         string
		wantGPTDirect   bool
		wantOtherDirect bool
	}{
		{
			name:            "openai always direct",
			config:          &Config{Provider: constants.ProviderOpenAI, ProviderURL: "http://openai.local/v1", ProviderKeySet: ProviderKeySet{OpenAIAPIKey: "k"}},
			wantOK:          true,
			wantURL:         "http://openai.local/v1/responses",
			wantGPTDirect:   true,
			wantOtherDirect: true,
		},
		{
			name:            "argo with flag is gpt only",
			config:          &Config{Provider: constants.ProviderArgo, ProviderURL: "http://argo.local/argoapi", ArgoUser: "u", OpenAIResponses: true},
			wantOK:          true,
			wantURL:         "http://argo.local/argoapi/v1/responses",
			wantGPTDirect:   true,
			wantOtherDirect: false,
		},
		{
			name:   "argo without flag",
			config: &Config{Provider: constants.ProviderArgo, ProviderURL: "http://argo.local/argoapi", ArgoUser: "u"},
		},
		{
			name:   "argo legacy ignores flag",
			config: &Config{Provider: constants.ProviderArgo, ProviderURL: "http://argo.local/argoapi", ArgoUser: "u", OpenAIResponses: true, ArgoLegacy: true},
		},
		{
			name:   "anthropic",
			config: &Config{Provider: constants.ProviderAnthropic, ProviderURL: "http://anthropic.local/v1", ProviderKeySet: ProviderKeySet{AnthropicAPIKey: "k"}, OpenAIResponses: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.MaxRequestBodySize = fixtureMaxBodySize
			tt.config.SessionsDir = t.TempDir()
			client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errUnexpectedUpstreamCall
			}))
			server := NewTestServerDirectWithClient(t, tt.config, client)

			target, ok := server.responsesPassthroughTarget()
			if ok != tt.wantOK {
				t.Fatalf("responsesPassthroughTarget() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && target.URL != tt.wantURL {
				t.Fatalf("target URL = %q, want %q", target.URL, tt.wantURL)
			}
			if got := server.responsesPassthroughEnabled(); got != tt.wantOK {
				t.Fatalf("responsesPassthroughEnabled() = %v, want %v", got, tt.wantOK)
			}
			if got := server.useDirectResponsesForModel("gpt5"); got != tt.wantGPTDirect {
				t.Fatalf("useDirectResponsesForModel(gpt5) = %v, want %v", got, tt.wantGPTDirect)
			}
			if got := server.useDirectResponsesForModel("claude-opus-4-1-20250805"); got != tt.wantOtherDirect {
				t.Fatalf("useDirectResponsesForModel(claude) = %v, want %v", got, tt.wantOtherDirect)
			}
		})
	}
}
