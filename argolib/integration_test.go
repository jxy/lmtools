package argo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIntegrationEmbedAndChat verifies end-to-end embedding and chat workflows.
func TestIntegrationEmbedAndChat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/base/embed/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"embedding":[[1.0,2.0,3.0]]}`); err != nil {
			t.Fatalf("failed to write embedding response: %v", err)
		}
	})
	mux.HandleFunc("/base/chat/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"response":"Y"}`); err != nil {
			t.Fatalf("failed to write chat response: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	baseURL := server.URL + "/base"
	cfg := Config{Env: baseURL, Embed: true, Timeout: time.Second}

	// Embed path
	req, _, err := BuildRequest(cfg, "hello")
	if err != nil {
		t.Fatalf("BuildRequest(embed) failed: %v", err)
	}
	client := &http.Client{Timeout: cfg.Timeout}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("SendRequestWithTimeout(embed) failed: %v", err)
	}
	out, err := HandleResponse(ctx, cfg, resp)
	if err != nil {
		t.Fatalf("HandleResponse(embed) failed: %v", err)
	}
	expectedOut := "[1,2,3]"
	if out != expectedOut {
		t.Errorf("Embed output = %q; want %q", out, expectedOut)
	}

	// Chat path
	cfg.Embed = false
	req, _, err = BuildRequest(cfg, "hello")
	if err != nil {
		t.Fatalf("BuildRequest(chat) failed: %v", err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("SendRequestWithTimeout(chat) failed: %v", err)
	}
	out, err = HandleResponse(ctx, cfg, resp)
	if err != nil {
		t.Fatalf("HandleResponse(chat) failed: %v", err)
	}
	if out != "Y" {
		t.Errorf("Chat output = %q; want Y", out)
	}
}
