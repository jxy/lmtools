package retry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type trackedResponseBody struct {
	*strings.Reader
	closes int
}

func (b *trackedResponseBody) Close() error { b.closes++; return nil }

func TestFinalHTTPResponseRetainsBodyAndHeaders(t *testing.T) {
	for _, status := range []int{429, 503} {
		for _, retries := range []int{0, 2} {
			for _, direct := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/retries%d/direct%v", status, retries, direct), func(t *testing.T) {
					var bodies []*trackedResponseBody
					transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
						body := &trackedResponseBody{Reader: strings.NewReader(`{"message":"provider explanation"}`)}
						bodies = append(bodies, body)
						return &http.Response{StatusCode: status, Body: body, Request: req, Header: http.Header{"X-Request-Id": []string{"audit-id"}, "Retry-After": []string{"0"}}}, nil
					})
					retryer := NewRetryer(&Config{MaxRetries: retries, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BackoffFactor: 1}, nil)
					req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader("prompt"))
					client := &http.Client{Transport: transport}
					var resp *http.Response
					var err error
					if direct {
						resp, err = retryer.Do(context.Background(), client, req, []byte("prompt"))
					} else {
						resp, err = retryer.DoWithFunc(context.Background(), func() (*http.Response, error) { return client.Do(req) })
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(bodies) != retries+1 || resp.StatusCode != status || resp.Header.Get("X-Request-Id") != "audit-id" {
						t.Fatalf("attempts=%d response=%+v", len(bodies), resp)
					}
					for i, b := range bodies {
						want := 1
						if i == len(bodies)-1 {
							want = 0
						}
						if b.closes != want {
							t.Fatalf("body %d closes=%d want %d", i, b.closes, want)
						}
					}
					data, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					if string(data) != `{"message":"provider explanation"}` || bodies[len(bodies)-1].closes != 1 {
						t.Fatalf("final body=%q", data)
					}
				})
			}
		}
	}
}
