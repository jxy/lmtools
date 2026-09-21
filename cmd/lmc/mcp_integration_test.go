//go:build integration

package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/mcp/mcptest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mcpProviderServer plays a provider on the Chat wire that calls one MCP
// tool: its first answer is the call, every later one is plain text, and
// every request body is kept.
type mcpProviderServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	tool   string
	args   string
}

func newMCPProviderServer(t *testing.T, tool, args string) *mcpProviderServer {
	t.Helper()
	recorder := &mcpProviderServer{tool: tool, args: args}
	recorder.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.bodies = append(recorder.bodies, body)
		first := len(recorder.bodies) == 1
		recorder.mu.Unlock()

		message := map[string]interface{}{"role": "assistant", "content": "the server answered"}
		finish := "stop"
		if first {
			message = map[string]interface{}{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{{
					"id":       "call_mcp",
					"type":     "function",
					"function": map[string]interface{}{"name": recorder.tool, "arguments": recorder.args},
				}},
			}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-mcp",
			"object":  "chat.completion",
			"created": 1,
			"model":   "gpt-test",
			"choices": []map[string]interface{}{{"index": 0, "message": message, "finish_reason": finish}},
		})
	}))
	t.Cleanup(recorder.Close)
	return recorder
}

func (s *mcpProviderServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *mcpProviderServer) body(t *testing.T, index int) map[string]interface{} {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.bodies) {
		t.Fatalf("provider saw %d requests, want at least %d", len(s.bodies), index+1)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(s.bodies[index], &payload); err != nil {
		t.Fatalf("unmarshal request %d: %v", index, err)
	}
	return payload
}

// writeMCPConfigFile writes an mcpServers file whose one stdio server is
// this test binary playing scn.
func writeMCPConfigFile(t *testing.T, name string, scn mcptest.Scenario, extra map[string]interface{}) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	env, err := mcptest.ScenarioEnv(scn)
	if err != nil {
		t.Fatalf("scenario env: %v", err)
	}
	server := map[string]interface{}{
		"command": executable,
		"env":     map[string]string{mcptest.EnvScenario: env},
	}
	for key, value := range extra {
		server[key] = value
	}
	data, err := json.Marshal(map[string]interface{}{"mcpServers": map[string]interface{}{name: server}})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func echoScenario() mcptest.Scenario {
	return mcptest.Scenario{
		Era:          mcptest.EraModern,
		Instructions: "Use echo for echoing.",
		Tools: []mcp.Tool{{
			Name:        "echo",
			Description: "Echo the text",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		}},
		Results: map[string]mcp.CallToolResult{"echo": {Content: []mcp.Content{
			{Type: "text", Text: "hello from mcp"},
			{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(integrationPNG)},
		}}},
	}
}

func systemMessageText(t *testing.T, payload map[string]interface{}) string {
	t.Helper()
	messages, _ := payload["messages"].([]interface{})
	for _, raw := range messages {
		msg := raw.(map[string]interface{})
		if msg["role"] == "system" {
			text, _ := msg["content"].(string)
			return text
		}
	}
	return ""
}

func toolMessageAndFollower(t *testing.T, payload map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	messages, _ := payload["messages"].([]interface{})
	for i, raw := range messages {
		msg := raw.(map[string]interface{})
		if msg["role"] == "tool" {
			var follower map[string]interface{}
			if i+1 < len(messages) {
				follower = messages[i+1].(map[string]interface{})
			}
			return msg, follower
		}
	}
	t.Fatalf("no tool message in %v", messages)
	return nil, nil
}

