//go:build integration

package main

import (
	"encoding/json"
	"io"
	"lmtools/internal/core"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// viewImageServer plays a provider that asks to see a file: its first answer
// is a view_image call for imagePath, every later one is plain text, and
// every request body is kept. wire selects the response shape.
type viewImageServer struct {
	*httptest.Server
	mu        sync.Mutex
	bodies    [][]byte
	imagePath string
	wire      string
}

func newViewImageServer(t *testing.T, wire, imagePath string) *viewImageServer {
	t.Helper()
	recorder := &viewImageServer{imagePath: imagePath, wire: wire}
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

		w.Header().Set("Content-Type", "application/json")
		var payload map[string]interface{}
		switch recorder.wire {
		case "anthropic":
			payload = recorder.anthropicResponse(first)
		default:
			payload = recorder.chatResponse(first)
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(recorder.Close)
	return recorder
}

func (s *viewImageServer) chatResponse(first bool) map[string]interface{} {
	message := map[string]interface{}{"role": "assistant", "content": "a plot of y = x^2"}
	finish := "stop"
	if first {
		args, _ := json.Marshal(map[string]string{"path": s.imagePath, "detail": "low"})
		message = map[string]interface{}{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []map[string]interface{}{{
				"id":       "call_img",
				"type":     "function",
				"function": map[string]interface{}{"name": core.ViewImageToolName, "arguments": string(args)},
			}},
		}
		finish = "tool_calls"
	}
	return map[string]interface{}{
		"id":      "chatcmpl-view-image",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-test",
		"choices": []map[string]interface{}{{"index": 0, "message": message, "finish_reason": finish}},
	}
}

func (s *viewImageServer) anthropicResponse(first bool) map[string]interface{} {
	content := []map[string]interface{}{{"type": "text", "text": "a plot of y = x^2"}}
	if first {
		content = []map[string]interface{}{
			{"type": "text", "text": "Let me look."},
			{"type": "tool_use", "id": "toolu_img", "name": core.ViewImageToolName, "input": map[string]interface{}{"path": s.imagePath}},
		}
	}
	return map[string]interface{}{
		"id":      "msg_view_image",
		"type":    "message",
		"role":    "assistant",
		"model":   "claude-test",
		"content": content,
	}
}

func (s *viewImageServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *viewImageServer) body(t *testing.T, index int) map[string]interface{} {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.bodies) {
		t.Fatalf("server saw %d requests, want at least %d", len(s.bodies), index+1)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(s.bodies[index], &payload); err != nil {
		t.Fatalf("unmarshal request %d: %v", index, err)
	}
	return payload
}

func toolNames(t *testing.T, payload map[string]interface{}) []string {
	t.Helper()
	tools, _ := payload["tools"].([]interface{})
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool := raw.(map[string]interface{})
		if function, ok := tool["function"].(map[string]interface{}); ok {
			names = append(names, function["name"].(string))
			continue
		}
		names = append(names, tool["name"].(string))
	}
	return names
}

func showSession(t *testing.T, lmcBin, sessionsDir string) (sessionID, shown string) {
	t.Helper()
	listing, stderr, err := runLmcCommand(t, lmcBin, []string{"-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("show-sessions failed: %v\nstderr:\n%s", err, stderr)
	}
	sessionID = extractFirstSessionID(listing)
	if sessionID == "" {
		t.Fatalf("no session id in listing:\n%s", listing)
	}
	shown, stderr, err = runLmcCommand(t, lmcBin, []string{"-show", sessionID, "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("show failed: %v\nstderr:\n%s", err, stderr)
	}
	return sessionID, shown
}

func TestViewImageToolOnChatWireSendsTheFileBackAndTheSessionKeepsIt(t *testing.T) {
	lmcBin := getLmcBinary(t)
	imagePath := writeIntegrationImage(t, "plot.png")
	server := newViewImageServer(t, "chat", imagePath)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	sessionsDir := t.TempDir()
	wantURL := core.ImageDataURL("image/png", integrationPNG)
	wantText := "Attached image plot.png (image/png, 12 bytes)."

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "gpt-test",
			"-sessions-dir", sessionsDir,
			"-tool", "-tool-auto-approve",
		},
		"Look at plot.png")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "a plot of y = x^2" {
		t.Fatalf("stdout = %q, want the final answer", stdout)
	}
	if server.count() != 2 {
		t.Fatalf("server saw %d requests, want the call and the follow-up", server.count())
	}

	names := toolNames(t, server.body(t, 0))
	if len(names) != 2 || names[0] != core.UniversalCommandToolName || names[1] != core.ViewImageToolName {
		t.Fatalf("advertised tools = %v, want universal_command and view_image", names)
	}

	messages := server.body(t, 1)["messages"].([]interface{})
	var toolMsg, userAfter map[string]interface{}
	for i, raw := range messages {
		msg := raw.(map[string]interface{})
		if msg["role"] == "tool" {
			toolMsg = msg
			if i+1 < len(messages) {
				userAfter = messages[i+1].(map[string]interface{})
			}
		}
	}
	if toolMsg == nil || toolMsg["tool_call_id"] != "call_img" || toolMsg["content"] != wantText {
		t.Fatalf("tool message = %#v, want the description for call_img", toolMsg)
	}
	if userAfter == nil || userAfter["role"] != "user" {
		t.Fatalf("message after the tool message = %#v, want a user message carrying the image", userAfter)
	}
	parts, _ := userAfter["content"].([]interface{})
	if len(parts) != 1 {
		t.Fatalf("user message parts = %#v, want the image alone", userAfter["content"])
	}
	imagePart := parts[0].(map[string]interface{})
	imageURL, _ := imagePart["image_url"].(map[string]interface{})
	if imagePart["type"] != "image_url" || imageURL["url"] != wantURL || imageURL["detail"] != "low" {
		t.Fatalf("image part = %#v", imagePart)
	}

	for _, want := range []string{`View image: "` + imagePath + `"`, "Detail: low", wantText} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "base64,") {
		t.Fatal("stderr printed image bytes")
	}

	sessionID, shown := showSession(t, lmcBin, sessionsDir)
	if !strings.Contains(shown, "     "+wantText+"\n     [image: plot.png (image/png, 12 bytes)]\n") {
		t.Fatalf("-show output = %q, want the result text followed by the image line", shown)
	}
	if strings.Contains(shown, "base64,") {
		t.Fatal("-show printed image bytes")
	}

	// The bytes are stored once, in the blocks file of the results message.
	sessionDir := filepath.Join(sessionsDir, sessionID)
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	stored := 0
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			continue
		}
		occurrences := strings.Count(string(data), wantURL)
		if occurrences > 0 && !strings.HasSuffix(entry.Name(), ".blocks.json") {
			t.Errorf("%s holds the image bytes; only .blocks.json should", entry.Name())
		}
		stored += occurrences
	}
	if stored != 1 {
		t.Fatalf("session holds the image %d times, want once", stored)
	}

	// A resumed turn replays the image from the session, once.
	stdout, stderr, err = runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "gpt-test",
			"-sessions-dir", sessionsDir,
			"-tool", "-tool-auto-approve",
			"-resume", sessionID,
		},
		"And the axes?")
	if err != nil {
		t.Fatalf("resume failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	server.mu.Lock()
	resumed := string(server.bodies[len(server.bodies)-1])
	server.mu.Unlock()
	if got := strings.Count(resumed, wantURL); got != 1 {
		t.Fatalf("resumed request carries the image %d times, want once", got)
	}
}

func TestViewImageToolOnAnthropicWireNestsTheImageInTheToolResult(t *testing.T) {
	lmcBin := getLmcBinary(t)
	imagePath := writeIntegrationImage(t, "plot.png")
	server := newViewImageServer(t, "anthropic", imagePath)
	apiKeyFile := writeTestAPIKeyFile(t, "test-anthropic-key")
	wantText := "Attached image plot.png (image/png, 12 bytes)."

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL,
			"-api-key-file", apiKeyFile,
			"-model", "claude-test",
			"-no-session",
			"-tool", "-tool-auto-approve",
		},
		"Look at plot.png")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if server.count() != 2 {
		t.Fatalf("server saw %d requests, want the call and the follow-up", server.count())
	}

	messages := server.body(t, 1)["messages"].([]interface{})
	last := messages[len(messages)-1].(map[string]interface{})
	content, _ := last["content"].([]interface{})
	if last["role"] != "user" || len(content) != 1 {
		t.Fatalf("last message = %#v, want a user message holding the tool result", last)
	}
	result := content[0].(map[string]interface{})
	parts, _ := result["content"].([]interface{})
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_img" || len(parts) != 2 {
		t.Fatalf("tool_result = %#v, want a text and an image block", result)
	}
	text := parts[0].(map[string]interface{})
	if text["type"] != "text" || text["text"] != wantText {
		t.Fatalf("parts[0] = %#v", text)
	}
	image := parts[1].(map[string]interface{})
	source, _ := image["source"].(map[string]interface{})
	if image["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" {
		t.Fatalf("parts[1] = %#v", image)
	}
	if want := strings.TrimPrefix(core.ImageDataURL("image/png", integrationPNG), "data:image/png;base64,"); source["data"] != want {
		t.Fatalf("image data = %v, want the file's base64", source["data"])
	}
}

