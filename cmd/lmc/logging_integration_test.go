//go:build integration

package main

import (
	"fmt"
	"io"
	"lmtools/internal/mockserver"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRequestFailureLoggedOnce(t *testing.T) {
	lmcBin := getLmcBinary(t)
	t.Setenv("HOME", t.TempDir())
	for _, repl := range []bool{false, true} {
		t.Run(fmt.Sprintf("repl=%v", repl), func(t *testing.T) {
			logDir := t.TempDir()
			args := []string{
				"-provider", "openai", "-provider-url", "unsupported://example.test/v1",
				"-model", "test-model", "-api-key-file", writeTestAPIKeyFile(t, "test-key"),
				"-sessions-dir", t.TempDir(), "-log-level", "ERROR", "-retries", "0",
			}
			if repl {
				args = append(args, "-repl")
			}
			stdout, stderr, err := runLmcCommand(t, lmcBin, args, "question\n", WithLogDir(logDir))
			if err == nil || stdout != "" || strings.Count(stderr, `unsupported protocol scheme "unsupported"`) != 1 {
				t.Fatalf("exit=%v stdout=%q stderr=%q", err, stdout, stderr)
			}
			logged := readProcessLog(t, logDir)
			if strings.Count(logged, "Request failed") != 1 || !strings.Contains(logged, "[ERROR]") ||
				!strings.Contains(logged, "[#1]") {
				t.Fatalf("missing request failure log: %q", logged)
			}
		})
	}
}

func TestLoggingIntegration(t *testing.T) {
	// Get lmc binary
	lmcBin := getLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Test 1: Embedding request with logging
	logDir := t.TempDir()
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-e", "-model", "v3large", "-provider-url", ms.URL(), "-sessions-dir", sessionsDir},
		"Test message for embedding", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run embedding: %v\nStderr: %s", err, stderr)
	}

	// Verify embedding output
	if !strings.Contains(stdout, "[") || !strings.Contains(stdout, "]") {
		t.Errorf("Expected embedding array output, got: %s", stdout)
	}

	// Check that log directory was created
	if _, err := os.Stat(logDir); err != nil {
		t.Fatalf("Log directory not created: %v", err)
	}

	// Find log files
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("No log files found in %s", logDir)
	}

	if !assertRecentLogFiles(t, logDir, "_embed_input", ".json") {
		t.Error("Request log file not found")
	}

	// Test 2: Chat request with logging (use same log dir)
	_, stderr, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", ms.URL(), "-sessions-dir", sessionsDir},
		"Test chat message", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run chat: %v\nStderr: %s", err, stderr)
	}

	// Check for new log files
	entries, err = os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("No log files found in %s after chat", logDir)
	}

	if !assertRecentLogFiles(t, logDir, "_chat_input", ".json") {
		t.Error("Chat request log file not found")
	}

	// Test 3: Verify log content format
	now := time.Now()
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "_lmc_") && strings.HasSuffix(entry.Name(), ".log") {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) < time.Minute {
				// Read the process log
				logPath := filepath.Join(logDir, entry.Name())
				content, err := os.ReadFile(logPath)
				if err != nil {
					t.Errorf("Failed to read log file %s: %v", entry.Name(), err)
					continue
				}

				logContent := string(content)
				if logContent == "" {
					// Log might be empty if nothing was logged
					continue
				}

				// Check for proper timestamp format
				lines := strings.Split(strings.TrimSpace(logContent), "\n")
				for _, line := range lines {
					if line == "" {
						continue
					}

					// Log format is now: [LEVEL] [RFC3339Nano timestamp] [component] message
					// Example: [INFO] [2025-08-13T03:42:20.524221955Z] [lmc] message

					// Should contain [INFO] or [WARN] or [DEBUG] or [ERROR]
					if !strings.Contains(line, "[INFO]") && !strings.Contains(line, "[WARN]") &&
						!strings.Contains(line, "[DEBUG]") && !strings.Contains(line, "[ERROR]") {
						t.Errorf("Log line missing level: %s", line)
						continue
					}

					// Check for timestamp in RFC3339Nano format between brackets
					// Pattern: [2025-08-13T03:42:20.524221955Z]
					if !strings.Contains(line, "[20") || !strings.Contains(line, "T") || !strings.Contains(line, "Z]") {
						t.Errorf("Log line missing or invalid timestamp format: %s", line)
					}
				}

				t.Logf("Process log validated: %s", entry.Name())
				break
			}
		}
	}
}

