package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonRoundTripResponse(status int, payload interface{}) *http.Response {
	data, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(data)),
	}
}

func postResponses(t *testing.T, server http.Handler, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	return requestJSON(t, server, http.MethodPost, "/v1/responses", payload)
}

func getJSON(t *testing.T, server http.Handler, path string) map[string]interface{} {
	t.Helper()
	return requestJSON(t, server, http.MethodGet, path, nil)
}

func responseConversationID(payload map[string]interface{}) string {
	conv, _ := payload["conversation"].(map[string]interface{})
	id, _ := conv["id"].(string)
	return id
}

func assertConversationMetadataScope(t *testing.T, server http.Handler, convID, want string) {
	t.Helper()
	conv := getJSON(t, server, "/v1/conversations/"+convID)
	metadata, ok := conv["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("conversation metadata = %#v, want object", conv["metadata"])
	}
	if got := metadata["scope"]; got != want {
		t.Fatalf("conversation metadata scope = %#v, want %q; payload = %#v", got, want, conv)
	}
}

func assertResponseRecordMetadataScope(t *testing.T, server *Server, resp map[string]interface{}, want string) {
	t.Helper()
	respID, _ := resp["id"].(string)
	if respID == "" {
		t.Fatalf("response missing id: %#v", resp)
	}
	rec, ok, err := server.responsesState.loadResponse(respID)
	if err != nil || !ok {
		t.Fatalf("loadResponse(%q) = ok:%v err:%v", respID, ok, err)
	}
	if got := rec.Metadata["scope"]; got != want {
		t.Fatalf("response metadata scope = %#v, want %q; record = %#v", got, want, rec)
	}
}

func requestJSONStatus(t *testing.T, server http.Handler, method, path string, payload map[string]interface{}) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal(payload) error = %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody
}

func requestJSON(t *testing.T, server http.Handler, method, path string, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal(payload) error = %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s status = %d, body = %s", method, path, resp.StatusCode, string(respBody))
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v, body = %s", err, string(respBody))
	}
	return decoded
}

func waitForChannel(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

type blockingRequestBody struct {
	payload []byte
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRequestBody(payload []byte) *blockingRequestBody {
	return &blockingRequestBody{
		payload: append([]byte(nil), payload...),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingRequestBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	if len(b.payload) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.payload)
	b.payload = b.payload[n:]
	return n, nil
}

func waitForBackgroundIdle(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.backgroundMu.Lock()
		count := len(server.backgroundCancel)
		server.backgroundMu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	server.backgroundMu.Lock()
	count := len(server.backgroundCancel)
	server.backgroundMu.Unlock()
	t.Fatalf("background responses still running: %d", count)
}
