package mcp_test

import (
	"context"
	"encoding/json"
	"lmtools/internal/mcp"
	"lmtools/internal/mcp/mcptest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func httpFake(t *testing.T, scn mcptest.Scenario) (*mcptest.Handler, mcp.ServerConfig) {
	t.Helper()
	handler := mcptest.NewHandler(scn)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return handler, mcp.ServerConfig{Name: "web", Type: mcp.TransportHTTP, URL: server.URL + "/mcp"}
}

func dialHTTP(t *testing.T, cfg mcp.ServerConfig, opts mcp.DialOptions) *mcp.Client {
	t.Helper()
	client, err := mcp.Dial(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func requestsFor(handler *mcptest.Handler, method string) []mcptest.Recorded {
	var out []mcptest.Recorded
	for _, req := range handler.Requests() {
		if req.Message != nil && req.Message.Method == method {
			out = append(out, req)
		}
	}
	return out
}

func TestHTTPModernRequestsCarryTheRoutingHeaders(t *testing.T) {
	handler, cfg := httpFake(t, mcptest.Scenario{
		Era:          mcptest.EraModern,
		CheckHeaders: true,
		Tools:        []mcp.Tool{echoTool()},
		Results:      map[string]mcp.CallToolResult{"echo": {Content: []mcp.Content{{Type: "text", Text: "web ok"}}}},
	})
	client := dialHTTP(t, cfg, mcp.DialOptions{})

	if client.Era() != "modern" {
		t.Fatalf("era = %s", client.Era())
	}
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("ListTools() = %+v, %v", tools, err)
	}
	if got := callText(t, client, "echo", `{"text":"hi"}`); got != "web ok" {
		t.Fatalf("call text = %q", got)
	}

	calls := requestsFor(handler, "tools/call")
	if len(calls) != 1 {
		t.Fatalf("saw %d tools/call requests", len(calls))
	}
	headers := calls[0].Headers
	for name, want := range map[string]string{
		"Accept":               "application/json, text/event-stream",
		"Content-Type":         "application/json",
		"Mcp-Protocol-Version": mcp.ProtocolVersion20260728,
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "echo",
	} {
		if got := headers.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if headers.Get("Mcp-Session-Id") != "" {
		t.Error("a modern request carried a session id")
	}
	if requestsFor(handler, "initialize") != nil {
		t.Error("a modern server received initialize")
	}
}

func TestHTTPModernStreamedResponses(t *testing.T) {
	log := &testLog{}
	_, cfg := httpFake(t, mcptest.Scenario{
		Era:             mcptest.EraModern,
		StreamResponses: true,
		Progress:        2,
		Tools:           []mcp.Tool{echoTool()},
	})
	client := dialHTTP(t, cfg, mcp.DialOptions{Log: log.logf})
	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
	if !log.contains("progress") {
		t.Fatalf("progress missing from log %v", log.all())
	}
}

func TestHTTPModernParamHeaders(t *testing.T) {
	tool := mcp.Tool{
		Name: "query",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"region":{"type":"string","x-mcp-header":"Region"},
			"count":{"type":"integer","x-mcp-header":"Count"},
			"dry":{"type":"boolean","x-mcp-header":"Dry"},
			"sql":{"type":"string"}}}`),
	}
	handler, cfg := httpFake(t, mcptest.Scenario{Era: mcptest.EraModern, CheckHeaders: true, Tools: []mcp.Tool{tool}})
	host, err := mcp.Connect(context.Background(), []mcp.ServerConfig{cfg}, mcp.HostOptions{})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(host.Close)

	name := mcp.QualifiedName("web", "query")
	for _, tt := range []struct {
		name string
		args string
		want map[string]string
	}{
		{
			name: "plain values", args: `{"region":"us-west1","count":3,"dry":true,"sql":"select 1"}`,
			want: map[string]string{"Mcp-Param-Region": "us-west1", "Mcp-Param-Count": "3", "Mcp-Param-Dry": "true"},
		},
		{
			name: "non ASCII and null", args: `{"region":"Hello, 世界","count":null}`,
			want: map[string]string{"Mcp-Param-Region": "=?base64?SGVsbG8sIOS4lueVjA==?=", "Mcp-Param-Count": "", "Mcp-Param-Dry": ""},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := len(handler.Requests())
			result, err := host.Call(context.Background(), name, json.RawMessage(tt.args))
			if err != nil || result.IsError {
				t.Fatalf("Call() = %+v, %v", result, err)
			}
			last := handler.Requests()[len(handler.Requests())-1]
			if len(handler.Requests()) != before+1 {
				t.Fatalf("expected one request, saw %d", len(handler.Requests())-before)
			}
			for header, want := range tt.want {
				if got := last.Headers.Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}

	_, err = host.Call(context.Background(), name, json.RawMessage(`{"count":1.5}`))
	if err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("a fractional integer argument error = %v", err)
	}
}

func TestHTTPLegacySessions(t *testing.T) {
	handler, cfg := httpFake(t, mcptest.Scenario{
		Era:        mcptest.EraLegacy,
		SessionIDs: true,
		Tools:      []mcp.Tool{echoTool()},
	})
	client := dialHTTP(t, cfg, mcp.DialOptions{})
	if client.Era() != "legacy" || client.Version() != mcp.ProtocolVersion20251125 {
		t.Fatalf("era/version = %s/%s", client.Era(), client.Version())
	}
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}

	requests := handler.Requests()
	var seen []string
	for _, req := range requests {
		if req.Message != nil {
			seen = append(seen, req.Message.Method)
		}
	}
	if strings.Join(seen, ",") != "server/discover,initialize,notifications/initialized,tools/list,tools/call" {
		t.Fatalf("server saw %v", seen)
	}
	session := ""
	for _, req := range requests[2:] {
		if session == "" {
			session = req.Headers.Get("Mcp-Session-Id")
		}
		if session == "" || req.Headers.Get("Mcp-Session-Id") != session {
			t.Fatalf("%s carried session %q, want %q", req.Message.Method, req.Headers.Get("Mcp-Session-Id"), session)
		}
		if req.Headers.Get("Mcp-Protocol-Version") != mcp.ProtocolVersion20251125 {
			t.Fatalf("%s carried protocol version header %q", req.Message.Method, req.Headers.Get("Mcp-Protocol-Version"))
		}
		if req.Headers.Get("Mcp-Method") != "" {
			t.Fatalf("%s carried the modern Mcp-Method header", req.Message.Method)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if handler.Deletes() != 1 {
		t.Fatalf("session deletes = %d, want 1", handler.Deletes())
	}
}

func TestHTTPLegacyExpiredSessionIsStartedAgain(t *testing.T) {
	handler, cfg := httpFake(t, mcptest.Scenario{
		Era:                mcptest.EraLegacy,
		SessionIDs:         true,
		ExpireSessionAfter: 1,
		Tools:              []mcp.Tool{echoTool()},
	})
	client := dialHTTP(t, cfg, mcp.DialOptions{})
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
	if got := len(requestsFor(handler, "initialize")); got != 2 {
		t.Fatalf("initialize was sent %d times, want 2", got)
	}
	calls := requestsFor(handler, "tools/call")
	if len(calls) != 2 || calls[0].Headers.Get("Mcp-Session-Id") == calls[1].Headers.Get("Mcp-Session-Id") {
		t.Fatalf("the retried call did not carry the new session: %d calls", len(calls))
	}
}

func TestHTTPLegacyServerRequestOnTheStreamIsAnswered(t *testing.T) {
	handler, cfg := httpFake(t, mcptest.Scenario{
		Era:            mcptest.EraLegacy,
		ServerRequests: []string{"ping"},
		Tools:          []mcp.Tool{echoTool()},
	})
	client := dialHTTP(t, cfg, mcp.DialOptions{})
	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
	var replies int
	for _, req := range handler.Requests() {
		if req.Message != nil && req.Message.IsResponse() {
			replies++
			if string(req.Message.Result) != "{}" {
				t.Fatalf("ping reply = %+v", req.Message)
			}
		}
	}
	if replies != 1 {
		t.Fatalf("client posted %d replies, want 1", replies)
	}
}

func TestHTTPLegacyDetectedFromAPlainBadRequest(t *testing.T) {
	_, cfg := httpFake(t, mcptest.Scenario{
		Era:            mcptest.EraLegacy,
		DiscoverStatus: http.StatusBadRequest,
		DiscoverBody:   "Bad Request: Server not initialized",
		Tools:          []mcp.Tool{echoTool()},
	})
	client := dialHTTP(t, cfg, mcp.DialOptions{})
	if client.Era() != "legacy" {
		t.Fatalf("era = %s", client.Era())
	}
	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
}

func TestHTTPUnauthorizedNamesTheChallenge(t *testing.T) {
	_, cfg := httpFake(t, mcptest.Scenario{Era: mcptest.EraModern, AuthToken: "secret", Tools: []mcp.Tool{echoTool()}})
	_, err := mcp.Dial(context.Background(), cfg, mcp.DialOptions{})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "WWW-Authenticate") {
		t.Fatalf("Dial() error = %v, want the 401 challenge", err)
	}

	cfg.Headers = map[string]string{"Authorization": "Bearer secret"}
	client := dialHTTP(t, cfg, mcp.DialOptions{})
	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
}

func TestHTTPUnexpectedContentTypeIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(server.Close)
	_, err := mcp.Dial(context.Background(), mcp.ServerConfig{Name: "plain", Type: mcp.TransportHTTP, URL: server.URL}, mcp.DialOptions{})
	if err == nil || !strings.Contains(err.Error(), "Content-Type") {
		t.Fatalf("Dial() error = %v", err)
	}
}
