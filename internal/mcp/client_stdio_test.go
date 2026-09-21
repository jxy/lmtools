package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/mcp"
	"lmtools/internal/mcp/mcptest"
	"strings"
	"testing"
	"time"
)

func TestStdioModernServerSpeaksWithoutAHandshake(t *testing.T) {
	log := &testLog{}
	client, record := dialStdio(t, "modern", mcptest.Scenario{
		Era:          mcptest.EraModern,
		ServerInfo:   mcp.Implementation{Name: "fake-modern", Version: "2"},
		Instructions: "Use echo for echoing.",
		Tools:        []mcp.Tool{echoTool()},
		Results: map[string]mcp.CallToolResult{
			"echo": {Content: []mcp.Content{{Type: "text", Text: "hello back"}}},
		},
	}, mcp.DialOptions{Log: log.logf})

	if client.Era() != "modern" || client.Version() != mcp.ProtocolVersion20260728 {
		t.Fatalf("era/version = %s/%s, want modern/%s", client.Era(), client.Version(), mcp.ProtocolVersion20260728)
	}
	if info := client.ServerInfo(); info.Name != "fake-modern" || info.Version != "2" {
		t.Fatalf("ServerInfo() = %+v", info)
	}
	if client.Instructions() != "Use echo for echoing." {
		t.Fatalf("Instructions() = %q", client.Instructions())
	}
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools() = %+v, %v", tools, err)
	}
	if got := callText(t, client, "echo", `{"text":"hi"}`); got != "hello back" {
		t.Fatalf("call text = %q", got)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	messages := readRecord(t, record)
	if got := methods(messages); strings.Join(got, ",") != "server/discover,tools/list,tools/call" {
		t.Fatalf("server saw %v", got)
	}
	for _, msg := range messages {
		if msg.ProtocolVersion() != mcp.ProtocolVersion20260728 {
			t.Fatalf("%s carried protocol version %q in _meta", msg.Method, msg.ProtocolVersion())
		}
		info, _ := msg.Meta()["io.modelcontextprotocol/clientInfo"].(map[string]interface{})
		if info["name"] != "lmc" {
			t.Fatalf("%s carried clientInfo %v", msg.Method, info)
		}
		if _, ok := msg.Meta()["io.modelcontextprotocol/clientCapabilities"]; !ok {
			t.Fatalf("%s carried no clientCapabilities", msg.Method)
		}
	}
	call := messages[len(messages)-1]
	if name := call.ParamsMap()["name"]; name != "echo" {
		t.Fatalf("tools/call name = %v", name)
	}
	if args, _ := call.ParamsMap()["arguments"].(map[string]interface{}); args["text"] != "hi" {
		t.Fatalf("tools/call arguments = %v", call.ParamsMap()["arguments"])
	}
	if !log.contains("modern server") {
		t.Fatalf("log = %v", log.all())
	}
}

