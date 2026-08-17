package proxy

// Converted-path allocation benchmarks.
//
// The proxy buffers and re-serializes request bodies on every converted path,
// so a large request costs a multiple of its own size in allocations. These
// benchmarks report that multiple directly as the "x-body" metric, which is the
// number to watch: B/op divided by the client body size.
//
// Two payload shapes are measured because they stress different costs. A single
// large attachment is dominated by copies of the payload bytes; a long
// transcript of small items is dominated by per-item map and interface
// overhead. Each runs with `store` off (conversion only) and on (adds the
// Responses state record and the session commit).
//
//	go test ./internal/proxy -run '^$' -bench BenchmarkConvertedResponsesRequest -benchmem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// benchBigAttachmentBody builds a request carrying one large base64 image, the
// shape a Codex session takes after a PDF page is attached.
func benchBigAttachmentBody(b *testing.B, megabytes int, store bool) []byte {
	b.Helper()
	payload := map[string]interface{}{
		"model": "gpt5",
		"store": store,
		"input": []map[string]interface{}{{
			"type": "message",
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "input_text", "text": "describe this page"},
				{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", megabytes*1024*1024), "detail": "high"},
			},
		}},
	}
	return mustBenchJSON(b, payload)
}

// benchLongTranscriptBody builds a request with many small items, the shape a
// long agentic session takes: alternating messages, tool calls, and results.
func benchLongTranscriptBody(b *testing.B, turns int, store bool) []byte {
	b.Helper()
	input := make([]map[string]interface{}, 0, turns*3)
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d", i)
		input = append(input,
			map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": fmt.Sprintf("step %d: inspect the build output and continue", i)},
				},
			},
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   callID,
				"name":      "shell",
				"arguments": fmt.Sprintf(`{"command":["bash","-lc","make step-%d"]}`, i),
			},
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  fmt.Sprintf("step %d completed with 0 warnings and 0 errors", i),
			},
		)
	}
	return mustBenchJSON(b, map[string]interface{}{"model": "gpt5", "store": store, "input": input})
}

func mustBenchJSON(b *testing.B, payload interface{}) []byte {
	b.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		b.Fatalf("json.Marshal(payload) error = %v", err)
	}
	return data
}

// newBenchConvertedServer returns a converted-path server whose upstream answers
// immediately, so the benchmark measures the proxy rather than the network.
func newBenchConvertedServer(b *testing.B) http.Handler {
	b.Helper()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		return jsonRoundTripResponse(http.StatusOK, map[string]interface{}{
			"id":      "chatcmpl-bench",
			"model":   "gpt5",
			"choices": []map[string]interface{}{{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		}), nil
	})
	config := &Config{
		Provider:           constants.ProviderArgo,
		ProviderURL:        "http://argo.local/argoapi",
		ArgoUser:           "argo-user",
		MaxRequestBodySize: 512 * 1024 * 1024,
		SessionsDir:        b.TempDir(),
	}
	client := retry.NewClientWithTransport(30*time.Second, 0, &retryLoggerAdapter{ctx: context.Background()}, extractRequestLogger, transport)
	return NewTestServerDirectWithClient(b, config, client)
}

func benchmarkConvertedResponses(b *testing.B, body []byte) {
	server := newBenchConvertedServer(b)

	// Warm up so first-call initialization is not attributed to the measurement.
	runBenchConvertedRequest(b, server, body)

	b.SetBytes(int64(len(body)))
	b.ReportAllocs()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runBenchConvertedRequest(b, server, body)
	}
	b.StopTimer()
	runtime.ReadMemStats(&after)

	// The headline number: bytes allocated per request as a multiple of the
	// client body size.
	perOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(b.N)
	b.ReportMetric(perOp/float64(len(body)), "x-body")
}

func runBenchConvertedRequest(b *testing.B, server http.Handler, body []byte) {
	b.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		b.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func BenchmarkConvertedResponsesRequest(b *testing.B) {
	cases := []struct {
		name string
		body func(*testing.B, bool) []byte
	}{
		{"big attachment 8MB", func(b *testing.B, store bool) []byte { return benchBigAttachmentBody(b, 8, store) }},
		{"long transcript 2000 turns", func(b *testing.B, store bool) []byte { return benchLongTranscriptBody(b, 2000, store) }},
	}
	for _, tc := range cases {
		for _, store := range []bool{false, true} {
			name := tc.name
			if store {
				name += "/store"
			} else {
				name += "/nostore"
			}
			b.Run(name, func(b *testing.B) {
				benchmarkConvertedResponses(b, tc.body(b, store))
			})
		}
	}
}