func TestStreamingLogging(t *testing.T) {
	// Get lmc binary
	lmcBin := getLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()
	logDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Test streaming request with custom log directory
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", streamOn, "-provider-url", ms.URL(), "-sessions-dir", sessionsDir},
		"Stream this message", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run streaming: %v\nStderr: %s", err, stderr)
	}

	// Verify output contains the response text
	if !strings.Contains(stdout, "This is a mock response") {
		t.Errorf("Expected streaming output to contain response text, got: %s", stdout)
	}

	// Check log files
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if !assertRecentLogFiles(t, logDir, "_stream_chat_input", ".json") {
		t.Error("Stream input log file not found")
	}
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("Stream output log file not found")
	} else {
		// Verify content
		for _, entry := range entries {
			if strings.Contains(entry.Name(), "_stream_chat_output") && strings.HasSuffix(entry.Name(), ".log") {
				logPath := filepath.Join(logDir, entry.Name())
				content, err := os.ReadFile(logPath)
				if err != nil {
					t.Errorf("Failed to read stream output log: %v", err)
				} else if len(content) == 0 {
					t.Error("Stream output log is empty")
				}
				break
			}
		}
	}

	// Verify session was created
	stdout, _, err = runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir},
		"")
	if err != nil {
		t.Fatalf("Failed to show sessions: %v", err)
	}

	// Should have at least one session
	if !strings.Contains(stdout, " • ") {
		t.Error("No session created for streaming request")
	}
}

