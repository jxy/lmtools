package retry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProgressReportsAttemptsAndWait(t *testing.T) {
	for _, path := range []string{"retryer", "client"} {
		t.Run(path, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer server.Close()
			var events []Progress
			ctx := WithProgress(context.Background(), func(p Progress) { events = append(events, p) })
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := NewRetryer(&Config{MaxRetries: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BackoffFactor: 1}, nil)
			var resp *http.Response
			if path == "client" {
				resp, err = NewClientForTesting(time.Second, 1, nil, nil).Do(ctx, req, "test")
			} else {
				resp, err = r.Do(ctx, server.Client(), req, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if len(events) != 3 || events[0] != (Progress{Attempt: 1, Total: 2}) || events[1].Attempt != 2 || events[1].Total != 2 || events[1].Delay <= 0 || events[1].Reason != "HTTP 503" || events[2] != (Progress{Attempt: 2, Total: 2}) {
				t.Fatalf("events=%+v", events)
			}
		})
	}
}

// A retried connection failure is named in a few words: the error text
// holds addresses and is no reason to show.
func TestProgressNamesConnectionFailures(t *testing.T) {
	for _, tt := range []struct {
		err    error
		reason string
	}{
		{timeoutError{}, "a timeout"},
		{temporaryError{}, "a connection error"},
		{io.ErrUnexpectedEOF, "a connection error"},
	} {
		var calls atomic.Int32
		transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return nil, tt.err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header), Request: req}, nil
		})
		client := newClient(clientOptions{transport: transport, retryConfig: &Config{MaxRetries: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BackoffFactor: 1}})
		var events []Progress
		ctx := WithProgress(context.Background(), func(p Progress) { events = append(events, p) })
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
		resp, err := client.Do(ctx, req, "lmc")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(events) != 3 || events[1].Reason != tt.reason {
			t.Errorf("%v: events=%+v, want reason %q", tt.err, events, tt.reason)
		}
	}
}