func TestMCPToolOnChatWireRunsAndTheSessionKeepsIt(t *testing.T) {
	lmcBin := getLmcBinary(t)
	configPath := writeMCPConfigFile(t, "fake", echoScenario(), nil)
	toolName := mcp.QualifiedName("fake", "echo")
	provider := newMCPProviderServer(t, toolName, `{"text":"hi"}`)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	sessionsDir := t.TempDir()
	wantURL := core.ImageDataURL("image/png", integrationPNG)

	args := []string{
		"-provider", "openai",
		"-provider-url", provider.URL + "/v1",
		"-api-key-file", apiKeyFile,
		"-model", "gpt-test",
		"-sessions-dir", sessionsDir,
		"-mcp-config", configPath,
		"-tool-auto-approve",
	}
	stdout, stderr, err := runLmcCommand(t, lmcBin, args, "Say hi through the server")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "the server answered" {
		t.Fatalf("stdout = %q", stdout)
	}
	if provider.count() != 2 {
		t.Fatalf("provider saw %d requests, want the call and the follow-up", provider.count())
	}

	first := provider.body(t, 0)
	names := toolNames(t, first)
	if len(names) != 3 || names[0] != core.UniversalCommandToolName || names[1] != core.ViewImageToolName || names[2] != toolName {
		t.Fatalf("advertised tools = %v", names)
	}
	system := systemMessageText(t, first)
	if !strings.Contains(system, "Instructions from MCP server fake:\nUse echo for echoing.") || !strings.Contains(system, "Rules for universal_command") {
		t.Fatalf("system prompt = %q, want the tool prompt with the addendum", system)
	}

	toolMsg, follower := toolMessageAndFollower(t, provider.body(t, 1))
	if toolMsg["tool_call_id"] != "call_mcp" || toolMsg["content"] != "hello from mcp" {
		t.Fatalf("tool message = %#v", toolMsg)
	}
	parts, _ := follower["content"].([]interface{})
	if follower["role"] != "user" || len(parts) != 1 {
		t.Fatalf("message after the tool message = %#v, want the image alone", follower)
	}
	imagePart := parts[0].(map[string]interface{})
	imageURL, _ := imagePart["image_url"].(map[string]interface{})
	if imagePart["type"] != "image_url" || imageURL["url"] != wantURL {
		t.Fatalf("image part = %#v", imagePart)
	}

	for _, want := range []string{"MCP tool: fake/echo", `Arguments: {"text":"hi"}`, "1 image(s) attached", "hello from mcp"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}

	sessionID, shown := showSession(t, lmcBin, sessionsDir)
	if !strings.Contains(shown, "  • "+toolName+" (ID: call_mcp)\n     MCP: fake/echo\n") {
		t.Fatalf("-show output = %q, want the labelled call", shown)
	}
	if !strings.Contains(shown, "[image: image/png, 12 bytes]") {
		t.Fatalf("-show output = %q, want the image line under the result", shown)
	}

	// A resumed turn with the same servers keeps the session and its prompt.
	stdout, stderr, err = runLmcCommand(t, lmcBin, append(args, "-resume", sessionID), "And again?")
	if err != nil {
		t.Fatalf("resume failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stderr, "Forking session") {
		t.Fatalf("the resume forked the session:\n%s", stderr)
	}
	resumed := provider.body(t, provider.count()-1)
	if !strings.Contains(systemMessageText(t, resumed), "Instructions from MCP server fake:") {
		t.Fatal("the resumed request lost the addendum")
	}
	if got := strings.Count(string(mustJSON(t, resumed)), wantURL); got != 1 {
		t.Fatalf("the resumed request carries the image %d times, want once", got)
	}
}

func mustJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestMCPWhitelistRulesGovernScriptedRuns(t *testing.T) {
	lmcBin := getLmcBinary(t)
	configPath := writeMCPConfigFile(t, "fake", echoScenario(), nil)
	toolName := mcp.QualifiedName("fake", "echo")
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")

	for _, tt := range []struct {
		name     string
		rule     string
		wantText string
	}{
		{name: "the tool is granted", rule: `{"mcp":"fake","tool":"echo"}`, wantText: "hello from mcp"},
		{name: "the server is granted", rule: `{"mcp":"fake"}`, wantText: "hello from mcp"},
		{name: "another tool is granted", rule: `{"mcp":"fake","tool":"other"}`, wantText: `Add {"mcp":"fake","tool":"echo"} to your whitelist file`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := newMCPProviderServer(t, toolName, `{"text":"hi"}`)
			whitelist := filepath.Join(t.TempDir(), "whitelist.txt")
			if err := os.WriteFile(whitelist, []byte(tt.rule+"\n"), 0o600); err != nil {
				t.Fatalf("write whitelist: %v", err)
			}
			stdout, stderr, err := runLmcCommand(t, lmcBin, []string{
				"-provider", "openai",
				"-provider-url", provider.URL + "/v1",
				"-api-key-file", apiKeyFile,
				"-model", "gpt-test",
				"-no-session",
				"-mcp-config", configPath,
				"-tool-non-interactive", "-tool-whitelist", whitelist,
			}, "Say hi")
			if err != nil {
				t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			toolMsg, _ := toolMessageAndFollower(t, provider.body(t, 1))
			content, _ := toolMsg["content"].(string)
			if !strings.Contains(content, tt.wantText) {
				t.Fatalf("tool message content = %q, want %q", content, tt.wantText)
			}
		})
	}
}

func TestMCPToolsAppearInPrintCurl(t *testing.T) {
	lmcBin := getLmcBinary(t)
	configPath := writeMCPConfigFile(t, "fake", echoScenario(), nil)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")

	stdout, stderr, err := runLmcCommand(t, lmcBin, []string{
		"-provider", "openai",
		"-provider-url", "http://127.0.0.1:9/v1",
		"-api-key-file", apiKeyFile,
		"-model", "gpt-test",
		"-no-session",
		"-mcp-config", configPath,
		"-print-curl",
	}, "Say hi")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, mcp.QualifiedName("fake", "echo")) || !strings.Contains(stdout, "Echo the text") {
		t.Fatalf("curl output lacks the MCP tool:\n%s", stdout)
	}
}