func TestConcurrentLogging(t *testing.T) {
	// Get lmc binary
	lmcBin := getLmcBinary(t)

	// Create temporary directories
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	sessionsDir := t.TempDir()

	// Create mock server
	ms := mockserver.NewMockServer()
	defer ms.Close()

	// Create shared log directory for this test
	logDir := t.TempDir()

	// Run multiple argo processes concurrently with same log directory
	const numProcesses = 5
	done := make(chan error, numProcesses)

	for i := 0; i < numProcesses; i++ {
		go func(id int) {
			// Use specific log dir for concurrent test
			_, stderr, err := runLmcCommand(t, lmcBin,
				[]string{"-argo-user", "testuser", "-e", "-provider-url", ms.URL(), "-sessions-dir", sessionsDir},
				"Concurrent test message", WithLogDir(logDir))
			if err != nil {
				t.Logf("Process %d failed: %v, stderr: %s", id, err, stderr)
			}
			done <- err
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < numProcesses; i++ {
		if err := <-done; err != nil {
			t.Errorf("Process failed: %v", err)
		}
	}

	// Wait for log files to be written
	processLogs := waitForLogFiles(t, logDir, "_lmc_", numProcesses, time.Second)
	requestLogs := waitForLogFiles(t, logDir, "_embed_input_", numProcesses, time.Second)

	t.Logf("Found %d process logs and %d request logs in %s", processLogs, requestLogs, logDir)

	// We should have at least numProcesses of each type
	if processLogs < numProcesses {
		t.Errorf("Expected at least %d process logs, found %d", numProcesses, processLogs)
	}

	if requestLogs < numProcesses {
		t.Errorf("Expected at least %d request logs, found %d", numProcesses, requestLogs)
	}
}

// At DEBUG the log holds every request of a turn, a tool round's as well
// as the first: the request whole, the status and head of its response,
// and the response's body as it came, a stream byte for byte with its
// comments and carriage returns. The tool round's request body also still
// goes to its own file.
func TestCLILogsEveryRequestOfATurnAtDebug(t *testing.T) {
	toolCall := `data: {"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_log","type":"function","function":{"name":"universal_command","arguments":"{\"command\":[\"/usr/bin/printf\",\"TOOL_OK\"]}"}}]},"finish_reason":"tool_calls"}]}` + "\r\n\r\ndata: [DONE]\r\n\r\n"
	answer := ": keepalive\r\n\r\n" + `data: {"choices":[{"index":0,"delta":{"content":"DONE_OK"},"finish_reason":"stop"}]}` + "\r\n\r\ndata: [DONE]\r\n\r\n"
	server, requests := completionServer(t, completionReply{[]byte(toolCall), true}, completionReply{[]byte(answer), true})
	logDir := t.TempDir()
	stdout, stderr, err := runLmcCommand(t, getLmcBinary(t), completionArgs(t, server.URL, t.TempDir(), false, true, "-log-dir", logDir, "-log-level", "DEBUG", "-tool", "-tool-auto-approve"), "run the tool")
	if err != nil || stdout != "DONE_OK" {
		t.Fatalf("stdout=%q stderr=%s err=%v", stdout, stderr, err)
	}
	sent := requests()
	if len(sent) != 2 || !strings.Contains(sent[1], "TOOL_OK") {
		t.Fatalf("requests=%q", sent)
	}
	logged := readProcessLog(t, logDir)
	for _, record := range []string{
		"] WIRE BACKEND REQUEST:\n",
		"] Sending request to " + server.URL + "/v1/chat/completions\n",
		"] Response received | Status: 200 | ",
		"] WIRE BACKEND RESPONSE HEADERS:\nHTTP/1.1 200 OK\r\n",
	} {
		if got := strings.Count(logged, record); got != 2 {
			t.Fatalf("logged %q %d times, want once for each request:\n%s", record, got, logged)
		}
	}
	for _, body := range sent {
		if !strings.Contains(logged, "\r\n\r\n"+body+"\n") {
			t.Fatalf("no request logged with the body %q:\n%s", body, logged)
		}
	}
	if got := wireBodies(logged); got != toolCall+answer {
		t.Fatalf("response bodies logged = %q, want both streams as sent", got)
	}
	if files, err := filepath.Glob(filepath.Join(logDir, "*_tool_result_input_*.json")); err != nil || len(files) != 1 {
		t.Fatalf("tool round request files = %v, err = %v", files, err)
	}
}

// wireBodyRecord matches a DEBUG record of a response body read, holding
// what was read; the logger ends each record with a newline.
var wireBodyRecord = regexp.MustCompile(`(?s)\] WIRE BACKEND RESPONSE BODY:\n(.*?)\n(?:\[(?:DEBUG|INFO|WARN|ERROR)\] \[|\z)`)

// wireBodies joins, in order, what the DEBUG records of response bodies in
// log hold.
func wireBodies(log string) string {
	var joined strings.Builder
	for _, match := range wireBodyRecord.FindAllStringSubmatch(log, -1) {
		joined.WriteString(match[1])
	}
	return joined.String()
}

// At DEBUG, -list-models logs its request whole, the status and head of the
// response, and the body as it came: a long list sent in many writes, an
// error, and a list whose connection broke partway, as far as it arrived.
func TestCLIListModelsLogsAtDebug(t *testing.T) {
	var entries []string
	for i := 0; i < 2000; i++ {
		entries = append(entries, fmt.Sprintf(`{"id":"model-%04d","object":"model","owned_by":"test"}`, i))
	}
	list := `{"object":"list","data":[` + strings.Join(entries, ",") + `]}`
	failure := `{"error":{"message":"busy","type":"server_error","request_id":"req_models"}}`
	partial := `{"object":"list","data":[{"id":"model-0000",`
	for _, tt := range []struct {
		name   string
		serve  http.HandlerFunc
		status string
		sent   string
		err    string // what lmc reports, when it fails
	}{
		{
			name: "a long list in many writes",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				for rest := list; rest != ""; {
					n := min(len(rest), 1000)
					_, _ = io.WriteString(w, rest[:n])
					w.(http.Flusher).Flush()
					rest = rest[n:]
				}
			},
			status: "200 OK",
			sent:   list,
		},
		{
			name: "an error",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, failure)
			},
			status: "503 Service Unavailable",
			sent:   failure,
			err:    "HTTP 503",
		},
		{
			name: "a connection broken partway",
			serve: func(w http.ResponseWriter, r *http.Request) {
				conn, buf, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n" + partial)
				_ = buf.Flush()
			},
			status: "200 OK",
			sent:   partial,
			err:    "unexpected EOF",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.serve)
			defer server.Close()
			logDir := t.TempDir()
			args := []string{"-list-models", "-provider", "openai", "-provider-url", server.URL + "/v1", "-api-key-file", writeTestAPIKeyFile(t, "test-key"), "-log-dir", logDir, "-log-level", "DEBUG"}
			_, stderr, err := runLmcCommand(t, getLmcBinary(t), args, "")
			if (tt.err == "") != (err == nil) || !strings.Contains(stderr, tt.err) {
				t.Fatalf("err=%v stderr=%s, want it to say %q", err, stderr, tt.err)
			}
			logged := readProcessLog(t, logDir)
			for _, record := range []string{
				"] WIRE BACKEND REQUEST:\nGET /v1/models HTTP/1.1\r\n",
				"] WIRE BACKEND RESPONSE HEADERS:\nHTTP/1.1 " + tt.status + "\r\n",
			} {
				if strings.Count(logged, record) != 1 {
					t.Fatalf("want %q logged once:\n%s", record, logged)
				}
			}
			if got := wireBodies(logged); got != tt.sent {
				t.Fatalf("body logged = %d bytes %.200q, want the %d bytes sent", len(got), got, len(tt.sent))
			}
		})
	}
}