// Piped stdin is no terminal, so there is nobody to ask: the call is refused
// with the reason, the model is told, and no bytes leave the machine.
func TestViewImageToolIsRefusedWhenNobodyCanApprove(t *testing.T) {
	lmcBin := getLmcBinary(t)
	imagePath := writeIntegrationImage(t, "plot.png")
	server := newViewImageServer(t, "chat", imagePath)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "gpt-test",
			"-no-session",
			"-tool",
		},
		"Look at plot.png")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "Not run: no terminal available for approval") {
		t.Fatalf("stderr lacks the refusal:\n%s", stderr)
	}
	server.mu.Lock()
	followUp := string(server.bodies[1])
	server.mu.Unlock()
	if strings.Contains(followUp, "base64,") {
		t.Fatal("a refused image was sent to the provider")
	}
	if !strings.Contains(followUp, "denied: no terminal available for approval") {
		t.Fatalf("follow-up request does not tell the model why:\n%s", followUp)
	}
}

// A whitelist rule naming the directory admits the file with nobody to ask,
// which is what a scripted run needs; one that names only commands refuses
// it and prints the rule that would admit it.
func TestViewImageWhitelistRulesGovernScriptedRuns(t *testing.T) {
	lmcBin := getLmcBinary(t)
	imagePath := writeIntegrationImage(t, "plot.png")
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	rulesDir := t.TempDir()
	imagesUnderDir := filepath.Join(rulesDir, "images.txt")
	if err := os.WriteFile(imagesUnderDir, []byte(`{"tool":"view_image","path":"`+filepath.Dir(imagePath)+`"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write whitelist: %v", err)
	}
	commandsOnly := filepath.Join(rulesDir, "commands.txt")
	if err := os.WriteFile(commandsOnly, []byte(`["echo"]`+"\n"), 0o600); err != nil {
		t.Fatalf("write whitelist: %v", err)
	}
	wantURL := core.ImageDataURL("image/png", integrationPNG)

	run := func(whitelist string) (stderr, followUp string) {
		t.Helper()
		server := newViewImageServer(t, "chat", imagePath)
		stdout, stderr, err := runLmcCommand(t, lmcBin,
			[]string{
				"-provider", "openai",
				"-provider-url", server.URL + "/v1",
				"-api-key-file", apiKeyFile,
				"-model", "gpt-test",
				"-no-session",
				"-tool", "-tool-whitelist", whitelist,
			},
			"Look at plot.png")
		if err != nil {
			t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if server.count() != 2 {
			t.Fatalf("server saw %d requests, want the call and the follow-up", server.count())
		}
		server.mu.Lock()
		defer server.mu.Unlock()
		return stderr, string(server.bodies[1])
	}

	stderr, followUp := run(imagesUnderDir)
	if !strings.Contains(stderr, "Completed in") || !strings.Contains(followUp, wantURL) {
		t.Fatalf("whitelisted image was not sent\nstderr:\n%s", stderr)
	}

	stderr, followUp = run(commandsOnly)
	suggestion := `{"tool":"view_image","path":"` + imagePath + `"}`
	for _, want := range []string{"Not run: not in whitelist", "To allow this image, either:", suggestion} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(followUp, "base64,") {
		t.Fatal("a refused image was sent to the provider")
	}
	if !strings.Contains(followUp, "denied: not in whitelist") {
		t.Fatalf("follow-up request does not tell the model why:\n%s", followUp)
	}
}
