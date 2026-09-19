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

var integrationPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

// imageRecordingServer answers OpenAI Chat Completions with a fixed reply and
// keeps every request body it saw.
type imageRecordingServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newImageRecordingServer(t *testing.T) *imageRecordingServer {
	t.Helper()
	recorder := &imageRecordingServer{}
	recorder.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.bodies = append(recorder.bodies, body)
		recorder.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-image",
			"object":  "chat.completion",
			"created": 1,
			"model":   "gpt-test",
			"choices": []map[string]interface{}{{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": "a picture"},
				"finish_reason": "stop",
			}},
		})
	}))
	t.Cleanup(recorder.Close)
	return recorder
}

func (s *imageRecordingServer) body(t *testing.T, index int) map[string]interface{} {
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

func writeIntegrationImage(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, integrationPNG, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	return path
}

// imageParts returns the image_url values and text parts of the last user
// message in an OpenAI Chat Completions request.
func imageParts(t *testing.T, payload map[string]interface{}) (urls []string, texts []string) {
	t.Helper()
	messages := payload["messages"].([]interface{})
	var last map[string]interface{}
	for _, raw := range messages {
		msg := raw.(map[string]interface{})
		if msg["role"] == "user" {
			last = msg
		}
	}
	if last == nil {
		t.Fatalf("no user message in %v", messages)
	}
	switch content := last["content"].(type) {
	case string:
		return nil, []string{content}
	case []interface{}:
		for _, raw := range content {
			part := raw.(map[string]interface{})
			switch part["type"] {
			case "image_url":
				urls = append(urls, part["image_url"].(map[string]interface{})["url"].(string))
			case "text":
				texts = append(texts, part["text"].(string))
			}
		}
	}
	return urls, texts
}

func imageArgs(server *imageRecordingServer, apiKeyFile string, extra ...string) []string {
	args := []string{
		"-provider", "openai",
		"-provider-url", server.URL + "/v1",
		"-api-key-file", apiKeyFile,
		"-model", "gpt-test",
	}
	return append(args, extra...)
}

func TestImageFlagSendsDataURLAndSessionReplaysIt(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := newImageRecordingServer(t)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	sessionsDir := t.TempDir()
	imagePath := writeIntegrationImage(t, "shot.png")
	wantURL := core.ImageDataURL("image/png", integrationPNG)

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		imageArgs(server, apiKeyFile, "-sessions-dir", sessionsDir, "-image", imagePath, "-image-detail", "low"),
		"What is this?")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if stdout != "a picture" {
		t.Fatalf("stdout = %q, want the model text alone", stdout)
	}

	urls, texts := imageParts(t, server.body(t, 0))
	if len(urls) != 1 || urls[0] != wantURL {
		t.Fatalf("first request image parts = %v, want the data URL once", urls)
	}
	if len(texts) != 1 || texts[0] != "What is this?" {
		t.Fatalf("first request text parts = %v, want the prompt", texts)
	}
	if !strings.Contains(string(server.bodies[0]), `"detail":"low"`) {
		t.Fatalf("first request lacks the detail hint:\n%s", server.bodies[0])
	}

	listing, stderr, err := runLmcCommand(t, lmcBin, []string{"-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("show-sessions failed: %v\nstderr:\n%s", err, stderr)
	}
	sessionID := extractFirstSessionID(listing)
	if sessionID == "" {
		t.Fatalf("no session id in listing:\n%s", listing)
	}

	shown, stderr, err := runLmcCommand(t, lmcBin, []string{"-show", sessionID, "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("show failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(shown, "What is this?\n[image: shot.png (image/png, 12 bytes)]\n") {
		t.Fatalf("-show output = %q, want the prompt followed by the image line", shown)
	}
	if strings.Contains(shown, "base64,") {
		t.Fatal("-show printed image bytes")
	}

	stdout, stderr, err = runLmcCommand(t, lmcBin,
		imageArgs(server, apiKeyFile, "-sessions-dir", sessionsDir, "-resume", sessionID),
		"And in more detail?")
	if err != nil {
		t.Fatalf("resume failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	resumed := server.body(t, 1)
	urls, texts = imageParts(t, resumed)
	if len(urls) != 0 || len(texts) != 1 || texts[0] != "And in more detail?" {
		t.Fatalf("resumed user turn = images %v, texts %v; want the new prompt alone", urls, texts)
	}
	if got := strings.Count(string(server.bodies[1]), wantURL); got != 1 {
		t.Fatalf("resumed request carries the image %d times, want once from the earlier turn", got)
	}
}

func TestImageFlagAcceptsImageOnlyInput(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := newImageRecordingServer(t)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	imagePath := writeIntegrationImage(t, "only.png")

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		imageArgs(server, apiKeyFile, "-no-session", "-image", imagePath), "")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	urls, texts := imageParts(t, server.body(t, 0))
	if len(urls) != 1 || len(texts) != 0 {
		t.Fatalf("user turn = images %v, texts %v; want the image alone", urls, texts)
	}
}

func TestImageFlagRejectsRegenerationBranch(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := newImageRecordingServer(t)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	sessionsDir := t.TempDir()
	imagePath := writeIntegrationImage(t, "shot.png")

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		imageArgs(server, apiKeyFile, "-sessions-dir", sessionsDir), "first question")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	listing, _, err := runLmcCommand(t, lmcBin, []string{"-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("show-sessions failed: %v", err)
	}
	sessionID := extractFirstSessionID(listing)
	assistantID := findMessageIDByRole(t, filepath.Join(sessionsDir, sessionID), "assistant")

	stdout, stderr, err = runLmcCommand(t, lmcBin,
		imageArgs(server, apiKeyFile, "-sessions-dir", sessionsDir, "-branch", sessionID+"/"+assistantID, "-image", imagePath),
		"")
	if err == nil {
		t.Fatalf("lmc succeeded regenerating with -image\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "-image needs a user turn") {
		t.Fatalf("stderr = %q, want the -image regeneration error", stderr)
	}
	if len(server.bodies) != 1 {
		t.Fatalf("server saw %d requests, want only the first turn", len(server.bodies))
	}
}

func TestImageFlagRejectsUnreadableFileBeforeSending(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := newImageRecordingServer(t)
	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")

	_, stderr, err := runLmcCommand(t, lmcBin,
		imageArgs(server, apiKeyFile, "-no-session", "-image", filepath.Join(t.TempDir(), "missing.png")), "look")
	if err == nil {
		t.Fatal("lmc succeeded with a missing image")
	}
	if !strings.Contains(stderr, "read image") {
		t.Fatalf("stderr = %q, want the read error", stderr)
	}
	if len(server.bodies) != 0 {
		t.Fatalf("server saw %d requests, want none", len(server.bodies))
	}
}

func findMessageIDByRole(t *testing.T, sessionPath, role string) string {
	t.Helper()
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.Contains(strings.TrimSuffix(name, ".json"), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionPath, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var meta struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.Role == role {
			return strings.TrimSuffix(name, ".json")
		}
	}
	t.Fatalf("no %s message in %s", role, sessionPath)
	return ""
}
