package main

import (
	"context"
	"encoding/json"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(t *testing.T) {
	// This tests that main doesn't panic with help flag
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "help flag",
			args: []string{"lmc", "-h"},
		},
		{
			name: "help long flag",
			args: []string{"lmc", "-help"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture panic if any
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("main() panicked with args %v: %v", tt.args, r)
				}
			}()

			// Note: We can't actually test main() directly as it calls os.Exit
			// This is more of a compilation test
		})
	}
}

func TestGetExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: 0,
		},
		{
			name:     "generic error",
			err:      errorf("some error"),
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := getExitCode(tt.err)
			if code != tt.expected {
				t.Errorf("getExitCode(%v) = %d, want %d", tt.err, code, tt.expected)
			}
		})
	}
}

func TestGetOperationName(t *testing.T) {
	// This is a compilation test to ensure the function exists
	// We can't test it directly without creating a config
}

func TestRenderCurlCommand(t *testing.T) {
	req, err := http.NewRequest("POST", "https://api.example.com/v1/chat/completions?debug=true", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	body := []byte(`{"message":"Bob's answer"}`)

	got := renderCurlCommand(req, body)
	wants := []string{
		"curl -X POST",
		"-H 'Authorization: Bearer sk-test'",
		"-H 'Content-Type: application/json'",
		"--data-binary @-",
		"'https://api.example.com/v1/chat/completions?debug=true'",
		"<<'EOJ'\n{\"message\":\"Bob's answer\"}\nEOJ",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("renderCurlCommand() = %q, want substring %q", got, want)
		}
	}
}

func TestRenderCurlCommandPreservesHeaderValueOrder(t *testing.T) {
	req, err := http.NewRequest("GET", "https://api.example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Add("X-Test", "second")
	req.Header.Add("X-Test", "first")

	got := renderCurlCommand(req, nil)
	secondIdx := strings.Index(got, "-H 'X-Test: second'")
	firstIdx := strings.Index(got, "-H 'X-Test: first'")
	if secondIdx == -1 || firstIdx == -1 {
		t.Fatalf("renderCurlCommand() = %q, want both X-Test headers", got)
	}
	if secondIdx > firstIdx {
		t.Fatalf("renderCurlCommand() = %q, repeated header values were reordered", got)
	}
}

func TestRenderCurlCommandAvoidsHeredocDelimiterCollision(t *testing.T) {
	req, err := http.NewRequest("POST", "https://api.example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	body := []byte("line one\nEOJ\nline two")

	got := renderCurlCommand(req, body)
	if strings.Contains(got, "<<'EOJ'\n") {
		t.Fatalf("renderCurlCommand() used colliding delimiter: %q", got)
	}
	if !strings.Contains(got, "<<'EOJ_1'\n") || !strings.HasSuffix(got, "\nEOJ_1") {
		t.Fatalf("renderCurlCommand() = %q, want EOJ_1 delimiter", got)
	}
}

func TestBuildPrintCurlRequestResumeAppendsInputWithoutMutatingSession(t *testing.T) {
	ctx := context.Background()
	oldDir := session.GetSessionsDir()
	session.SetSessionsDir(t.TempDir())
	t.Cleanup(func() { session.SetSessionsDir(oldDir) })

	sess, err := session.CreateSession("session system", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	appendTestSessionMessage(t, ctx, sess, core.RoleUser, "hello")
	appendTestSessionMessage(t, ctx, sess, core.RoleAssistant, "hi")

	cfg, err := config.ParseFlags([]string{
		"-provider", "openai",
		"-provider-url", "http://example.test/v1",
		"-resume", session.GetSessionID(sess.Path),
		"-print-curl",
	})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}

	opts := cfg.RequestOptions()
	plan, err := prepareSessionRequestPlan(ctx, &cfg, opts, core.NewTestNotifier(), "preview question", false, session.PendingToolPreview)
	if err != nil {
		t.Fatalf("prepareSessionRequestPlan() error = %v", err)
	}
	rb, err := buildHTTPRequest(ctx, &cfg, opts, plan, "preview question")
	if err != nil {
		t.Fatalf("buildHTTPRequest() error = %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rb.Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	messages, ok := body["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v, want non-empty array", body["messages"])
	}
	last, ok := messages[len(messages)-1].(map[string]interface{})
	if !ok {
		t.Fatalf("last message = %#v, want object", messages[len(messages)-1])
	}
	if last["role"] != "user" || last["content"] != "preview question" {
		t.Fatalf("last message = %#v, want preview user input", last)
	}
	if _, err := os.Stat(filepath.Join(sess.Path, "0003.json")); !os.IsNotExist(err) {
		t.Fatalf("print-curl preview mutated session, 0003.json stat err = %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "simple-token", want: "simple-token"},
		{value: "", want: "''"},
		{value: "Content-Type: application/json", want: "'Content-Type: application/json'"},
		{value: "Bob's answer", want: "'Bob'\\''s answer'"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := shellQuote(tt.value); got != tt.want {
				t.Fatalf("shellQuote(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestGetActualModel(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		expected string
	}{
		{
			name: "explicit model provided",
			cfg: config.Config{
				Model:    "custom-model",
				Provider: "argo",
			},
			expected: "custom-model",
		},
		{
			name: "embed mode without explicit model",
			cfg: config.Config{
				Model:    "",
				Embed:    true,
				Provider: "argo",
			},
			expected: core.DefaultEmbedModel,
		},
		{
			name: "argo provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "argo",
			},
			expected: "gpt5",
		},
		{
			name: "empty provider defaults to argo",
			cfg: config.Config{
				Model:    "",
				Provider: "",
			},
			expected: "gpt5",
		},
		{
			name: "openai provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "openai",
			},
			expected: "gpt-5",
		},
		{
			name: "google provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "google",
			},
			expected: "gemini-2.5-pro",
		},
		{
			name: "anthropic provider default",
			cfg: config.Config{
				Model:    "",
				Provider: "anthropic",
			},
			expected: "claude-opus-4-1-20250805",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Compute actual model using the same logic as in main.go
			actual := tt.cfg.Model
			if actual == "" {
				if tt.cfg.Embed {
					actual = core.DefaultEmbedModel
				} else {
					provider := tt.cfg.Provider
					if provider == "" {
						provider = constants.ProviderArgo
					}
					actual = core.GetDefaultChatModel(provider)
				}
			}
			if actual != tt.expected {
				t.Errorf("computed model = %q, want %q", actual, tt.expected)
			}
		})
	}
}

// Helper to create simple errors for testing
type testError struct {
	msg string
}

func (e testError) Error() string {
	return e.msg
}

func errorf(format string, args ...interface{}) error {
	return testError{msg: format}
}

func appendTestSessionMessage(t *testing.T, ctx context.Context, sess *session.Session, role core.Role, content string) {
	t.Helper()
	_, err := session.AppendMessageWithToolInteraction(ctx, sess, session.Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}, nil, nil)
	if err != nil {
		t.Fatalf("AppendMessageWithToolInteraction(%s, %q) error = %v", role, content, err)
	}
}
