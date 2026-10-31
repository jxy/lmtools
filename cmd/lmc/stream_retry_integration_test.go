//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// argoStreamFailure is what Argo sent on 2026-10-29 in place of an answer
// to a tool round: HTTP 200, then this one event.
const argoStreamFailure = `data: {"error": {"message": "Streaming response failed", "type": "internal_error", "code": "internal_error"}}` + "\n\n"

// A stream that fails before any content is sent again, for the first
// request of a turn and for a tool round, and the failures are logged: an
// error event, or a stream that ends with nothing in it.
func TestCLIRetriesStreamFailingBeforeContent(t *testing.T) {
	arguments := `{"command":["/usr/bin/printf","TOOL_OK"]}`
	call := map[string]interface{}{"index": 0, "id": "call_retry", "type": "function", "function": map[string]interface{}{"name": "universal_command", "arguments": arguments}}
	chunk, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"index": 0, "delta": map[string]interface{}{"role": "assistant", "tool_calls": []interface{}{call}}, "finish_reason": "tool_calls"}}})
	toolCall := "data: " + string(chunk) + "\n\ndata: [DONE]\n\n"
	answer := `data: {"choices":[{"index":0,"delta":{"content":"DONE_OK"},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	for _, tt := range []struct {
		name    string
		failure string
		logged  string
	}{
		{"error event", argoStreamFailure, "error event before any content (as HTTP 500): Streaming response failed (type: internal_error, code: internal_error)"},
		{"empty stream", "", "stream ended before any content: EOF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := completionServer(t,
				completionReply{[]byte(tt.failure), true},
				completionReply{[]byte(toolCall), true},
				completionReply{[]byte(tt.failure), true},
				completionReply{[]byte(answer), true},
			)
			logDir := t.TempDir()
			stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, true, "-log-dir", logDir, "-retries", "1", "-tool", "-tool-auto-approve"), "run the tool")
			if err != nil || stdout != "DONE_OK" || strings.Contains(stderr, "Streaming response failed") {
				t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
			}
			reqs := requests()
			if len(reqs) != 4 || reqs[0] != reqs[1] || reqs[2] != reqs[3] || !strings.Contains(reqs[2], "TOOL_OK") {
				t.Fatalf("requests=%q", reqs)
			}
			logs, err := filepath.Glob(filepath.Join(logDir, "*_lmc_*.log"))
			if err != nil || len(logs) != 1 {
				t.Fatalf("logs=%v err=%v", logs, err)
			}
			data, err := os.ReadFile(logs[0])
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(data), tt.logged); got != 2 {
				t.Fatalf("logged %d stream failures, want 2:\n%s", got, data)
			}
		})
	}
}

// With no retries left, the stream reaches the parser, which reports the
// error as it did before streams were checked.
func TestCLIReportsStreamFailureWithoutRetries(t *testing.T) {
	server, requests := completionServer(t, completionReply{[]byte(argoStreamFailure), true})
	stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, true, "-log-dir", t.TempDir()), "hello")
	assertCompletionError(t, err, stderr, "upstream stream error: Streaming response failed (type: internal_error, code: internal_error)")
	if stdout != "" || len(requests()) != 1 {
		t.Fatalf("stdout=%q requests=%d", stdout, len(requests()))
	}
}

// At DEBUG, the log keeps a stream that was retried as it came, ahead of
// the error it was retried for: its comments and the provider's request ID
// are in no formatted message.
func TestCLILogsTheStreamItRetriesAtDebug(t *testing.T) {
	failure := ": upstream busy\n\n" + `data: {"error": {"message": "Streaming response failed", "type": "internal_error", "code": "internal_error", "request_id": "req_retried"}}` + "\n\n"
	answer := `data: {"choices":[{"index":0,"delta":{"content":"DONE_OK"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	server, _ := completionServer(t, completionReply{[]byte(failure), true}, completionReply{[]byte(answer), true})
	logDir := t.TempDir()
	stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, true, "-log-dir", logDir, "-log-level", "DEBUG", "-retries", "1"), "hello")
	if err != nil || stdout != "DONE_OK" {
		t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
	}
	logs, err := filepath.Glob(filepath.Join(logDir, "*_lmc_*.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%v err=%v", logs, err)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	head := strings.Index(log, "WIRE BACKEND RESPONSE HEADERS (attempt 1/2, retried):\nHTTP/1.1 200 OK\r\n")
	body := strings.Index(log, "WIRE BACKEND RESPONSE BODY (attempt 1/2, retried):\n"+failure)
	failed := strings.Index(log, "error event before any content (as HTTP 500)")
	if head < 0 || body < head || failed < body {
		t.Fatalf("want the retried stream's head and body ahead of its error:\n%s", log)
	}
}
