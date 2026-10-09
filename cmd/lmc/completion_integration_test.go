//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type completionReply struct {
	body   []byte
	stream bool
}

func completionFixture(t *testing.T, caseID, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "api-fixtures", "cases", caseID, "captures", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// completionServer serves fixed offline responses and records the requests
// made by separate CLI invocations, including a later session resume.
func completionServer(t *testing.T, replies ...completionReply) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		index := len(requests)
		requests = append(requests, string(body))
		mu.Unlock()
		if index >= len(replies) {
			http.Error(w, "unexpected extra inference", http.StatusBadRequest)
			return
		}
		contentType := "application/json"
		if replies[index].stream {
			contentType = "text/event-stream"
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(replies[index].body)
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func completionArgs(t *testing.T, url, sessionsDir string, responses, stream bool, extra ...string) []string {
	t.Helper()
	args := []string{
		"-provider", "openai", "-provider-url", url + "/v1",
		"-api-key-file", writeTestAPIKeyFile(t, "test-key"), "-model", "gemma-test",
		"-retries", "0", "-sessions-dir", sessionsDir,
		fmt.Sprintf("-stream=%t", stream),
	}
	if responses {
		args = append(args, "-openai-responses")
	}
	return append(args, extra...)
}

func completionSession(t *testing.T, dir string) (*session.Session, []core.TypedMessage) {
	t.Helper()
	useTempSessionsDir(t, dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			roots = append(roots, filepath.Join(dir, entry.Name()))
		}
	}
	if len(roots) != 1 {
		t.Fatalf("session roots = %q, want one", roots)
	}
	sess, err := session.OpenSession(context.Background(), roots[0])
	if err != nil {
		t.Fatal(err)
	}
	messages, err := session.BuildMessagesForSession(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	return sess, messages
}

func assertCompletionError(t *testing.T, err error, stderr, want string) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want status 1; stderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, want) || strings.Count(stderr, "Error:") != 1 {
		t.Fatalf("stderr = %q, want %q and one Error: prefix", stderr, want)
	}
}

func TestCLIIncompleteCapturedResponsesPersistPartialContent(t *testing.T) {
	for _, tc := range []struct {
		name, caseID, file, text, thinking string
		responses, stream                  bool
	}{
		{"chat buffered", "response-openai-llamacpp-length", "openai.response.json", "", "The user is asking for", false, false},
		{"chat streamed", "response-openai-llamacpp-length", "openai-stream.stream.txt", "", "The user wants to know", false, true},
		{"responses buffered", "response-openai-responses-incomplete", "openai-responses.response.json", "Partial answer", "", true, false},
		{"responses streamed", "openai-responses-incomplete-stream", "openai-responses-stream.stream.txt", "API streaming clients must handle terminal incomplete events because streams can end for", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dir := t.TempDir()
			server, requests := completionServer(t, completionReply{completionFixture(t, tc.caseID, tc.file), tc.stream})
			stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, dir, tc.responses, tc.stream, "-max-tokens", "8", "-show-thinking"), "Answer the question")
			assertCompletionError(t, err, stderr, "output token limit reached")
			if stdout != tc.text {
				t.Fatalf("stdout = %q, want retained partial answer %q", stdout, tc.text)
			}
			_, messages := completionSession(t, dir)
			last := messages[len(messages)-1]
			if last.Role != "assistant" {
				t.Fatalf("last saved message = %#v, want the partial assistant response", last)
			}
			var text, thinking string
			for _, block := range last.Blocks {
				switch block := block.(type) {
				case core.TextBlock:
					text += block.Text
				case core.ReasoningBlock:
					thinking += core.ReasoningTextForDisplay(block)
				}
			}
			if text != tc.text || (tc.thinking != "" && thinking != tc.thinking) {
				t.Fatalf("saved text = %q, thinking = %q; want %q, %q", text, thinking, tc.text, tc.thinking)
			}
			if tc.thinking != "" && (!strings.Contains(stderr, tc.thinking) || !strings.Contains(stderr, "--- end thinking (incomplete) ---")) {
				t.Fatalf("partial thinking was not shown as incomplete:\n%s", stderr)
			}
			if len(requests()) != 1 {
				t.Fatalf("%d inferences, want one", len(requests()))
			}
		})
	}
}

