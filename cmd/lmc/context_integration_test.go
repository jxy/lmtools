//go:build integration

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Exercise the real subprocess selection, session binding, checkpoint writer,
// and pinned local history through ordinary lmc entry points.
func TestContextNotebookResumeAndHistory(t *testing.T) {
	const expectedHandoff = "WRITER-DRAFT"
	bin := t.TempDir()
	lmc := filepath.Join(bin, "lmc")
	raw, err := os.ReadFile(getLmcBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(lmc, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	// go test runs this package with cmd/lmc as its working directory.
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "notebook"), "../notebook")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build notebook: %v %s", err, out)
	}
	dir := t.TempDir()
	notes := filepath.Join(dir, "notes")
	sessions := filepath.Join(dir, "sessions")
	key := writeTestAPIKeyFile(t, "mock")
	rules := filepath.Join(dir, "rules")
	if err = os.WriteFile(rules, []byte("{\"mcp\":\"history\"}\n{\"mcp\":\"notebook\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls int
	var writer, search, notebookWrites, notebookVersions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var input struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
			Tools []json.RawMessage
		}
		if err = json.Unmarshal(body, &input); err != nil {
			t.Error(err)
			return
		}
		system := string(input.Messages[0].Content)
		last := input.Messages[len(input.Messages)-1]
		message := map[string]any{"role": "assistant", "content": "first answer"}
		finish := "stop"
		switch {
		case strings.Contains(string(last.Content), "Prepare a notebook that lets a colleague reconstruct"):
			notebookVersions++
			message["content"] = "versions covered"
		case strings.Contains(string(last.Content), "Prepare this project's notebook"):
			notebookWrites++
			message["content"] = "notes updated"
		case strings.Contains(system, "Write a handoff"):
			writer++
			message["content"] = "WRITER-DRAFT. Source recorded."
			if len(input.Tools) != 0 {
				t.Error("writer received tools")
			}
		case strings.Contains(system, "independent editor"):
			t.Error("prose handoff requested a second writer")
		case strings.Contains(string(last.Content), "FIND-THE-ORIGINAL"):
			if !strings.Contains(string(body), expectedHandoff) || strings.Contains(string(body), strings.Repeat("padding ", 350)) {
				t.Error("resume did not use the checkpoint")
			}
			if !strings.Contains(string(body), "mcp__notebook__put") || strings.Contains(string(body), "Run one argv command directly") {
				t.Error("incorrect tool selection")
			}
			message["content"] = nil
			message["tool_calls"] = []any{map[string]any{"id": "history-call", "type": "function", "function": map[string]any{"name": "mcp__history__search", "arguments": `{"query":"lost-id-738","kind":"user"}`}}}
			finish = "tool_calls"
		case last.Role == "tool":
			search++
			if !strings.Contains(string(last.Content), "lost-id-738") || !strings.Contains(string(last.Content), "offset") {
				t.Error("lost the original evidence")
			}
			message["content"] = "found original"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "mock", "model": "gpt-test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
	}))
	defer server.Close()
	base := []string{"-provider", "openai", "-model", "gpt-test", "-provider-url", server.URL + "/v1", "-api-key-file", key, "-sessions-dir", sessions, "-tool-whitelist", rules, "-tool-non-interactive"}
	start := append(append([]string{}, base...), "-notebook", notes, "-context-window", "65536", "-context-trigger", "4200")
	input := strings.Repeat("padding ", 800) + "lost-id-738" + strings.Repeat("padding ", 800)
	std, diag, err := runLmcCommand(t, lmc, start, input)
	if err != nil || strings.TrimSpace(std) != "first answer" {
		t.Fatalf("start %q %v %s", std, err, diag)
	}
	resume := append(append([]string{}, base...), "-resume", "0001")
	mu.Lock()
	before := calls
	mu.Unlock()
	_, diag, err = runLmcCommand(t, lmc, append(append([]string{}, resume...), "-print-curl"), "FIND-THE-ORIGINAL")
	if err == nil || !strings.Contains(diag, "preview requires") {
		t.Fatalf("preview %v: %s", err, diag)
	}
	mu.Lock()
	if calls != before {
		t.Error("preview sent a request")
	}
	mu.Unlock()
	std, diag, err = runLmcCommand(t, lmc, resume, "FIND-THE-ORIGINAL")
	if err != nil || !strings.Contains(std, "found original") || strings.Contains(std, expectedHandoff) {
		t.Fatalf("resume %q %v %s", std, err, diag)
	}
	mu.Lock()
	if writer != 1 || search != 1 {
		t.Errorf("writer=%d search=%d", writer, search)
	}
	mu.Unlock()
	std, _, err = runLmcCommand(t, lmc, []string{"-show", "0001", "-sessions-dir", sessions}, "")
	if err != nil || !strings.Contains(std, strings.TrimSpace(input)) {
		t.Fatal("compaction changed the saved transcript", err)
	}
	mu.Lock()
	before = calls
	mu.Unlock()
	std, diag, err = runLmcCommand(t, lmc, append(append([]string{}, resume...), "-context-trigger", "40000", "-print-curl"), "preview existing checkpoint")
	if err != nil || !strings.Contains(std, expectedHandoff) || strings.Contains(std, strings.Repeat("padding ", 350)) {
		t.Fatalf("persisted projection missing from preview: %v %s", err, diag)
	}
	mu.Lock()
	if calls != before {
		t.Error("previewing a committed checkpoint contacted the provider")
	}
	mu.Unlock()
	repl := append(append([]string{}, base...), "-repl", "-no-stream", "-notebook", notes)
	// A handoff that cannot start fails a piped REPL, as a failed turn does.
	std, diag, err = runLmcCommand(t, lmc, repl, "/handoff\n/handoff verify\n/handoff versions\n/handoff invalid\n/exit\n")
	if err == nil || !strings.Contains(diag, "2 REPL turn(s) failed") || !strings.Contains(std, "notes updated") || !strings.Contains(std, "versions covered") || !strings.Contains(diag, "Use /handoff") {
		t.Fatalf("notebook commands: %q %v %s", std, err, diag)
	}
	std, diag, err = runLmcCommand(t, lmc, append(repl, "-tool-include", "notebook/read"), "/handoff\n/exit\n")
	if err == nil || !strings.Contains(diag, "1 REPL turn(s) failed") || strings.TrimSpace(std) != "" || !strings.Contains(diag, "requires the notebook put tool") {
		t.Fatalf("read-only notebook: %q %v %s", std, err, diag)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls-before != 2 || notebookWrites != 1 || notebookVersions != 1 {
		t.Fatalf("handoff made %d calls, writes %d versions %d", calls-before, notebookWrites, notebookVersions)
	}
}
