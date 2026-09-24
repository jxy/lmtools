package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/mcp"
	"lmtools/internal/mcp/mcptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets this test binary be the stdio server the client launches:
// with the scenario variable set it serves and exits before any test runs.
// With the relay variable set it plays lmc instead; see runRelayFromEnv.
// Every stdio shutdown waits its whole grace after SIGTERM, so the tests
// shorten the timing; a test about the timing sets its own.
func TestMain(m *testing.M) {
	mcptest.RunFromEnv()
	mcp.SetShutdownTimingForTest(200*time.Millisecond, 3*time.Second)
	runRelayFromEnv()
	os.Exit(m.Run())
}

// testLog collects the client's debug lines.
type testLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLog) logf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l *testLog) contains(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

func (l *testLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func echoTool() mcp.Tool {
	return mcp.Tool{
		Name:        "echo",
		Description: "Echo the input",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}
}

// stdioConfig makes a server config that re-executes this test binary as a
// fake server playing scn, recording what it receives under dir.
func stdioConfig(t *testing.T, name string, scn mcptest.Scenario) (mcp.ServerConfig, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	record := filepath.Join(t.TempDir(), name+".jsonl")
	scn.RecordPath = record
	env, err := mcptest.ScenarioEnv(scn)
	if err != nil {
		t.Fatalf("scenario env: %v", err)
	}
	return mcp.ServerConfig{
		Name:    name,
		Type:    mcp.TransportStdio,
		Command: executable,
		Env:     map[string]string{mcptest.EnvScenario: env},
	}, record
}

// readRecord loads the messages a stdio fake recorded.
func readRecord(t *testing.T, path string) []mcptest.Message {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read record: %v", err)
	}
	var messages []mcptest.Message
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var msg mcptest.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("decode record line %q: %v", line, err)
		}
		messages = append(messages, msg)
	}
	return messages
}

func methods(messages []mcptest.Message) []string {
	var out []string
	for _, msg := range messages {
		if msg.Method != "" {
			out = append(out, msg.Method)
		}
	}
	return out
}

func dialStdio(t *testing.T, name string, scn mcptest.Scenario, opts mcp.DialOptions) (*mcp.Client, string) {
	t.Helper()
	cfg, record := stdioConfig(t, name, scn)
	client, err := mcp.Dial(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, record
}

func callText(t *testing.T, client *mcp.Client, name string, args string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, name, json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	var texts []string
	for _, item := range result.Content {
		if item.Type == "text" {
			texts = append(texts, item.Text)
		}
	}
	return strings.Join(texts, "\n")
}
