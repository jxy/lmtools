package proxy

import (
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// A reply with nothing in it, as Gemini's safety filter left through Argo,
// adds no assistant message to the stored conversation. The next request,
// sent with previous_response_id, carries the user's two messages and no
// empty assistant message between them, which backends refuse.
func TestResponsesStateStoresNoEmptyAssistantMessage(t *testing.T) {
	replies := []string{
		`{"id":"c1","object":"chat.completion","created":1,"model":"gemini4argon","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":null},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		`{"id":"c2","object":"chat.completion","created":2,"model":"gemini4argon","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
	}
	var mu sync.Mutex
	var sent []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reply := replies[min(len(sent), len(replies)-1)]
		sent = append(sent, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	defer backend.Close()
	handler, cleanup := NewTestServer(t, &Config{Provider: constants.ProviderArgo, ProviderURL: backend.URL + "/v1", ArgoUser: "testuser"})
	defer cleanup()

	_ = captureStderr(t, func() {
		code, body := requestJSONStatus(t, handler, http.MethodPost, "/v1/responses", map[string]interface{}{"model": "gemini4argon", "input": "first"})
		if code != http.StatusOK {
			t.Fatalf("first response status = %d, body = %s", code, body)
		}
		var first struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &first); err != nil || first.ID == "" {
			t.Fatalf("first response = %s, err = %v", body, err)
		}
		code, body = requestJSONStatus(t, handler, http.MethodPost, "/v1/responses", map[string]interface{}{"model": "gemini4argon", "previous_response_id": first.ID, "input": "second"})
		if code != http.StatusOK {
			t.Fatalf("second response status = %d, body = %s", code, body)
		}
	})

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("backend requests = %d, want 2", len(sent))
	}
	var round struct {
		Messages []struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(sent[1]), &round); err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, message := range round.Messages {
		roles = append(roles, message.Role)
	}
	if len(roles) != 2 || roles[0] != "user" || roles[1] != "user" || round.Messages[0].Content != "first" || round.Messages[1].Content != "second" {
		t.Fatalf("second request messages = %+v, want the two user messages alone", round.Messages)
	}
}
