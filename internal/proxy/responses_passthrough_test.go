package proxy

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capturedPassthroughRequest struct {
	Body          string
	ContentLength int64
	Accept        string
}

func newResponsesPassthroughTestServer(t *testing.T, respond func(*http.Request) (*http.Response, error), modelMapSpecs ...string) (*Server, *capturedPassthroughRequest) {
	t.Helper()
	captured := &capturedPassthroughRequest{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		captured.ContentLength = r.ContentLength
		captured.Accept = r.Header.Get("Accept")
		if r.Body != nil {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			captured.Body = string(raw)
		}
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
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		ModelMapRules:      rules,
		MaxRequestBodySize: fixtureMaxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client), captured
}

func postRawResponses(t *testing.T, server http.Handler, rawBody string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func responsesSSEResponse(events ...string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(events, ""))),
	}
}

func responsesCompletedEvent(id, model string) string {
	return "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + id + "\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"" + model + "\",\"output\":[]}}\n\n"
}

// Direct passthrough forwards the client's own bytes when no model rewrite is
// needed: odd spacing, key order, and unmodeled fields all survive, which they
// would not if the request were re-encoded from the parsed struct.
func TestResponsesPassthroughForwardsBodyVerbatim(t *testing.T) {
	server, captured := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return responsesSSEResponse(responsesCompletedEvent("resp_upstream", "gpt-test")), nil
	})

	rawBody := "{\"model\" : \"gpt-test\",\n  \"stream\":true,\t\"vendor_only\":{\"keep\":[1,2,3]},\n\"input\":\"say hi\"}"
	status, body := postRawResponses(t, server, rawBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	if captured.Body != rawBody {
		t.Fatalf("upstream body =\n%s\nwant verbatim client body:\n%s", captured.Body, rawBody)
	}
	if captured.ContentLength != int64(len(rawBody)) {
		t.Fatalf("upstream Content-Length = %d, want %d", captured.ContentLength, len(rawBody))
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("client stream = %s, want the upstream terminal event", body)
	}
}

