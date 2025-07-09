package argo

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestHandleResponseEmbed(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Embed: true, LogDir: dir}
	body := []byte(`{"embedding":[[0.1,0.2,0.3]]}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	out, err := HandleResponse(context.Background(), cfg, resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedOut := "[0.1,0.2,0.3]"
	if out != expectedOut {
		t.Errorf("out = %q; want %q", out, expectedOut)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	// Check filename pattern matches new format with PID and random suffix
	filename := files[0].Name()
	if !strings.Contains(filename, "_embed_output_") || !strings.HasSuffix(filename, ".json") {
		t.Errorf("got %q; want filename containing '_embed_output_' and ending with '.json'", filename)
	}
}

func TestHandleResponseChat(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{LogDir: dir}
	body := []byte(`{"response":"R"}`)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
	out, err := HandleResponse(context.Background(), cfg, resp)
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
	// Check filename pattern matches new format with PID and random suffix
	filename := files[0].Name()
	if !strings.Contains(filename, "_chat_output_") || !strings.HasSuffix(filename, ".json") {
		t.Errorf("got %q; want filename containing '_chat_output_' and ending with '.json'", filename)
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

	out, err := HandleResponse(context.Background(), cfg, resp)
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
	// Check filename pattern matches new format with PID and random suffix
	filename := files[0].Name()
	if !strings.Contains(filename, "_stream_chat_output_") || !strings.HasSuffix(filename, ".log") {
		t.Errorf("got %q; want filename containing '_stream_chat_output_' and ending with '.log'", filename)
	}
}
