package argo

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestHandleResponseEmbed(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Embed: true, LogDir: dir}
	body := []byte(`{"embedding":"E"}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	out, err := HandleResponse(cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "E" {
		t.Errorf("out = %q; want %q", out, "E")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !strings.HasSuffix(files[0].Name(), "_embed_output.json") {
		t.Errorf("got %q; want suffix _embed_output.json", files[0].Name())
	}
}

func TestHandleResponseChat(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{LogDir: dir}
	body := []byte(`{"response":"R"}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	out, err := HandleResponse(cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "R" {
		t.Errorf("out = %q; want %q", out, "R")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !strings.HasSuffix(files[0].Name(), "_chat_output.json") {
		t.Errorf("got %q; want suffix _chat_output.json", files[0].Name())
	}
}

func TestHandleResponseStreamChat(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{StreamChat: true, LogDir: dir}
	content := "STREAM"
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(content))}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	out, err := HandleResponse(cfg, resp)
	if err := w.Close(); err != nil {
		t.Fatalf("failed to close write pipe: %v", err)
	}
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("failed to copy output: %v", err)
	}

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("out = %q; want empty string", out)
	}
	if buf.String() != content {
		t.Errorf("stdout = %q; want %q", buf.String(), content)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !strings.HasSuffix(files[0].Name(), "_stream_chat_output.log") {
		t.Errorf("got %q; want suffix _stream_chat_output.log", files[0].Name())
	}
}