func TestStdioLegacyServerGetsTheInitializeHandshake(t *testing.T) {
	client, record := dialStdio(t, "legacy", mcptest.Scenario{
		Era:          mcptest.EraLegacy,
		ServerInfo:   mcp.Implementation{Name: "fake-legacy", Version: "1"},
		Instructions: "legacy guidance",
		Tools:        []mcp.Tool{echoTool()},
	}, mcp.DialOptions{})

	if client.Era() != "legacy" || client.Version() != mcp.ProtocolVersion20251125 {
		t.Fatalf("era/version = %s/%s", client.Era(), client.Version())
	}
	if client.ServerInfo().Name != "fake-legacy" || client.Instructions() != "legacy guidance" {
		t.Fatalf("server info/instructions = %+v / %q", client.ServerInfo(), client.Instructions())
	}
	if got := callText(t, client, "echo", `{"text":"x"}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
	_ = client.Close()

	messages := readRecord(t, record)
	if got := methods(messages); strings.Join(got, ",") != "server/discover,initialize,notifications/initialized,tools/call" {
		t.Fatalf("server saw %v", got)
	}
	initialize := messages[1]
	params := initialize.ParamsMap()
	if params["protocolVersion"] != mcp.ProtocolVersion20251125 {
		t.Fatalf("initialize protocolVersion = %v", params["protocolVersion"])
	}
	capabilities, _ := params["capabilities"].(map[string]interface{})
	if len(capabilities) != 0 {
		t.Fatalf("initialize declared capabilities %v; this client declares none", capabilities)
	}
	if info, _ := params["clientInfo"].(map[string]interface{}); info["name"] != "lmc" {
		t.Fatalf("initialize clientInfo = %v", params["clientInfo"])
	}
	if _, meta := messages[3].ParamsMap()["_meta"]; meta {
		t.Fatal("a legacy request carried _meta")
	}
}

func TestStdioSilentProbeFallsBackAfterTheStartupTimeout(t *testing.T) {
	started := time.Now()
	client, record := dialStdio(t, "silent", mcptest.Scenario{
		Era:   mcptest.EraSilent,
		Tools: []mcp.Tool{echoTool()},
	}, mcp.DialOptions{StartupTimeout: 300 * time.Millisecond})
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("fallback took %v", elapsed)
	}
	if client.Era() != "legacy" {
		t.Fatalf("era = %s, want legacy", client.Era())
	}
	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
	_ = client.Close()
	// The abandoned probe is cancelled before the handshake starts.
	if got := methods(readRecord(t, record)); strings.Join(got[:3], ",") != "server/discover,notifications/cancelled,initialize" {
		t.Fatalf("server saw %v", got)
	}
}

func TestStdioLegacyServerRequestsAreAnswered(t *testing.T) {
	client, record := dialStdio(t, "asking", mcptest.Scenario{
		Era:            mcptest.EraLegacy,
		Tools:          []mcp.Tool{echoTool()},
		ServerRequests: []string{"ping", "roots/list"},
	}, mcp.DialOptions{})

	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
	_ = client.Close()

	var replies []mcptest.Message
	for _, msg := range readRecord(t, record) {
		if msg.IsResponse() {
			replies = append(replies, msg)
		}
	}
	if len(replies) != 2 {
		t.Fatalf("client sent %d replies, want 2", len(replies))
	}
	if string(replies[0].Result) != "{}" || replies[0].Error != nil {
		t.Fatalf("ping reply = %+v, want an empty result", replies[0])
	}
	if replies[1].Error == nil || replies[1].Error.Code != mcp.CodeMethodNotFound {
		t.Fatalf("roots/list reply = %+v, want method not found", replies[1])
	}
}

func TestStdioCancellationSendsTheNotification(t *testing.T) {
	client, record := dialStdio(t, "slow", mcptest.Scenario{
		Era:         mcptest.EraModern,
		Tools:       []mcp.Tool{echoTool()},
		CallDelayMs: 10000,
	}, mcp.DialOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := client.CallTool(ctx, "echo", json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CallTool() error = %v, want deadline exceeded", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var cancelled *mcptest.Message
		messages := readRecord(t, record)
		for i := range messages {
			if messages[i].Method == "notifications/cancelled" {
				cancelled = &messages[i]
			}
		}
		if cancelled != nil {
			var call *mcptest.Message
			for i := range messages {
				if messages[i].Method == "tools/call" {
					call = &messages[i]
				}
			}
			var params struct {
				RequestID json.RawMessage `json:"requestId"`
				Reason    string          `json:"reason"`
			}
			if err := json.Unmarshal(cancelled.Params, &params); err != nil {
				t.Fatalf("cancelled params: %v", err)
			}
			if string(params.RequestID) != string(call.ID) {
				t.Fatalf("cancelled requestId %s, call id %s", params.RequestID, call.ID)
			}
			if !strings.Contains(params.Reason, "deadline") {
				t.Fatalf("cancelled reason = %q", params.Reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no cancellation reached the server; it saw %v", methods(messages))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStdioServerExitFailsTheCall(t *testing.T) {
	client, _ := dialStdio(t, "crash", mcptest.Scenario{
		Era:         mcptest.EraModern,
		Tools:       []mcp.Tool{echoTool()},
		CrashOnCall: true,
	}, mcp.DialOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.CallTool(ctx, "echo", json.RawMessage(`{}`), nil)
	if err == nil || !strings.Contains(err.Error(), "server exited") {
		t.Fatalf("CallTool() error = %v, want the exit", err)
	}
	_, err = client.ListTools(ctx)
	if err == nil || !strings.Contains(err.Error(), "server exited") {
		t.Fatalf("ListTools() after exit error = %v", err)
	}
}

func TestStdioStderrAndProgressGoToTheLog(t *testing.T) {
	log := &testLog{}
	client, _ := dialStdio(t, "noisy", mcptest.Scenario{
		Era:         mcptest.EraModern,
		Tools:       []mcp.Tool{echoTool()},
		StderrLines: []string{"starting fake server"},
		Progress:    2,
	}, mcp.DialOptions{Log: log.logf})

	if got := callText(t, client, "echo", `{}`); got != "ok" {
		t.Fatalf("call text = %q", got)
	}
	_ = client.Close()
	if !log.contains("stderr: starting fake server") {
		t.Fatalf("stderr line missing from log %v", log.all())
	}
	if !log.contains("progress") {
		t.Fatalf("progress notification missing from log %v", log.all())
	}
}

func TestStdioCloseEscalatesToSignals(t *testing.T) {
	for _, tt := range []struct {
		name          string
		ignoreSigterm bool
		limit         time.Duration
	}{
		{name: "SIGTERM after stdin close", ignoreSigterm: false, limit: 6 * time.Second},
		{name: "SIGKILL after SIGTERM", ignoreSigterm: true, limit: 8 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := dialStdio(t, "stubborn", mcptest.Scenario{
				Era:              mcptest.EraModern,
				Tools:            []mcp.Tool{echoTool()},
				IgnoreStdinClose: true,
				IgnoreSIGTERM:    tt.ignoreSigterm,
			}, mcp.DialOptions{})
			started := time.Now()
			if err := client.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if elapsed := time.Since(started); elapsed > tt.limit {
				t.Fatalf("Close() took %v", elapsed)
			}
		})
	}
}

func TestStdioListToolsFollowsCursors(t *testing.T) {
	tools := []mcp.Tool{echoTool(), echoTool(), echoTool()}
	tools[1].Name = "second"
	tools[2].Name = "third"
	client, record := dialStdio(t, "paged", mcptest.Scenario{
		Era:      mcptest.EraModern,
		Tools:    tools,
		PageSize: 1,
	}, mcp.DialOptions{})

	listed, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(listed) != 3 || listed[2].Name != "third" {
		t.Fatalf("ListTools() = %+v", listed)
	}
	_ = client.Close()
	var cursors []string
	for _, msg := range readRecord(t, record) {
		if msg.Method == "tools/list" {
			cursor, _ := msg.ParamsMap()["cursor"].(string)
			cursors = append(cursors, cursor)
		}
	}
	if strings.Join(cursors, ",") != ",1,2" {
		t.Fatalf("cursors = %q", cursors)
	}
}

func TestStdioVersionNegotiation(t *testing.T) {
	t.Run("server without a common version", func(t *testing.T) {
		cfg, _ := stdioConfig(t, "future", mcptest.Scenario{
			Era:               mcptest.EraModern,
			SupportedVersions: []string{"2099-01-01"},
			Tools:             []mcp.Tool{echoTool()},
		})
		_, err := mcp.Dial(context.Background(), cfg, mcp.DialOptions{})
		if err == nil || !strings.Contains(err.Error(), "2099-01-01") {
			t.Fatalf("Dial() error = %v, want the version list", err)
		}
	})
	t.Run("server listing legacy versions beside the modern one", func(t *testing.T) {
		client, _ := dialStdio(t, "both", mcptest.Scenario{
			Era:               mcptest.EraModern,
			SupportedVersions: []string{mcp.ProtocolVersion20251125, mcp.ProtocolVersion20260728},
			Tools:             []mcp.Tool{echoTool()},
		}, mcp.DialOptions{})
		if client.Version() != mcp.ProtocolVersion20260728 {
			t.Fatalf("Version() = %s", client.Version())
		}
	})
}

func TestStdioResultsPassThrough(t *testing.T) {
	client, _ := dialStdio(t, "results", mcptest.Scenario{
		Era:   mcptest.EraModern,
		Tools: []mcp.Tool{echoTool(), {Name: "ask", InputSchema: json.RawMessage(`{"type":"object"}`)}, {Name: "fail", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Results: map[string]mcp.CallToolResult{
			"ask": {
				ResultType:    mcp.ResultTypeInputRequired,
				InputRequests: json.RawMessage(`{"login":{"method":"elicitation/create","params":{"mode":"form","message":"who?"}}}`),
				RequestState:  "opaque",
			},
			"fail": {IsError: true, Content: []mcp.Content{{Type: "text", Text: "bad input"}}},
		},
		Errors: map[string]*mcp.RPCError{"echo": {Code: mcp.CodeInvalidParams, Message: "Unknown tool: echo"}},
	}, mcp.DialOptions{})

	ctx := context.Background()
	ask, err := client.CallTool(ctx, "ask", json.RawMessage(`{}`), nil)
	if err != nil || !ask.InputRequired() || ask.RequestState != "opaque" {
		t.Fatalf("ask = %+v, %v", ask, err)
	}
	fail, err := client.CallTool(ctx, "fail", json.RawMessage(`{}`), nil)
	if err != nil || !fail.IsError || fail.Content[0].Text != "bad input" {
		t.Fatalf("fail = %+v, %v", fail, err)
	}
	_, err = client.CallTool(ctx, "echo", json.RawMessage(`{}`), nil)
	var rpcErr *mcp.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != mcp.CodeInvalidParams {
		t.Fatalf("echo error = %v, want the JSON-RPC error", err)
	}
	if !strings.Contains(err.Error(), `mcp server "results": tools/call: Unknown tool: echo (JSON-RPC error -32602)`) {
		t.Fatalf("echo error text = %q", err)
	}
}

func TestStdioCommandThatCannotStart(t *testing.T) {
	_, err := mcp.Dial(context.Background(), mcp.ServerConfig{
		Name:    "missing",
		Type:    mcp.TransportStdio,
		Command: "/nonexistent/mcp-server",
	}, mcp.DialOptions{})
	if err == nil || !strings.Contains(err.Error(), `mcp server "missing"`) {
		t.Fatalf("Dial() error = %v", err)
	}
}