// Dynamic command paths cannot be captured in the corpus. The test keeps the
// captured API envelope and substitutes the message, command, and stop status.
func completionWire(t *testing.T, responses, stream bool, text, thinking, finish string, command []string) completionReply {
	t.Helper()
	caseID, file := "response-openai-tool-call", "openai.response.json"
	if responses {
		caseID, file = "response-llamacpp-responses-reasoning-tool", "openai-responses.response.json"
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal(completionFixture(t, caseID, file), &envelope); err != nil {
		t.Fatal(err)
	}
	marshal := func(value interface{}) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	args := string(marshal(map[string]interface{}{"command": command}))
	var wire bytes.Buffer
	event := func(kind string, fields map[string]interface{}) {
		fields["type"] = kind
		fmt.Fprintf(&wire, "event: %s\ndata: %s\n\n", kind, marshal(fields))
	}
	if !responses {
		message := map[string]interface{}{"role": "assistant", "content": text}
		if thinking != "" {
			message["reasoning_content"] = thinking
		}
		if command != nil {
			message["tool_calls"] = []interface{}{map[string]interface{}{
				"index": 0, "id": "call_completion", "type": "function",
				"function": map[string]interface{}{"name": "universal_command", "arguments": args},
			}}
		}
		envelope["choices"] = []interface{}{map[string]interface{}{"index": 0, "message": message, "finish_reason": finish}}
		if stream {
			fmt.Fprintf(&wire, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", marshal(map[string]interface{}{
				"choices": []interface{}{map[string]interface{}{"index": 0, "delta": message}},
			}), marshal(map[string]interface{}{
				"choices": []interface{}{map[string]interface{}{"index": 0, "delta": map[string]interface{}{}, "finish_reason": finish}},
			}))
		}
	} else {
		var items []interface{}
		if thinking != "" {
			items = append(items, map[string]interface{}{"id": "rs_completion", "type": "reasoning", "content": []interface{}{map[string]interface{}{"type": "reasoning_text", "text": thinking}}})
		}
		if command != nil {
			items = append(items, map[string]interface{}{"id": "fc_completion", "type": "function_call", "call_id": "call_completion", "name": "universal_command", "arguments": args, "status": "completed"})
		}
		if text != "" {
			items = append(items, map[string]interface{}{"id": "msg_completion", "type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": text}}})
		}
		envelope["output"] = items
		envelope["status"] = "completed"
		terminal := "response.completed"
		if finish == "length" {
			envelope["status"] = "incomplete"
			envelope["incomplete_details"] = map[string]interface{}{"reason": "max_output_tokens"}
			terminal = "response.incomplete"
		}
		if stream {
			for _, raw := range items {
				item := raw.(map[string]interface{})
				// Added items precede their deltas. Supplying the final text
				// here and then emitting it as a delta would append it twice.
				added := make(map[string]interface{}, len(item))
				for key, value := range item {
					added[key] = value
				}
				added["status"] = "in_progress"
				if item["type"] == "function_call" {
					added["arguments"] = ""
				} else {
					added["content"] = []interface{}{}
				}
				event("response.output_item.added", map[string]interface{}{"item": added})
				switch item["type"] {
				case "reasoning":
					event("response.reasoning_text.delta", map[string]interface{}{"item_id": item["id"], "content_index": 0, "delta": thinking})
				case "message":
					event("response.output_text.delta", map[string]interface{}{"item_id": item["id"], "content_index": 0, "delta": text})
				case "function_call":
					event("response.function_call_arguments.delta", map[string]interface{}{"item_id": item["id"], "delta": args})
				}
				event("response.output_item.done", map[string]interface{}{"item": item})
			}
			event(terminal, map[string]interface{}{"response": envelope})
		}
	}
	if !stream {
		wire.Write(marshal(envelope))
	}
	return completionReply{body: wire.Bytes(), stream: stream}
}

