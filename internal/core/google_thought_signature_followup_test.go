package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/apifixtures"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type googleThoughtSignatureLogger struct {
	dir string
}

func (l googleThoughtSignatureLogger) GetLogDir() string { return l.dir }

func (googleThoughtSignatureLogger) LogJSON(string, string, []byte) error { return nil }

func (l googleThoughtSignatureLogger) CreateLogFile(logDir, prefix string) (*os.File, string, error) {
	f, err := os.CreateTemp(logDir, prefix+"-*.log")
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

func (googleThoughtSignatureLogger) Debugf(string, ...interface{}) {}

func (googleThoughtSignatureLogger) IsDebugEnabled() bool { return false }

type googleThoughtSignatureNotifier struct{}

func (googleThoughtSignatureNotifier) Infof(string, ...interface{})   {}
func (googleThoughtSignatureNotifier) Warnf(string, ...interface{})   {}
func (googleThoughtSignatureNotifier) Errorf(string, ...interface{})  {}
func (googleThoughtSignatureNotifier) Promptf(string, ...interface{}) {}

func TestGoogleThoughtSignatureRoundTripFromCapture_NonStreaming(t *testing.T) {
	testGoogleThoughtSignatureRoundTripFromCapture(t, false)
}

func TestGoogleThoughtSignatureRoundTripFromCapture_Streaming(t *testing.T) {
	testGoogleThoughtSignatureRoundTripFromCapture(t, true)
}

func testGoogleThoughtSignatureRoundTripFromCapture(t *testing.T, stream bool) {
	t.Helper()
	store := session.Open(filepath.Join(t.TempDir(), "sessions"))

	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	file := "captures/google.response.json"
	if stream {
		file = "captures/google-stream.stream.txt"
	}
	raw, err := apifixtures.ReadCaseFile(suite.Root, "anthropic-messages-basic-text", file)
	if err != nil {
		t.Fatalf("ReadCaseFile(%q) error = %v", file, err)
	}

	ctx := context.Background()
	logDir := t.TempDir()
	cfg := core.NewTestRequestConfig()
	cfg.Provider = "google"
	cfg.ProviderURL = "https://generativelanguage.googleapis.com/v1beta"
	cfg.Model = "gemini-3.1-flash-lite-preview"
	cfg.System = ""
	cfg.StreamChat = stream

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(raw)),
	}
	parsed, err := core.HandleResponse(ctx, cfg, resp, googleThoughtSignatureLogger{dir: logDir}, googleThoughtSignatureNotifier{})
	if err != nil {
		t.Fatalf("HandleResponse() error = %v", err)
	}
	if parsed.Text == "" {
		t.Fatal("parsed.Text is empty")
	}
	if parsed.ThoughtSignature == "" {
		t.Fatal("parsed.ThoughtSignature is empty")
	}

	info, _, err := store.Create(ctx)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	sess := &session.Session{Store: store, Number: info.Number, Identity: info.Identity}
	text := func(text string) session.Entry {
		return session.Entry{Role: core.RoleUser, Time: time.Now().UTC(), Content: session.Content{Blocks: []core.Block{core.TextBlock{Text: text}}}}
	}
	if _, err := sess.Append(ctx, text("Hello")); err != nil {
		t.Fatalf("Append(user) error = %v", err)
	}
	if _, err := sess.SaveReply(ctx, parsed, cfg.Model); err != nil {
		t.Fatalf("SaveReply() error = %v", err)
	}
	if _, err := sess.Append(ctx, text("Follow up")); err != nil {
		t.Fatalf("Append(follow-up) error = %v", err)
	}
	h, err := sess.History()
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	typedMessages := h.Typed()

	_, body, err := core.BuildChatRequest(cfg, typedMessages, core.ChatBuildOptions{ModelOverride: cfg.Model})
	if err != nil {
		t.Fatalf("BuildChatRequest() error = %v", err)
	}

	gotSignature := extractGoogleAssistantThoughtSignature(t, body)
	if gotSignature != parsed.ThoughtSignature {
		t.Fatalf("assistant thoughtSignature = %q, want %q", gotSignature, parsed.ThoughtSignature)
	}
}

func extractGoogleAssistantThoughtSignature(t *testing.T, body []byte) string {
	t.Helper()

	var payload struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text             string `json:"text"`
				ThoughtSignature string `json:"thoughtSignature"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(payload.Contents) < 3 {
		t.Fatalf("len(contents) = %d, want at least 3", len(payload.Contents))
	}
	if got := payload.Contents[1].Role; got != "model" {
		t.Fatalf("contents[1].role = %q, want %q", got, "model")
	}
	if len(payload.Contents[1].Parts) == 0 {
		t.Fatal("contents[1].parts is empty")
	}
	return payload.Contents[1].Parts[0].ThoughtSignature
}
