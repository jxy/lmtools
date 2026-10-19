//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/core"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIThinkingContentHistoryAndThinking(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, show := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream%v/show%v", stream, show), func(t *testing.T) {
				dir := t.TempDir()
				file := "openai.response.json"
				if stream {
					file = "openai-stream.stream.txt"
				}
				body := completionFixture(t, "response-openai-thinking-content", file)
				server, requests := completionServer(t, completionReply{body, stream}, completionReply{body, stream}, completionReply{body, stream})
				args := completionArgs(t, server.URL, dir, false, stream, "-log-dir", t.TempDir(), "-repl", fmt.Sprintf("-show-thinking=%v", show))
				stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), args, "first\nsecond\n")
				if err != nil || stdout != "AUDIT_OK\n\nAUDIT_OK\n" {
					t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
				}
				if strings.Contains(stderr, "--- thinking ---") != show {
					t.Fatalf("thinking display=%s", stderr)
				}
				sess, messages := completionSession(t, dir)
				var thoughts int
				for _, msg := range messages {
					for _, b := range msg.Blocks {
						if r, ok := b.(core.ReasoningBlock); ok && r.Type == "thinking" && r.Text != "" {
							thoughts++
						}
					}
				}
				if thoughts != 2 {
					t.Fatalf("saved thoughts=%d", thoughts)
				}
				args = completionArgs(t, server.URL, dir, false, stream, "-log-dir", t.TempDir(), "-resume", sess.Path)
				stdout, stderr, err = runLmcCommand(t, getLmcBinary(t), args, "third")
				if err != nil || stdout != "AUDIT_OK" {
					t.Fatalf("resume stdout=%q stderr=%s err=%v", stdout, stderr, err)
				}
				reqs := requests()
				if len(reqs) != 3 {
					t.Fatal(reqs)
				}
				for i := 1; i < 3; i++ {
					var req struct {
						Messages []struct {
							Role             string
							Content          json.RawMessage
							ReasoningContent string `json:"reasoning_content"`
						}
					}
					if err := json.Unmarshal([]byte(reqs[i]), &req); err != nil {
						t.Fatal(err)
					}
					var assistants int
					for _, msg := range req.Messages {
						if msg.Role == "assistant" {
							assistants++
							if msg.ReasoningContent != "" || !strings.Contains(string(msg.Content), `"thinking"`) || !strings.Contains(string(msg.Content), "AUDIT_OK") {
								t.Fatalf("history=%s", reqs[i])
							}
						}
					}
					if assistants != i {
						t.Fatalf("assistant messages=%d want %d", assistants, i)
					}
				}
			})
		}
	}
}

func TestCLIInvalidStreamPreservesPartialWithoutRunningTools(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	arguments, _ := json.Marshal(map[string]interface{}{"command": []string{"/usr/bin/touch", marker}})
	chunk, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "partial", "tool_calls": []interface{}{map[string]interface{}{"index": 0, "id": "call1", "type": "function", "function": map[string]interface{}{"name": "universal_command", "arguments": string(arguments)}}}}}}})
	stream := "data: " + string(chunk) + "\n\ndata: {bad json}\n\ndata: [DONE]\n\n"
	server, requests := completionServer(t, completionReply{[]byte(stream), true})
	stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, dir, false, true, "-log-dir", t.TempDir(), "-tool", "-tool-auto-approve"), "test")
	assertCompletionError(t, err, stderr, "invalid stream event")
	if stdout != "partial" || len(requests()) != 1 {
		t.Fatalf("stdout=%q requests=%v", stdout, requests())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command executed: %v", err)
	}
	_, messages := completionSession(t, dir)
	last := messages[len(messages)-1]
	if last.Role != "assistant" || len(last.Blocks) != 1 || last.Blocks[0].(core.TextBlock).Text != "partial" {
		t.Fatalf("saved=%+v", last)
	}
}

func TestCLIPipedREPLFailureExit(t *testing.T) {
	for _, failAt := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				_, _ = io.Copy(io.Discard, r.Body)
				current := attempts
				attempts++
				w.Header().Set("Content-Type", "application/json")
				if current == failAt {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"message":"audit rate limit explanation"}`)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, false, "-log-dir", t.TempDir(), "-repl"), "first\nsecond\n")
			if failAt < 0 {
				if err != nil || stdout != "OK\n\nOK\n" {
					t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
				}
			} else {
				if err == nil || stdout != "OK\n" || !strings.Contains(stderr, "1 REPL turn(s) failed") || !strings.Contains(stderr, "audit rate limit explanation") || strings.Contains(stderr, "max retries exceeded") {
					t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
				}
			}
		})
	}
}

func TestCLIThinkingContentToolFollowupReplaysThinking(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			argsJSON := `{"command":["/usr/bin/printf","TOOL_OK"]}`
			reasoning := []interface{}{map[string]interface{}{"type": "thinking", "thinking": []interface{}{map[string]interface{}{"type": "text", "text": "Use the tool."}}, "closed": true}}
			call := map[string]interface{}{"index": 0, "id": "call_thinking", "type": "function", "function": map[string]interface{}{"name": "universal_command", "arguments": argsJSON}}
			message := map[string]interface{}{"role": "assistant", "content": reasoning, "tool_calls": []interface{}{call}}
			field := "message"
			if stream {
				field = "delta"
			}
			payload, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{field: message, "finish_reason": "tool_calls"}}})
			if stream {
				payload = []byte("data: " + string(payload) + "\n\ndata: [DONE]\n\n")
			}
			file := "openai.response.json"
			if stream {
				file = "openai-stream.stream.txt"
			}
			server, requests := completionServer(t, completionReply{payload, stream}, completionReply{completionFixture(t, "response-openai-thinking-content", file), stream})
			stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, stream, "-log-dir", t.TempDir(), "-tool", "-tool-auto-approve", "-show-thinking"), "run tool")
			if err != nil || stdout != "AUDIT_OK" {
				t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
			}
			reqs := requests()
			if len(reqs) != 2 || !strings.Contains(reqs[1], `"thinking"`) || !strings.Contains(reqs[1], "Use the tool.") || !strings.Contains(reqs[1], "TOOL_OK") || !strings.Contains(reqs[1], "call_thinking") {
				t.Fatalf("requests=%v", reqs)
			}
			if strings.Count(stderr, "--- thinking ---") != 2 {
				t.Fatalf("thinking must appear once per response: %s", stderr)
			}
		})
	}
}