func TestCLIIncompleteToolsNeverExecuteOnResume(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("responses=%t/stream=%t", responses, stream), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				dir := t.TempDir()
				marker := filepath.Join(t.TempDir(), "must-not-run")
				command := []string{"sh", "-c", `printf run >> "$1"`, "sh", marker}
				server, requests := completionServer(t,
					completionWire(t, responses, stream, "partial answer", "partial reasoning", "length", command),
					completionWire(t, responses, stream, "recovered", "", "stop", nil))
				args := completionArgs(t, server.URL, dir, responses, stream, "-tool", "-tool-auto-approve", "-show-thinking")
				stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), args, "Run the tool")
				assertCompletionError(t, err, stderr, "partial answer retained")
				if stdout != "partial answer" || len(requests()) != 1 {
					t.Fatalf("stdout=%q, inferences=%d, want one partial answer without tool follow-up", stdout, len(requests()))
				}
				sess, messages := completionSession(t, dir)
				var savedThinking string
				for _, message := range messages {
					for _, block := range message.Blocks {
						switch block := block.(type) {
						case core.ToolUseBlock, core.ToolResultBlock:
							t.Fatalf("incomplete tool was saved: %#v", block)
						case core.ReasoningBlock:
							savedThinking += core.ReasoningTextForDisplay(block)
						}
					}
				}
				if savedThinking != "partial reasoning" {
					t.Fatalf("saved thinking=%q, want partial reasoning retained", savedThinking)
				}
				pending, err := session.CheckForPendingToolCalls(context.Background(), sess.Path)
				if err != nil || len(pending) != 0 {
					t.Fatalf("pending=%v, err=%v; want no resumable incomplete tool", pending, err)
				}
				stdout, stderr, err = runLmcCommand(t, getLmcBinary(t), append(args, "-resume", session.GetSessionID(sess.Path)), "Continue")
				if err != nil || stdout != "recovered" || len(requests()) != 2 {
					t.Fatalf("resume stdout=%q, err=%v, inferences=%d; stderr:\n%s", stdout, err, len(requests()), stderr)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("incomplete command executed (marker stat=%v)", err)
				}
				if strings.Contains(requests()[1], "call_completion") {
					t.Fatalf("resume sent the incomplete invocation: %s", requests()[1])
				}
			})
		}
	}
}

func TestCLIToolFollowupCompletionPersistsWithoutRerunning(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, final := range []string{"length", "invalid JSON", "valid JSON"} {
				t.Run(fmt.Sprintf("responses=%t/stream=%t/final=%s", responses, stream, final), func(t *testing.T) {
					t.Setenv("HOME", t.TempDir())
					dir := t.TempDir()
					marker := filepath.Join(t.TempDir(), "runs")
					command := []string{"sh", "-c", `printf run >> "$1"; printf TOOL_RESULT`, "sh", marker}
					text, finish := "partial final", "length"
					extra := []string{"-tool", "-tool-auto-approve"}
					if final != "length" {
						finish = "stop"
						extra = append(extra, "-json")
					}
					if final == "valid JSON" {
						text = " {\n  \"answer\": 437\n}\n"
					}
					server, requests := completionServer(t,
						completionWire(t, responses, stream, "Running marker tool", "", "tool_calls", command),
						completionWire(t, responses, stream, text, "", finish, nil),
						completionWire(t, responses, stream, `{"answer":437}`, "", "stop", nil))
					args := completionArgs(t, server.URL, dir, responses, stream, extra...)
					stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), args, "Run the tool")
					if final == "valid JSON" {
						if err != nil {
							t.Fatalf("valid final JSON failed: %v; stderr:\n%s", err, stderr)
						}
					} else {
						want := "output token limit reached"
						if final == "invalid JSON" {
							want = "requested JSON"
						}
						assertCompletionError(t, err, stderr, want)
					}
					if stdout != "Running marker tool"+text || len(requests()) != 2 {
						t.Fatalf("stdout=%q, inferences=%d, want intermediate text followed by final bytes", stdout, len(requests()))
					}
					sess, messages := completionSession(t, dir)
					results := 0
					for _, message := range messages {
						for _, block := range message.Blocks {
							if result, ok := block.(core.ToolResultBlock); ok {
								results++
								if result.Content != "TOOL_RESULT" {
									t.Fatalf("saved tool result=%q", result.Content)
								}
							}
						}
					}
					if results != 1 || !strings.HasSuffix(strings.Join(lmcTypedTextLines(messages), "|"), "assistant:"+text) {
						t.Fatalf("saved results=%d, messages=%q, want one result and final answer", results, lmcTypedTextLines(messages))
					}
					pending, err := session.CheckForPendingToolCalls(context.Background(), sess.Path)
					if err != nil || len(pending) != 0 {
						t.Fatalf("pending=%v, err=%v, want none", pending, err)
					}
					stdout, stderr, err = runLmcCommand(t, getLmcBinary(t), append(args, "-resume", session.GetSessionID(sess.Path)), "Continue")
					if err != nil || stdout != `{"answer":437}` || len(requests()) != 3 {
						t.Fatalf("resume stdout=%q, err=%v, inferences=%d; stderr:\n%s", stdout, err, len(requests()), stderr)
					}
					resultField := `"content":"TOOL_RESULT"`
					if responses {
						resultField = `"output":"TOOL_RESULT"`
					}
					if strings.Count(requests()[2], resultField) != 1 {
						t.Fatalf("resume did not carry exactly one completed tool result: %s", requests()[2])
					}
					data, err := os.ReadFile(marker)
					if err != nil || string(data) != "run" {
						t.Fatalf("command marker=%q, err=%v; want exactly one execution", data, err)
					}
				})
			}
		}
	}
}

