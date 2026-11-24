package main

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	session "lmtools/internal/oldsession"
	"lmtools/internal/providerconfig"
	"lmtools/internal/providerrequest"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMainHelpFlags(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	help, err := os.CreateTemp(t.TempDir(), "help-")
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = help
	defer func() { os.Stderr = oldStderr; _ = help.Close() }()

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
			os.Args = tt.args
			notifier := core.NewTestNotifier()
			if err := run(notifier); err != nil {
				t.Fatalf("help failed: %v", err)
			}
			if len(notifier.ErrorMessages) != 0 || len(notifier.WarnMessages) != 0 {
				t.Fatalf("help printed diagnostics: %+v", notifier)
			}
		})
	}
}

// Only a gpt model on Argo's chat completions route gets the note: the
// Responses route returns summaries, and other models and providers return
// what they return.
func TestArgoChatHidesThinking(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts core.RequestOptions
		want string
	}{
		{"gpt on the chat route", core.RequestOptions{Provider: constants.ProviderArgo, Model: "gpt6astra"}, "Argo's chat completions route returns no thinking for gpt6astra; without -chat-completions, the Responses API returns reasoning summaries with -effort"},
		{"default model", core.RequestOptions{Provider: constants.ProviderArgo}, "returns no thinking for gpt5;"},
		{"Responses route", core.RequestOptions{Provider: constants.ProviderArgo, Model: "gpt6astra", OpenAIResponses: true}, ""},
		{"claude", core.RequestOptions{Provider: constants.ProviderArgo, Model: "claudeopus55"}, ""},
		{"legacy route", core.RequestOptions{Provider: constants.ProviderArgo, Model: "gpt6astra", ArgoLegacy: true}, ""},
		{"openai", core.RequestOptions{Provider: constants.ProviderOpenAI, Model: "gpt-5"}, ""},
		{"embed", core.RequestOptions{Provider: constants.ProviderArgo, Model: "gpt6astra", Embed: true}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := argoChatHidesThinking(tt.opts)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("note = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPersistAssistantOnlyWarnsButDoesNotFailOnSaveError(t *testing.T) {
	notifier := core.NewTestNotifier()
	err := persistAssistantOnly(
		context.Background(),
		core.Response{Text: "assistant text"},
		&session.Session{Path: filepath.Join(t.TempDir(), "missing-session")},
		&config.Config{},
		notifier,
		"gpt-test",
	)

	if err == nil {
		t.Fatal("persistAssistantOnly() error = nil, want the save failure returned for the turn outcome")
	}
	if len(notifier.WarnMessages) == 0 {
		t.Fatal("persistAssistantOnly() warning count = 0, want save failure warning")
	}
	if !strings.Contains(notifier.WarnMessages[0], "Failed to save response to session") {
		t.Fatalf("warning = %q, want save failure warning", notifier.WarnMessages[0])
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

func assertContainsAll(t *testing.T, got string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("got %q, want substring %q", got, want)
		}
	}
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
	assertContainsAll(t, got, []string{
		"curl -X POST",
		"-H 'Authorization: Bearer sk-test'",
		"-H 'Content-Type: application/json'",
		"--data-binary @-",
		"'https://api.example.com/v1/chat/completions?debug=true'",
		"<<'EOJ'\n{\"message\":\"Bob's answer\"}\nEOJ",
	})
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

func TestListModelsPrintCurlProviderURLIncludesAPIKeyHeader(t *testing.T) {
	apiKeyFile := writeTestAPIKeyFile(t, "test_key")
	req, err := providerrequest.BuildModelsRequest(providerrequest.ModelsRequestOptions{
		ProviderOptions: providerconfig.Options{
			Provider:    constants.ProviderOpenAI,
			ProviderURL: "http://localhost:8080/v1",
			APIKeyFile:  apiKeyFile,
		},
		RequireAPIKeyWithoutProviderURL: true,
	})
	if err != nil {
		t.Fatalf("BuildModelsRequest() error = %v", err)
	}

	got := renderCurlCommand(req, nil)
	assertContainsAll(t, got, []string{
		"curl -X GET",
		"-H 'Authorization: Bearer test_key'",
		"http://localhost:8080/v1/models",
	})
}

func TestBuildPrintCurlRequestArgoUserAddsAuthorizationHeader(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.ParseFlags([]string{
		"-argo-user", "testuser",
		"-model", "gpt4o",
		"-provider-url", "http://argo.example.test",
		"-print-curl",
	})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}

	opts := cfg.RequestOptions()
	rb, err := buildHTTPRequest(ctx, &cfg, opts, nil, "fresh preview")
	if err != nil {
		t.Fatalf("buildHTTPRequest() error = %v", err)
	}

	got := renderCurlCommand(rb.Request, rb.Body)
	assertContainsAll(t, got, []string{
		"-H 'Authorization: Bearer testuser'",
		"http://argo.example.test/v1/responses",
		"fresh preview",
	})
}

// A chat streams by default, through the Responses API where the provider
// and model have it; -chat-completions keeps Chat Completions, and the other
// routes keep their own API.
func TestDefaultChatRequestStreamsOnItsRoute(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		path string
	}{
		{"argo gpt", []string{"-argo-user", "testuser", "-model", "gpt6astra", "-provider-url", "http://argo.example.test"}, "/v1/responses"},
		{"argo gpt on chat", []string{"-argo-user", "testuser", "-model", "gpt6astra", "-provider-url", "http://argo.example.test", "-chat-completions"}, "/v1/chat/completions"},
		{"argo claude", []string{"-argo-user", "testuser", "-model", "claudeopus55", "-provider-url", "http://argo.example.test"}, "/v1/messages"},
		{"openai", []string{"-provider", "openai", "-model", "gpt-5", "-provider-url", "http://example.test/v1"}, "/v1/responses"},
		{"anthropic", []string{"-provider", "anthropic", "-api-key-file", writeTestAPIKeyFile(t, "key"), "-provider-url", "http://example.test/v1"}, "/v1/messages"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.ParseFlags(append(tt.args, "-no-session"))
			if err != nil {
				t.Fatalf("ParseFlags() error = %v", err)
			}
			rb, err := buildHTTPRequest(context.Background(), &cfg, cfg.RequestOptions(), nil, "hello")
			if err != nil {
				t.Fatalf("buildHTTPRequest() error = %v", err)
			}
			if rb.Request.URL.Path != tt.path {
				t.Fatalf("path = %q, want %q", rb.Request.URL.Path, tt.path)
			}
			var body struct {
				Stream bool `json:"stream"`
			}
			if err := json.Unmarshal(rb.Body, &body); err != nil || !body.Stream {
				t.Fatalf("body = %s, error = %v; want a streamed request", rb.Body, err)
			}
		})
	}
}