// A -model-map rule rewrites the model in the forwarded body.
func TestResponsesPassthroughRewritesMappedModel(t *testing.T) {
	server, captured := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
			"id": "resp_mapped", "object": "response", "status": "completed", "model": "gpt-mapped",
		}), nil
	}, `^gpt-test$=gpt-mapped`)

	status, body := postRawResponses(t, server, `{"model":"gpt-test","input":"say hi"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	if !strings.Contains(captured.Body, `"model":"gpt-mapped"`) {
		t.Fatalf("upstream body = %s, want the mapped model", captured.Body)
	}
}

// JSON says nothing useful about a name that appears twice, and every decoder
// in the chain keeps the last one. Routing has to agree with them, or a
// -model-map rule could be bypassed by a decoy key.
func TestResponsesPassthroughRoutesOnLastDuplicateModelKey(t *testing.T) {
	server, captured := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
			"id": "resp_dup", "object": "response", "status": "completed", "model": "gpt-mapped",
		}), nil
	}, `^gpt-effective$=gpt-mapped`)

	// Only the last name has a rule, so the mapped model upstream proves which
	// one routing used.
	status, body := postRawResponses(t, server, `{"model":"gpt-unmapped","input":"say hi","model":"gpt-effective"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	if !strings.Contains(captured.Body, `"model":"gpt-mapped"`) {
		t.Fatalf("upstream body = %s, want the rule for the last model applied", captured.Body)
	}
}

// A provider resolves an alias to a dated backend name; the client is owed back
// the name it sent, on every event it sees.
func TestResponsesPassthroughRewritesResponseModel(t *testing.T) {
	const clientModel = "gpt-4o"
	const backendModel = "gpt-4o-2024-11-20"

	t.Run("sse upstream", func(t *testing.T) {
		server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
			return responsesSSEResponse(responsesCompletedEvent("resp_alias", backendModel)), nil
		})
		status, body := postRawResponses(t, server, `{"model":"`+clientModel+`","stream":true,"input":"say hi"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, body)
		}
		if strings.Contains(body, backendModel) {
			t.Fatalf("client stream = %s, want the backend model rewritten to %q", body, clientModel)
		}
		if !strings.Contains(body, `"model":"`+clientModel+`"`) {
			t.Fatalf("client stream = %s, want the requested model", body)
		}
	})

	t.Run("json upstream", func(t *testing.T) {
		server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
			return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
				"id": "resp_alias", "object": "response", "status": "completed", "model": backendModel,
			}), nil
		})
		status, body := postRawResponses(t, server, `{"model":"`+clientModel+`","input":"say hi"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, body)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("json.Unmarshal(%s) error = %v", body, err)
		}
		if decoded["model"] != clientModel {
			t.Fatalf("response model = %#v, want %q", decoded["model"], clientModel)
		}
	})

	// The synthetic terminal event quotes whatever model the relay observed, so
	// it has to observe the rewritten one too.
	t.Run("synthetic failure", func(t *testing.T) {
		server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
			return responsesSSEResponse(
				"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_alias\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"" + backendModel + "\",\"output\":[]}}\n\n",
			), nil
		})
		status, body := postRawResponses(t, server, `{"model":"`+clientModel+`","stream":true,"input":"say hi"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", status, body)
		}
		if !strings.Contains(body, "response.failed") {
			t.Fatalf("client stream = %s, want a synthetic terminal event", body)
		}
		if strings.Contains(body, backendModel) {
			t.Fatalf("client stream = %s, want every event to name %q", body, clientModel)
		}
	})
}

// The proxy must not mask the provider's own limits: an upstream rejection is
// forwarded with its status and body unchanged.
func TestResponsesPassthroughForwardsUpstreamSizeRejection(t *testing.T) {
	upstreamError := `{"error":{"message":"message size exceeded","type":"request_too_large"}}`
	server, _ := newResponsesPassthroughTestServer(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusRequestEntityTooLarge,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamError)),
		}, nil
	})

	status, body := postRawResponses(t, server, `{"model":"gpt-test","stream":true,"input":"say hi"}`)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want the upstream 413; body = %s", status, body)
	}
	if strings.TrimSpace(body) != upstreamError {
		t.Fatalf("body = %s, want the upstream error forwarded unchanged", body)
	}
}

// newResponsesPassthroughSizeLimitedServer is newResponsesPassthroughTestServer
// with a small body limit and no expectation that the upstream is reached.
func newResponsesPassthroughSizeLimitedServer(t *testing.T, maxBodySize int64, upstreamCalled *bool) *Server {
	t.Helper()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*upstreamCalled = true
		if r.Body != nil {
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				return nil, err
			}
		}
		return responsesSSEResponse(responsesCompletedEvent("resp_upstream", "gpt-test")), nil
	})
	config := &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        "http://openai.local/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "test-key"},
		MaxRequestBodySize: maxBodySize,
		SessionsDir:        t.TempDir(),
	}
	client := retry.NewClientWithTransport(10*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(t, config, client)
}

func assertRequestTooLarge(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", status, body)
	}
	if !strings.Contains(body, ErrTypePayloadTooLarge) {
		t.Fatalf("body = %s, want it to mention %s", body, ErrTypePayloadTooLarge)
	}
	if !strings.Contains(body, "-max-request-body-size") {
		t.Fatalf("body = %s, want it to name the flag that raises the limit", body)
	}
}

// A declared length over the limit is refused before the body is read.
func TestResponsesPassthroughRejectsDeclaredOversizeWith413(t *testing.T) {
	const limit = 4096
	upstreamCalled := false
	server := newResponsesPassthroughSizeLimitedServer(t, limit, &upstreamCalled)

	rawBody := `{"model":"gpt-test","input":"` + strings.Repeat("x", limit*2) + `"}`
	status, body := postRawResponses(t, server, rawBody)
	assertRequestTooLarge(t, status, body)
	if upstreamCalled {
		t.Fatalf("upstream was called for a request already over the limit")
	}
	if !strings.Contains(body, "over the 4.0KB limit") {
		t.Fatalf("body = %s, want the configured limit reported", body)
	}
}

// A chunked upload declares no length, so the limit is only discovered while
// reading. The client still gets 413 rather than a parse or upstream failure,
// and the message reports no measured size because there is none.
func TestResponsesPassthroughRejectsUndeclaredOversizeWith413(t *testing.T) {
	const limit = 4096
	upstreamCalled := false
	server := newResponsesPassthroughSizeLimitedServer(t, limit, &upstreamCalled)
	handler := NewProxyMiddleware(server, server.config)

	rawBody := `{"model":"gpt-test","input":"` + strings.Repeat("x", limit*2) + `"}`
	// Hiding the reader's concrete type leaves Content-Length unknown, which is
	// what a chunked upload looks like to the handler.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", struct{ io.Reader }{strings.NewReader(rawBody)})
	req.Header.Set("Content-Type", "application/json")
	if req.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want -1 so the preflight cannot fire", req.ContentLength)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assertRequestTooLarge(t, resp.StatusCode, string(body))
	if upstreamCalled {
		t.Fatalf("upstream was called for a request over the limit")
	}
	if strings.Contains(string(body), "request body is") {
		t.Fatalf("body = %s, want no measured size when Content-Length is absent", string(body))
	}
}