func TestCLIStructuredOutputChecksJSONAndPreservesBytes(t *testing.T) {
	for _, tc := range []struct {
		name, text, schema, wantError string
	}{
		{"JSON object", " {\n \"answer\": 437\n}\n", "", ""},
		{"fenced JSON", "```json\n{}\n```", "", "requested JSON"},
		{"JSON array", "[]", "", "not an object"},
		{"schema match", " {\n \"answer\": 437\n}\n", `{"type":"object","properties":{"answer":{"const":437}},"required":["answer"],"additionalProperties":false}`, ""},
		{"schema enforcement belongs to provider", `{"answer":438}`, `{"type":"object","properties":{"answer":{"const":437}},"required":["answer"],"additionalProperties":false}`, ""},
		{"schema invalid JSON", "```json\n{}\n```", `{"type":"object"}`, "requested JSON"},
	} {
		for _, responses := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/responses=%t/stream=%t", tc.name, responses, stream), func(t *testing.T) {
					t.Setenv("HOME", t.TempDir())
					dir := t.TempDir()
					server, _ := completionServer(t, completionWire(t, responses, stream, tc.text, "", "stop", nil))
					extra := []string{"-json"}
					if tc.schema != "" {
						path := filepath.Join(t.TempDir(), "schema.json")
						if err := os.WriteFile(path, []byte(tc.schema), 0o600); err != nil {
							t.Fatal(err)
						}
						extra = []string{"-json-schema", path}
					}
					stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, dir, responses, stream, extra...), "Return JSON")
					if tc.wantError != "" {
						assertCompletionError(t, err, stderr, tc.wantError)
					} else if err != nil {
						t.Fatalf("valid output failed: %v; stderr:\n%s", err, stderr)
					}
					if stdout != tc.text {
						t.Fatalf("stdout=%q, want byte-for-byte %q", stdout, tc.text)
					}
					_, messages := completionSession(t, dir)
					if !strings.HasSuffix(strings.Join(lmcTypedTextLines(messages), "|"), "assistant:"+tc.text) {
						t.Fatalf("response was not saved: %q", lmcTypedTextLines(messages))
					}
				})
			}
		}
	}
}

func TestCLIShowThinkingWithoutEffortUsesReturnedContent(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("responses=%t/stream=%t", responses, stream), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				caseID, file := "response-openai-llamacpp-reasoning", "openai.response.json"
				if stream {
					file = "openai-stream.stream.txt"
				}
				if responses {
					caseID, file = "response-llamacpp-responses-reasoning-answer", "openai-responses.response.json"
					if stream {
						file = "openai-responses-stream.stream.txt"
					}
				}
				server, requests := completionServer(t, completionReply{completionFixture(t, caseID, file), stream})
				stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), responses, stream, "-show-thinking"), "Calculate 19 times 23")
				if err != nil || stdout != "437" || !strings.Contains(stderr, "--- thinking ---") || strings.Contains(stderr, "-effort") || strings.Contains(stderr, "No visible thinking") {
					t.Fatalf("stdout=%q, err=%v, stderr:\n%s", stdout, err, stderr)
				}
				if len(requests()) != 1 || strings.Contains(requests()[0], `"effort"`) {
					t.Fatalf("show-thinking changed the request: %v", requests())
				}
			})
		}
	}
}

func TestCLIHelpAndErrorsHaveExpectedExitStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, flag := range []string{"-h", "-help"} {
		stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), []string{flag}, "")
		if err != nil || strings.Contains(stdout+stderr, "Error:") || !strings.Contains(stdout+stderr, "Usage:") {
			t.Fatalf("%s: stdout=%q, stderr=%q, err=%v", flag, stdout, stderr, err)
		}
	}
	_, stderr, err := runLmcCommand(t, getLmcBinary(t), []string{"-no-such-flag"}, "")
	assertCompletionError(t, err, stderr, "flag provided but not defined")
	path := filepath.Join(t.TempDir(), "bad.png")
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runLmcCommand(t, getLmcBinary(t), []string{"-image", path}, "Describe it")
	assertCompletionError(t, err, stderr, "read image")
}
