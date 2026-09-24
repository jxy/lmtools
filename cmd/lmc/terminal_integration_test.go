//go:build integration && (darwin || freebsd || linux)

package main

import (
	"encoding/json"
	"io"
	"lmtools/internal/ptytest"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// heldToolProvider answers the first chat request with a universal_command
// call, but only once the test releases it, so the test can type while the
// model is generating. Every later request gets a plain answer.
type heldToolProvider struct {
	*httptest.Server
	requested chan struct{}
	release   chan struct{}
	mu        sync.Mutex
	bodies    [][]byte
}

func newHeldToolProvider(t *testing.T, arguments string) *heldToolProvider {
	t.Helper()
	p := &heldToolProvider{requested: make(chan struct{}), release: make(chan struct{})}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		first := len(p.bodies) == 1
		p.mu.Unlock()

		message := map[string]interface{}{"role": "assistant", "content": "done"}
		finish := "stop"
		if first {
			close(p.requested)
			select {
			case <-p.release:
			case <-r.Context().Done():
				return
			}
			message = map[string]interface{}{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]interface{}{{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]interface{}{"name": "universal_command", "arguments": arguments},
				}},
			}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-held",
			"object":  "chat.completion",
			"created": 1,
			"model":   "gpt-test",
			"choices": []map[string]interface{}{{"index": 0, "message": message, "finish_reason": finish}},
		})
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *heldToolProvider) body(index int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index >= len(p.bodies) {
		return ""
	}
	return string(p.bodies[index])
}

// A single run on a terminal reads its prompt through the input owner up to
// Ctrl-D. When the model asks for a command, the approval question drops the
// line typed while the model was generating, says so above the question, and
// takes only the answer typed after the question appeared.
func TestTerminalSingleRunDropsInputTypedDuringGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		result string
	}{
		{name: "then no", answer: "n\n", result: "user denied permission"},
		{name: "then yes", answer: "y\n", result: "from-command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lmcBin := getLmcBinary(t)
			provider := newHeldToolProvider(t, `{"command":["printf","%s-%s","from","command"]}`)
			pty, err := ptytest.Open()
			if err != nil {
				t.Fatalf("ptytest.Open() error = %v", err)
			}
			defer pty.Close()
			screen := pty.Screen()

			cmd := exec.Command(lmcBin,
				"-provider", "openai",
				"-provider-url", provider.URL+"/v1",
				"-api-key-file", writeTestAPIKeyFile(t, "test-key"),
				"-model", "gpt-test",
				"-no-session",
				"-tool",
				"-log-dir", t.TempDir(),
			)
			cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
			if err := pty.Start(cmd); err != nil {
				t.Fatalf("start lmc: %v", err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					<-exited
				}
			}()

			typeKeys(t, pty, "run the command\n"+ptytest.EndOfInput)
			select {
			case <-provider.requested:
			case <-time.After(20 * time.Second):
				t.Fatalf("lmc sent no request; the terminal showed %q", screen.String())
			}
			typeEchoed(t, pty, screen, "yes\n")
			close(provider.release)
			for _, want := range []string{discardedInputNotice, "Allow execution? [y/N]: "} {
				if err := screen.Expect(want, 20*time.Second); err != nil {
					t.Fatal(err)
				}
			}
			typeKeys(t, pty, tc.answer)
			if err := screen.Expect("done", 20*time.Second); err != nil {
				t.Fatal(err)
			}

			select {
			case err := <-exited:
				waited = true
				if err != nil {
					t.Fatalf("lmc exited with %v; the terminal showed %q", err, screen.String())
				}
			case <-time.After(20 * time.Second):
				t.Fatalf("lmc did not exit; the terminal showed %q", screen.String())
			}
			if body := provider.body(1); !strings.Contains(body, tc.result) {
				t.Fatalf("the follow-up request does not carry %q: %s", tc.result, body)
			}
		})
	}
}