// Native Argo routes render the request in the wire's dialect, anthropic for
// claude models, Responses for gpt models, and chat completions with
// -chat-completions, and each must still ask for an uncompressed stream: Argo
// releases a compressed event stream only when the response ends.
func TestBuildStreamingArgoRequestAsksForUncompressedStream(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		path string
	}{
		{"messages", []string{"-model", "claudeopus4"}, "/v1/messages"},
		{"responses", []string{"-model", "gpt4o"}, "/v1/responses"},
		{"chat completions", []string{"-model", "gpt4o", "-chat-completions"}, "/v1/chat/completions"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.ParseFlags(append([]string{
				"-argo-user", "testuser",
				"-provider-url", "http://argo.example.test",
				"-no-session",
			}, tt.args...))
			if err != nil {
				t.Fatalf("ParseFlags() error = %v", err)
			}
			rb, err := buildHTTPRequest(context.Background(), &cfg, cfg.RequestOptions(), nil, "hello")
			if err != nil {
				t.Fatalf("buildHTTPRequest() error = %v", err)
			}
			if rb.Request.URL.Path != tt.path {
				t.Fatalf("path = %q, want %q", rb.Request.URL.Path, tt.path)
			}
			if got := rb.Request.Header.Get("Accept-Encoding"); got != "identity" {
				t.Fatalf("Accept-Encoding = %q, want identity", got)
			}
		})
	}
}

func writeTestAPIKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(key), constants.FilePerm); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
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
		"-chat-completions",
		"-resume", session.GetSessionID(sess.Path),
		"-sessions-dir", session.GetSessionsDir(),
		"-print-curl",
	})
	if err != nil {
		t.Fatalf("ParseFlags() error = %v", err)
	}

	opts := cfg.RequestOptions()
	plan, err := prepareSessionRequestPlan(ctx, &cfg, opts, core.NewTestNotifier(), core.TestToolUI{}, "preview question", false, session.PendingToolPreview)
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

