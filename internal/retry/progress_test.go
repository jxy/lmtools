package retry

import (
	"context"
	"net/http"
	"net/http/httptest"
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
			if len(events) != 3 || events[0] != (Progress{Attempt: 1, Total: 2}) || events[1].Attempt != 2 || events[1].Total != 2 || events[1].Delay <= 0 || events[2] != (Progress{Attempt: 2, Total: 2}) {
				t.Fatalf("events=%+v", events)
			}
		})
	}
}