func TestMCPServerFailuresAreReportedOrSkipped(t *testing.T) {
	lmcBin := getLmcBinary(t)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	broken := map[string]interface{}{"mcpServers": map[string]interface{}{"broken": map[string]interface{}{"command": "/nonexistent/mcp-server"}}}
	optional := map[string]interface{}{"mcpServers": map[string]interface{}{"broken": map[string]interface{}{"command": "/nonexistent/mcp-server", "optional": true}}}

	for _, tt := range []struct {
		name   string
		config map[string]interface{}
		wantOK bool
	}{
		{name: "a required server fails the run", config: broken, wantOK: false},
		{name: "an optional server is skipped", config: optional, wantOK: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.config)
			configPath := filepath.Join(t.TempDir(), "mcp.json")
			if err := os.WriteFile(configPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			provider := newMCPProviderServer(t, core.UniversalCommandToolName, `{"command":["true"]}`)
			stdout, stderr, err := runLmcCommand(t, lmcBin, []string{
				"-provider", "openai",
				"-provider-url", provider.URL + "/v1",
				"-api-key-file", apiKeyFile,
				"-model", "gpt-test",
				"-no-session",
				"-mcp-config", configPath,
				"-tool-auto-approve",
			}, "Hello")
			if tt.wantOK {
				if err != nil {
					t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
				}
				if !strings.Contains(stderr, `skipping optional MCP server "broken"`) {
					t.Fatalf("stderr lacks the skip warning:\n%s", stderr)
				}
				if names := toolNames(t, provider.body(t, 0)); len(names) != 2 {
					t.Fatalf("advertised tools = %v, want the built-in ones alone", names)
				}
				return
			}
			if err == nil {
				t.Fatalf("lmc succeeded without its server:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			if !strings.Contains(stderr, `mcp server "broken"`) {
				t.Fatalf("stderr lacks the server name:\n%s", stderr)
			}
			if provider.count() != 0 {
				t.Fatalf("the provider was contacted %d times before the failure", provider.count())
			}
		})
	}
}
