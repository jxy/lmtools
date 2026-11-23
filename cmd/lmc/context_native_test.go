package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"lmtools/internal/config"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"lmtools/internal/mcp"
	"lmtools/internal/ui"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeSizingExposesCredentialRefusal(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"credential refused"}}`))
			}))
			defer server.Close()
			env := newTurnTestEnvAt(t, server.URL, "-context-policy", "full", "-context-window", "8192")
			engine := newContextWindow(env.env, env.opts)
			var messages []core.TypedMessage
			// Individually short fields can be measured only as native exchanges.
			for i := 0; i < 18; i++ {
				messages = append(messages, core.NewTextMessage("user", strings.Repeat("x", 1024)), core.NewTextMessage("assistant", "done"))
			}
			messages = append(messages, core.NewTextMessage("user", "continue"))
			_, err := engine.Project(context.Background(), messages, nil, nil, false)
			var refused *lmerrors.HTTPError
			if !errors.As(err, &refused) || refused.StatusCode != status || calls.Load() != 1 {
				t.Fatalf("native requests=%d, expected HTTP %d, got %v", calls.Load(), status, err)
			}
		})
	}
}

// Two endpoints serving one model name, tool definitions of one length, or
// two reasoning settings can count the same messages differently, so a
// request measured under one is measured again under the other.
func TestSizingIdentityNamesTheEndpointAndTools(t *testing.T) {
	opts := core.RequestOptions{Provider: "openai", ProviderURL: "https://one.example/v1", Model: "shared-model"}
	tools := []byte(`[{"name":"lookup","description":"Find a row by key."}]`)
	other := opts
	other.ProviderURL = "https://two.example/v1"
	if sizingIdentity(opts, tools) == sizingIdentity(other, tools) {
		t.Fatal("two endpoints share a sizing identity")
	}
	if sizingIdentity(opts, tools) == sizingIdentity(opts, []byte(`[{"name":"lookup","description":"Find a key by row."}]`)) {
		t.Fatal("two tool definitions of one length share a sizing identity")
	}
	for _, reasoning := range []func(*core.RequestOptions){
		func(o *core.RequestOptions) { o.ReasoningContext = "all_turns" },
		func(o *core.RequestOptions) { o.ReasoningMode = "pro" },
		func(o *core.RequestOptions) { o.Effort = "high" },
	} {
		changed := opts
		changed.ReasoningContext = "current_turn"
		before := sizingIdentity(changed, tools)
		reasoning(&changed)
		if sizingIdentity(changed, tools) == before {
			t.Fatal("two reasoning settings share a sizing identity")
		}
	}
	same := opts
	if sizingIdentity(opts, tools) != sizingIdentity(same, append([]byte(nil), tools...)) {
		t.Fatal("one configuration has two sizing identities")
	}
}

func TestConnectionNoticeCountsExternalServers(t *testing.T) {
	for _, tc := range []struct {
		remote, announce bool
		want             string
	}{
		{false, true, ""}, {true, true, "Connecting 1 MCP server(s)..."}, {true, false, ""},
	} {
		cfg := &config.Config{MCPServers: []mcp.ServerConfig{{Name: "history", Type: "local"}}}
		if tc.remote {
			cfg.MCPServers = append(cfg.MCPServers, mcp.ServerConfig{Name: "test", Type: "stdio", Command: filepath.Join(t.TempDir(), "unavailable")})
		}
		var out bytes.Buffer
		host, _ := connectMCPServers(context.Background(), cfg, &core.RequestOptions{}, ui.NewREPLNotifier(&out), tc.announce)
		if host != nil {
			host.Close()
		}
		if tc.want == "" {
			if strings.Contains(out.String(), "Connecting ") {
				t.Errorf("unexpected connection notice: %q", out.String())
			}
		} else if !strings.Contains(out.String(), tc.want) {
			t.Errorf("missing %q in %q", tc.want, out.String())
		}
	}
}

func TestNativeCountUsesTheOrdinaryToolSchemaWithoutExecutingReturnedCalls(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "unexpected-tool-execution")
	arguments, err := json.Marshal(map[string]any{"command": []string{"touch", marker}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools    []json.RawMessage `json:"tools"`
			Format   json.RawMessage   `json:"response_format"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Max           int  `json:"max_tokens"`
			CompletionMax int  `json:"max_completion_tokens"`
			Stream        bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Tools) == 0 || len(request.Format) != 0 || request.Stream || max(request.Max, request.CompletionMax) != 64 {
			t.Error("invalid native count request")
		}
		found := false
		for _, m := range request.Messages {
			if m.Role == "tool" {
				found = true
			}
		}
		if !found {
			t.Error("recorded tool result lost its native role")
		}
		response := map[string]any{
			"id": "measured", "model": "gpt-test", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "never-execute", "type": "function", "function": map[string]any{"name": "universal_command", "arguments": string(arguments)}}}}}},
			"usage":   map[string]any{"prompt_tokens": 123, "completion_tokens": 12, "total_tokens": 135},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	env := newTurnTestEnvAt(t, server.URL, "-tool", "-tool-auto-approve", "-context-policy", "full", "-context-window", "8192")
	env.opts.JSONMode = true
	env.opts.JSONSchema = json.RawMessage(`{"type":"object"}`)
	engine := newContextWindow(env.env, env.opts)
	messages := []core.TypedMessage{
		core.NewTextMessage("user", "previous source"),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "done", Name: "universal_command", Input: json.RawMessage(`{"command":["true"]}`)}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "done", Name: "universal_command", Content: "observed"}}},
		core.NewTextMessage("assistant", "recorded"),
		core.NewTextMessage("user", "measurement"),
	}
	n, err := engine.CountMessages(context.Background(), messages)
	if err != nil || n != 123 {
		t.Fatalf("native input count %d: %v", n, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sizing call reached the tool executor: %v", err)
	}
}