func TestPersistAssistantOnlyPersistsWithoutPrinting(t *testing.T) {
	ctx := context.Background()
	oldDir := session.GetSessionsDir()
	session.SetSessionsDir(t.TempDir())
	t.Cleanup(func() { session.SetSessionsDir(oldDir) })

	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	response := core.Response{Text: "assistant answer"}
	out, err := captureStdout(t, func() error {
		return persistAssistantOnly(ctx, response, sess, &config.Config{}, core.NewTestNotifier(), "test-model")
	})
	if err != nil {
		t.Fatalf("persistAssistantOnly() error = %v", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want presentation to remain owned by responsePresenter", out)
	}
	messages, err := session.BuildMessagesWithToolInteractions(ctx, sess.Path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	if got := strings.Join(lmcTypedTextLines(messages), "\n"); got != "assistant:assistant answer" {
		t.Fatalf("session messages = %q, want saved assistant response", got)
	}
}

func TestFinishToolExecutionReturnsErrorWithoutPrinting(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return finishToolExecution(core.ToolExecutionResult{FinalText: "final answer"})
	})
	if err != nil {
		t.Fatalf("finishToolExecution() error = %v", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want final response to remain owned by responsePresenter", out)
	}

	out, err = captureStdout(t, func() error {
		return finishToolExecution(core.ToolExecutionResult{FinalText: "already streamed", FinalStreamed: true})
	})
	if err != nil {
		t.Fatalf("finishToolExecution(streamed) error = %v", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want no duplicate streamed text", out)
	}

	wantErr := errorf("tool failed")
	out, err = captureStdout(t, func() error {
		return finishToolExecution(core.ToolExecutionResult{FinalText: "ignored", Error: wantErr})
	})
	if err != wantErr {
		t.Fatalf("finishToolExecution(error) error = %v, want %v", err, wantErr)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want no output on error", out)
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
				Model: "custom-model",
				Options: providerconfig.Options{
					Provider: "argo",
				},
			},
			expected: "custom-model",
		},
		{
			name: "embed mode without explicit model",
			cfg: config.Config{
				Model: "",
				Embed: true,
				Options: providerconfig.Options{
					Provider: "argo",
				},
			},
			expected: core.DefaultEmbedModel,
		},
		{
			name: "argo provider default",
			cfg: config.Config{
				Model: "",
				Options: providerconfig.Options{
					Provider: "argo",
				},
			},
			expected: "gpt5",
		},
		{
			name: "empty provider defaults to argo",
			cfg: config.Config{
				Model: "",
				Options: providerconfig.Options{
					Provider: "",
				},
			},
			expected: "gpt5",
		},
		{
			name: "openai provider default",
			cfg: config.Config{
				Model: "",
				Options: providerconfig.Options{
					Provider: "openai",
				},
			},
			expected: "gpt-5",
		},
		{
			name: "google provider default",
			cfg: config.Config{
				Model: "",
				Options: providerconfig.Options{
					Provider: "google",
				},
			},
			expected: "gemini-2.5-pro",
		},
		{
			name: "anthropic provider default",
			cfg: config.Config{
				Model: "",
				Options: providerconfig.Options{
					Provider: "anthropic",
				},
			},
			expected: "claude-opus-4-1-20250805",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := actualModelForConfig(&tt.cfg, tt.cfg.RequestOptions())
			if actual != tt.expected {
				t.Errorf("actualModelForConfig() = %q, want %q", actual, tt.expected)
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

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()
	runErr := fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("stdout writer close error = %v", err)
	}
	out, readErr := io.ReadAll(reader)
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatalf("stdout reader close error = %v", closeErr)
	}
	if readErr != nil {
		t.Fatalf("stdout read error = %v", readErr)
	}
	return string(out), runErr
}

func lmcTypedTextLines(messages []core.TypedMessage) []string {
	out := make([]string, 0, len(messages))
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			text, ok := block.(core.TextBlock)
			if ok && text.Text != "" {
				out = append(out, msg.Role+":"+text.Text)
			}
		}
	}
	return out
}
